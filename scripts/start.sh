#!/usr/bin/env bash
#
# 启动 autonomy —— 遵循「agent-control-plane-deployment」部署系统规范。
#
# 由控制面以 restartCmd 调用：cwd = runtimeDir，且注入
#   SERVICE_PORT = 服务契约里的端口（启动时注入；优先于通用 PORT）
#   PORT         = 服务契约 healthUrl 里的端口（SERVICE_PORT 未设时用它）
#   RUNTIME_DIR  = runtimeDir
#   APP_VERSION  = 本次部署的 8 位短 hash
#
# 监听端口优先级：SERVICE_PORT > PORT > 默认 4300（服务契约里 autonomy 的 port）。
# SERVICE_PORT 排在前面，是因为交互式 shell 里常残留别的服务的 PORT。
#
# runtime 布局（backend/ 下的内容由平台在部署时保留，不会被 --delete 清掉）：
#   bin/autonomyd           可执行文件（来自发版包）
#   scripts/*.sh            本目录（来自发版包）
#   src/agent_policy/       决策策略（来自发版包；PROJECT_ROOT 指到 runtimeDir）
#   backend/.env            可选覆盖项（首次启动自动生成，权限 600）
#   backend/data/           pid / 日志（库在 ~/database/autonomy/autonomy.db，全机一份）
#   backend/runtime.pid     进程号
#   backend/server.log      标准输出/错误
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="${RUNTIME_DIR:-$(cd "${DIR}/.." && pwd)}"
PORT="${SERVICE_PORT:-${PORT:-4300}}"
APP_VERSION="${APP_VERSION:-dev}"

# DATA_DIR 只放 pid/日志；库不在 backend/data 里了 —— 全机只有一份库，
# 见下面的 AUTONOMY_STORE_DSN。
BIN="${RUNTIME_DIR}/bin/autonomyd"
BACKEND="${RUNTIME_DIR}/backend"
ENV_FILE="${BACKEND}/.env"
DATA_DIR="${BACKEND}/data"
PID_FILE="${BACKEND}/runtime.pid"
LOG_FILE="${BACKEND}/server.log"
# 数据库引擎：sqlite（默认）或 postgres。sqlite 的库路径在下面 .env 载入之后推导
# （$AUTONOMY_STORE_DSN > $AUTONOMY_DATA_DIR/autonomy.db > ~/database/autonomy/autonomy.db）；
# postgres 的连接串由 .env / 环境给出（AUTONOMY_STORE_DSN 或 AUTONOMY_POSTGRES_DSN），
# 本脚本不推导、也不动它 —— 「全机一份库」只对 sqlite 的文件路径成立。

log() { echo "[start] $*"; }
die() { echo "[start][错误] $*" >&2; exit 1; }

case "${PORT}" in
  *[!0-9]*|"") die "端口必须是 1-65535 的整数，当前：${PORT}（来自 SERVICE_PORT/PORT）" ;;
esac
if [ "${PORT}" -lt 1 ] || [ "${PORT}" -gt 65535 ]; then
  die "端口超出范围：${PORT}（SERVICE_PORT/PORT 应为 1-65535）"
fi

[ -x "${BIN}" ] || die "缺少可执行文件 ${BIN}（发版包内容不完整？）"

mkdir -p "${DATA_DIR}"

# 首次启动生成 backend/.env：只放可覆盖项，权限 600，绝不入 git。
# 监听地址与库路径由本脚本按端口和 RUNTIME_DIR 推导，避免契约换端口后
# .env 里的旧值把服务卡在旧端口上。
if [ ! -f "${ENV_FILE}" ]; then
  umask 077
  cat > "${ENV_FILE}" <<'EOF'
