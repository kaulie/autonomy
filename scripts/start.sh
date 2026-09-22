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
# LLM 后端：cursor（默认）或 cline。切 cline 之前先读 docs/llm-backend.md ——
# 发版包自带 cline 桥（src/clinesdk/bridge + 依赖包，start.sh 自动解包并指过去），
# 所以 provider/model 不设就由 `cline auth` 保存的配置解析。这一行只在包的桥
# 不可用时才需要（指到一个装好 @cline/sdk 的桥脚本）。改完走平台重启，再用 /health 确认。
# AUTONOMY_LLM_BACKEND=cline
# AUTONOMY_CLINE_PROVIDER=deepseek
# AUTONOMY_CLINE_MODEL=deepseek-v4-pro
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
# —— 所以这里明说它在哪。backend/.env 里显式设了 CURSOR_SDK_BRIDGE_BIN 就听它的；
# 也可以自己指向别处的桥（或干脆用 CURSOR_SDK_BRIDGE_URL 附到外部桥）。
if [ -z "${CURSOR_SDK_BRIDGE_BIN:-}" ] && [ -x "${RUNTIME_DIR}/bin/cursor-sdk-bridge" ]; then
  export CURSOR_SDK_BRIDGE_BIN="${RUNTIME_DIR}/bin/cursor-sdk-bridge"
fi

# cline bridge: the release package carries it (build.sh) — its sources, plus a tarball
# of their production install (@cline/sdk and its tree: 251MB unpacked, ~27MB packed).
# The dependencies are extracted next to the sources when the tarball is new, and
# AUTONOMY_CLINE_BRIDGE_SCRIPT is pointed at them — so the deployed cline backend runs
# the bridge *this release* shipped, refreshed by every deploy.
#
# backend/.env is overridden on purpose, the same rule as the port above: it survives
# every deploy, so a path into some checkout would pin the bridge to code nobody
# updates. Set AUTONOMY_CLINE_BRIDGE_SCRIPT in .env only for a deployment whose package
# carries no bridge (or to run an external one).
CLINE_BRIDGE_DIR="${RUNTIME_DIR}/src/clinesdk/bridge"
if [ -f "${CLINE_BRIDGE_DIR}/bridge.mjs" ]; then
  DEPS_TGZ="${CLINE_BRIDGE_DIR}/bridge-deps.tgz"
  if [ -f "${DEPS_TGZ}" ]; then
    if command -v shasum >/dev/null 2>&1; then
      wanted="$(shasum -a 256 "${DEPS_TGZ}" | cut -d' ' -f1)"
    else
      wanted="$(sha256sum "${DEPS_TGZ}" | cut -d' ' -f1)"
    fi
    current="$(cat "${CLINE_BRIDGE_DIR}/.deps-sha256" 2>/dev/null || true)"
    if [ -n "${wanted}" ] && [ "${wanted}" != "${current}" ]; then
      log "解包 cline bridge 依赖（$(du -h "${DEPS_TGZ}" | cut -f1)）"
      if tar xzf "${DEPS_TGZ}" -C "${CLINE_BRIDGE_DIR}"; then
        printf '%s' "${wanted}" > "${CLINE_BRIDGE_DIR}/.deps-sha256"
      else
        log "警告：cline bridge 依赖解包失败（沿用已有 node_modules，如果有）"
      fi
    fi
  fi
  if [ -d "${CLINE_BRIDGE_DIR}/node_modules" ]; then
    export AUTONOMY_CLINE_BRIDGE_SCRIPT="${CLINE_BRIDGE_DIR}/bridge.mjs"
  else
    log "警告：包里有 cline bridge 却没有依赖（bridge-deps.tgz 不在/解包失败）；沿用 AUTONOMY_CLINE_BRIDGE_SCRIPT=${AUTONOMY_CLINE_BRIDGE_SCRIPT:-未配置}"
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
  log "启动 部署版本=${APP_VERSION} 监听=${AUTONOMY_HTTP_ADDR} 引擎=sqlite 库=${AUTONOMY_STORE_DSN} 后端=${AUTONOMY_LLM_BACKEND:-cursor} 桥=${CURSOR_SDK_BRIDGE_BIN:-未配置} cline桥=${AUTONOMY_CLINE_BRIDGE_SCRIPT:-未配置}"
else
  log "启动 部署版本=${APP_VERSION} 监听=${AUTONOMY_HTTP_ADDR} 引擎=${STORE_ENGINE} 后端=${AUTONOMY_LLM_BACKEND:-cursor} 桥=${CURSOR_SDK_BRIDGE_BIN:-未配置} cline桥=${AUTONOMY_CLINE_BRIDGE_SCRIPT:-未配置}"
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