# autonomy 运行期配置（首次启动自动生成，权限 600，请勿提交到 git）
# 监听端口不在这里配置：由 SERVICE_PORT（优先）或 PORT 决定，都没有则 4300。
# 数据库：全机只有一份（默认 ~/database/autonomy/autonomy.db），autonomy、
# 评测工具、SQL 编辑器看的是同一个文件。要换位置就设这里（或 AUTONOMY_DATA_DIR
# 只换目录）；没有特殊原因不要改，改了就等于换一个库。
# AUTONOMY_STORE_DSN=/Users/gaolei/database/autonomy/autonomy.db
# 换引擎（postgres）：老数据留在上面那个 sqlite 文件里，不迁移；postgres 那边是新建的库，
# 连接串由 AUTONOMY_STORE_DSN 或 AUTONOMY_POSTGRES_DSN 给出（后者是它的默认 DSN）。改完走平台
# 重启，再用 /health 确认，见 docs/store.md「两个引擎，两份数据」。
# AUTONOMY_STORE_ENGINE=postgres
# AUTONOMY_POSTGRES_DSN=postgres://user:pass@127.0.0.1:5432/autonomy?sslmode=disable
# 读写分离：主库还是写侧，另外给一个**读侧**连接串（本机那份副本），看得多、改得少的读
# 就走本地。立副本：scripts/local-replica.sh（见 docs/local-replica.md）；读侧连不上不会
# 失败，只是退回主库并warning。见 docs/store.md「读写分离」。
# AUTONOMY_STORE_READ_DSN=postgres://user:pass@127.0.0.1:5433/autonomy?sslmode=disable
# 推理后端：local（离线）或 llm。部署后按需要改，再走平台重启。
AUTONOMY_REASONER=llm
# LLM 后端（默认 harness）：cursor（默认）/ cline / codex。发版包自带两个 Node 桥
# （src/clinesdk/bridge、src/codexsdk/bridge + 依赖包，本脚本自动解包并指过去），
# 所以桥不用配。见 docs/llm-backend.md；改完走平台重启，再用 /health 确认。
#
# 凭据不在这里：一个 agent 用哪把 key（哪个账号）由**账号池**决定 —— GET /accounts 页面或
# /api/accounts，见 docs/accounts.md。池子里没有该 harness 的启用账号时，run 会被明确拒绝
# （不再回落到环境变量），所以顺序是：先加账号，再部署/重启。
# AUTONOMY_LLM_BACKEND=cline
# AUTONOMY_CLINE_BRIDGE_SCRIPT=/absolute/path/to/some/checkout/src/clinesdk/bridge/bridge.mjs
# AUTONOMY_MAX_STEPS=4
# provider 原始事件流（llm_events，一行一个逐 token 事件）默认不落库：它是一张叶子表，
# 只服务回放，run header（reason_turns）与对话（llm_messages）照常记录。需要回放/分析时再打开：
# AUTONOMY_LLM_EVENTS=1
# cursor bridge 默认用发版包自带的（bin/cursor-sdk-bridge，本脚本自动指过去）。
# 想换桥（或指向别处的下载产物）在这里覆盖；外部桥用
# CURSOR_SDK_BRIDGE_URL + CURSOR_SDK_BRIDGE_TOKEN。
# CURSOR_SDK_BRIDGE_BIN=/absolute/path/to/cursor-sdk-bridge
EOF
  chmod 600 "${ENV_FILE}"
  log "已生成 ${ENV_FILE}"
fi

# shellcheck disable=SC1090
set -a; . "${ENV_FILE}"; set +a

# 平台注入的端口优先：不让 .env 里的 AUTONOMY_HTTP_ADDR 把服务钉在旧端口。
export AUTONOMY_HTTP_ADDR="127.0.0.1:${PORT}"
# 引擎与库：sqlite（默认）走「全机一份库」那条推导，显式给了就听显式的；postgres 的连接串
# 原样传下去，脚本不为它建目录、也不猜它。
STORE_ENGINE="${AUTONOMY_STORE_ENGINE:-sqlite}"
if [ "${STORE_ENGINE}" = "sqlite" ]; then
  DB_PATH="${AUTONOMY_STORE_DSN:-${AUTONOMY_DATA_DIR:-${HOME}/database/autonomy}/autonomy.db}"
  export AUTONOMY_STORE_DSN="${DB_PATH}"
  mkdir -p "$(dirname "${AUTONOMY_STORE_DSN}")"
fi
export PROJECT_ROOT="${RUNTIME_DIR}"
export APP_VERSION

# 发版包自带的 cursor bridge（build.sh 放进 bin/）：runtime 的 defaultBridgeBinary
# 只按【进程 cwd】找 third_party/bin/cursor-sdk-bridge，而部署的 cwd 是 runtimeDir
# —— 所以这里明说它在哪。包里有它就**用它**（连 `.env` 里的值也覆盖）：backend/.env
# 每次部署都保留，一条指向某个 checkout 的路径会把桥钉死在那份没人更新的代码上（和上面
# 端口、以及下面的 cline 桥同一条规矩）。要跑外部的桥就设 CURSOR_SDK_BRIDGE_URL
# （附到外部桥，那是一种模式而不是一条路径，这里不动它）；包里的缺席时，才听 .env 的
# CURSOR_SDK_BRIDGE_BIN。
if [ -z "${CURSOR_SDK_BRIDGE_URL:-}" ] && [ -x "${RUNTIME_DIR}/bin/cursor-sdk-bridge" ]; then
  if [ -n "${CURSOR_SDK_BRIDGE_BIN:-}" ] && [ "${CURSOR_SDK_BRIDGE_BIN}" != "${RUNTIME_DIR}/bin/cursor-sdk-bridge" ]; then
    log "cursor 桥：用发版包里的 ${RUNTIME_DIR}/bin/cursor-sdk-bridge（.env 里的 ${CURSOR_SDK_BRIDGE_BIN} 不参与）"
  fi
  export CURSOR_SDK_BRIDGE_BIN="${RUNTIME_DIR}/bin/cursor-sdk-bridge"
fi

# —— Node 桥（cline / codex）：发版包自带源码 + 依赖包 ——
# 每个 harness 的桥都是「一个 Node 脚本 + 一棵依赖树」，处理方式完全一样，所以这里是一
# 个函数而不是每加一个后端抄一遍：依赖解到包外的缓存
# （${RUNTIME_DIR}/.cache/<name>-bridge/<sha256>，部署 rsync 的 --delete 不碰它），源码目录建
# node_modules 符号链接（node 从脚本目录往上解析裸包名，ESM 又忽略 NODE_PATH，所以包必须
# 挨着 bridge.mjs，但不必「是」它），然后把 AUTONOMY_<NAME>_BRIDGE_SCRIPT 指到包里的桥 ——
# 部署跑的就是这个发版包带的桥。
#
# 为什么用缓存：部署会 rsync --delete 整个目录，包里没有的东西会被换掉（平台只保留
# data/ logs/ backend/data/ backend/server.log Library/ packages/ upgrade-requests/，并排除
# .cache/）。依赖是构建产物、不是包内容：按 tarball 的 sha256 做 key，每个版本每台机只解一
# 次、跨部署复用；而 tarball 仍在包里，冷机器不需要 npm 也不需要网络就能启动。
#
# backend/.env 里的路径被**刻意覆盖**（与端口同一条规矩）：它每次部署都保留，一条指向某个
# checkout 的路径会把桥钉死在那份没人更新的代码上。要跑外部的桥，就让包里那份缺席。
node_bridge_setup() { # <name> <package-relative dir> <script env var>
  local name="$1" pkgdir="$2" scriptenv="$3"
  local dir="${RUNTIME_DIR}/${pkgdir}"
  local cache="${AUTONOMY_CACHE_DIR:-${RUNTIME_DIR}/.cache}/${name}-bridge"
  [ -f "${dir}/bridge.mjs" ] || return 0
  local tgz="${dir}/bridge-deps.tgz"
  if [ -f "${tgz}" ]; then
    local sha=""
    if command -v shasum >/dev/null 2>&1; then
      sha="$(shasum -a 256 "${tgz}" | cut -d' ' -f1)"
    else
      sha="$(sha256sum "${tgz}" | cut -d' ' -f1)"
    fi
    if [ -n "${sha}" ]; then
      local deps_dir="${cache}/${sha}"
      if [ ! -d "${deps_dir}/node_modules" ]; then
        mkdir -p "${cache}"
        log "解包 ${name} bridge 依赖（$(du -h "${tgz}" | cut -f1) → ${deps_dir}）"
        local staging="${deps_dir}.tmp.$$"
        rm -rf "${staging}"
        if mkdir -p "${staging}" && tar xzf "${tgz}" -C "${staging}"; then
          rm -rf "${deps_dir}"
          mv "${staging}" "${deps_dir}"
          # 只留最近两个版本，别让缓存无限长。
          ls -1dt "${cache}"/*/ 2>/dev/null | sed -e '1,2d' | while read -r old; do rm -rf "${old}"; done
        else
          rm -rf "${staging}"
          log "警告：${name} bridge 依赖解包失败（沿用已有 node_modules，如果有）"
        fi
      fi
      if [ -d "${deps_dir}/node_modules" ] && [ ! -e "${dir}/node_modules" ]; then
        rm -rf "${dir}/node_modules"
        ln -s "${deps_dir}/node_modules" "${dir}/node_modules" 2>/dev/null ||
          cp -R "${deps_dir}/node_modules" "${dir}/node_modules" 2>/dev/null ||
          log "警告：${name} bridge 依赖链接失败（symlink 与拷贝都不成）"
      fi
    fi
  fi
  if [ -e "${dir}/node_modules" ]; then
    export "${scriptenv}=${dir}/bridge.mjs"
  else
    log "警告：包里有 ${name} bridge 却没有依赖（bridge-deps.tgz 不在/解包失败）；沿用 ${scriptenv}=${!scriptenv:-未配置}"
  fi
}

node_bridge_setup cline src/clinesdk/bridge AUTONOMY_CLINE_BRIDGE_SCRIPT
node_bridge_setup codex src/codexsdk/bridge AUTONOMY_CODEX_BRIDGE_SCRIPT

# —— 启动前自检：所选后端的桥必须真的能用 ——
# 这一层是刻意「宁可这次部署失败，也不要起一个跑不了任务的服务」：桥是 llm 后端唯一的
# 执行通道，缺了它 /health 照样 ok，但每一个任务都会失败在「ping the bridge」那一步 ——
# 静默降级最难查。自检不过 = 本脚本非 0 退出 = 平台把本次部署判为失败，线上留在上一个
# 可用版本（.cache 里还留着上一版的依赖，回滚不用重新解包）。
#   * reasoner=local（离线推理）不经过任何 LLM 后端，跳过；
#   * 需要临时放行（比如排查问题）时设 AUTONOMY_SKIP_BRIDGE_CHECK=1。
if [ "${AUTONOMY_SKIP_BRIDGE_CHECK:-0}" = "1" ]; then
  log "跳过桥自检（AUTONOMY_SKIP_BRIDGE_CHECK=1）"
elif [ "${AUTONOMY_REASONER:-llm}" = "local" ]; then
  log "跳过桥自检（AUTONOMY_REASONER=local：不经过 LLM 后端）"
elif [ "${AUTONOMY_LLM_BACKEND:-cursor}" = "cline" ] || [ "${AUTONOMY_LLM_BACKEND:-cursor}" = "codex" ]; then
  # 两个 Node 桥的后端检查一样，只有坐标不同。
  node_backend="${AUTONOMY_LLM_BACKEND:-cursor}"
  case "${node_backend}" in
  cline)
    node_script="${AUTONOMY_CLINE_BRIDGE_SCRIPT:-}"
    node_pkgdir="src/clinesdk/bridge"
    node_deps_pkg="@cline/sdk"
    node_bin_var="AUTONOMY_CLINE_NODE_BIN"
    node_script_var="AUTONOMY_CLINE_BRIDGE_SCRIPT"
    ;;
  codex)
    node_script="${AUTONOMY_CODEX_BRIDGE_SCRIPT:-}"
    node_pkgdir="src/codexsdk/bridge"
    node_deps_pkg="@openai/codex-sdk"
    node_bin_var="AUTONOMY_CODEX_NODE_BIN"
    node_script_var="AUTONOMY_CODEX_BRIDGE_SCRIPT"
    ;;
  esac
  [ -n "${node_script}" ] ||
    die "${node_backend} 后端，但没有桥脚本：包里缺 ${node_pkgdir}/bridge.mjs，且 .env 没设 ${node_script_var}"
  [ -f "${node_script}" ] || die "${node_backend} 桥脚本不存在：${node_script}"
  # 依赖必须能被 Node 从脚本目录往上解析到（bridge-deps.tgz 解出的缓存 + symlink）。
  node_deps=""
  probe="$(cd "$(dirname "${node_script}")" && pwd)"
  for _ in 1 2 3 4; do
    if [ -d "${probe}/node_modules/${node_deps_pkg}" ]; then
      node_deps="${probe}/node_modules"
      break
    fi
    up="$(dirname "${probe}")"
    [ "${up}" = "${probe}" ] && break
    probe="${up}"
  done
  [ -n "${node_deps}" ] ||
    die "${node_backend} 桥的依赖解析不到：${node_script} 及其上级都没有 node_modules/${node_deps_pkg}（包缺 bridge-deps.tgz，或解包/链接失败）"
  # 真加载一次（不联网、不用凭据）：桥 load 不起来，这次部署就不该上。
  node_bin="${!node_bin_var:-node}"
  node_reply="$(printf '{"id":"start-self-check","cmd":"ping"}
' | "${node_bin}" "${node_script}" 2>&1 | head -1 || true)"
  case "${node_reply}" in
  *'"type":"ready"'*)
    log "${node_backend} 桥自检通过：${node_bin} ${node_script} → ready（依赖 ${node_deps}）"
    ;;
  *)
    die "${node_backend} 桥自检失败：${node_bin} ${node_script} 没有回应 ready（输出：$(printf '%s' "${node_reply}" | head -c 200)）"
    ;;
  esac
else
  if [ -n "${CURSOR_SDK_BRIDGE_URL:-}" ]; then
    log "cursor 桥自检通过：使用外部桥 URL ${CURSOR_SDK_BRIDGE_URL}"
  elif [ -n "${CURSOR_SDK_BRIDGE_BIN:-}" ] && [ -x "${CURSOR_SDK_BRIDGE_BIN}" ]; then
    log "cursor 桥自检通过：${CURSOR_SDK_BRIDGE_BIN}"
  else
    die "cursor 后端，但没有可执行的 cursor bridge：包里缺 bin/cursor-sdk-bridge（构建机上先跑 scripts/fetch-bridge.sh），且 .env 没设 CURSOR_SDK_BRIDGE_BIN / CURSOR_SDK_BRIDGE_URL"
  fi
fi

if [ -f "${PID_FILE}" ]; then
  old="$(tr -d '[:space:]' < "${PID_FILE}" || true)"
  if [ -n "${old}" ] && kill -0 "${old}" 2>/dev/null; then
    log "已在运行 pid=${old}"
    exit 0
  fi
  rm -f "${PID_FILE}"
fi

if command -v lsof >/dev/null 2>&1; then
  holder="$(lsof -nP -iTCP:"${PORT}" -sTCP:LISTEN -t 2>/dev/null | head -1 || true)"
  if [ -n "${holder}" ]; then
    die "端口 ${PORT} 已被 pid=${holder} 占用：$(ps -o command= -p "${holder}" 2>/dev/null | head -c 160)
      请显式指定端口（SERVICE_PORT=…），或先停掉占用者"
  fi
fi

if [ "${STORE_ENGINE}" = "sqlite" ]; then
  log "启动 部署版本=${APP_VERSION} 监听=${AUTONOMY_HTTP_ADDR} 引擎=sqlite 库=${AUTONOMY_STORE_DSN} 后端=${AUTONOMY_LLM_BACKEND:-cursor} 桥=${CURSOR_SDK_BRIDGE_BIN:-未配置} cline桥=${AUTONOMY_CLINE_BRIDGE_SCRIPT:-未配置} codex桥=${AUTONOMY_CODEX_BRIDGE_SCRIPT:-未配置}"
else
  log "启动 部署版本=${APP_VERSION} 监听=${AUTONOMY_HTTP_ADDR} 引擎=${STORE_ENGINE} 后端=${AUTONOMY_LLM_BACKEND:-cursor} 桥=${CURSOR_SDK_BRIDGE_BIN:-未配置} cline桥=${AUTONOMY_CLINE_BRIDGE_SCRIPT:-未配置} codex桥=${AUTONOMY_CODEX_BRIDGE_SCRIPT:-未配置}"
fi
nohup "${BIN}" >> "${LOG_FILE}" 2>&1 &
echo $! > "${PID_FILE}"
pid="$(cat "${PID_FILE}")"

# 探活：/health 是平台对每个服务统一探的路径。
for _ in $(seq 1 40); do
  if ! kill -0 "${pid}" 2>/dev/null; then
    rm -f "${PID_FILE}"
    echo "[start][错误] 进程已退出，最近日志：" >&2
    tail -20 "${LOG_FILE}" >&2 || true
    exit 1
  fi
  if curl -fsS -m 2 "http://127.0.0.1:${PORT}/health" >/dev/null 2>&1; then
    log "启动成功 pid=${pid} log=${LOG_FILE}"
    exit 0
  fi
  sleep 0.5
done

echo "[start][错误] 20s 内 /health 未就绪，最近日志：" >&2
tail -20 "${LOG_FILE}" >&2 || true
kill "${pid}" 2>/dev/null || true
rm -f "${PID_FILE}"
exit 1
