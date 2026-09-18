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
# 监听端口优先级：SERVICE_PORT > PORT > 默认 4230。
# SERVICE_PORT 排在前面，是因为交互式 shell 里常残留别的服务的 PORT。
#
# runtime 布局（backend/ 下的内容由平台在部署时保留，不会被 --delete 清掉）：
#   bin/autonomyd           可执行文件（来自发版包）
#   scripts/*.sh            本目录（来自发版包）
#   src/agent_policy/       决策策略（来自发版包；PROJECT_ROOT 指到 runtimeDir）
#   backend/.env            可选覆盖项（首次启动自动生成，权限 600）
#   backend/data/           SQLite（autonomy.db）
#   backend/runtime.pid     进程号
#   backend/server.log      标准输出/错误
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="${RUNTIME_DIR:-$(cd "${DIR}/.." && pwd)}"
PORT="${SERVICE_PORT:-${PORT:-4230}}"
APP_VERSION="${APP_VERSION:-dev}"

BIN="${RUNTIME_DIR}/bin/autonomyd"
BACKEND="${RUNTIME_DIR}/backend"
ENV_FILE="${BACKEND}/.env"
DATA_DIR="${BACKEND}/data"
PID_FILE="${BACKEND}/runtime.pid"
LOG_FILE="${BACKEND}/server.log"

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
  cat > "${ENV_FILE}" <<EOF
# autonomy 运行期配置（首次启动自动生成，权限 600，请勿提交到 git）
# 监听端口不在这里配置：由 SERVICE_PORT（优先）或 PORT 决定，都没有则 4230。
# 推理后端：local（离线）或 llm。部署后按需要改，再走平台重启。
AUTONOMY_REASONER=llm
# AUTONOMY_LLM_BACKEND=cline
# AUTONOMY_MAX_STEPS=4
EOF
  chmod 600 "${ENV_FILE}"
  log "已生成 ${ENV_FILE}"
fi

# shellcheck disable=SC1090
set -a; . "${ENV_FILE}"; set +a

# 平台注入的端口优先：不让 .env 里的 AUTONOMY_HTTP_ADDR 把服务钉在旧端口。
export AUTONOMY_HTTP_ADDR="127.0.0.1:${PORT}"
export AUTONOMY_STORE_DSN="${DATA_DIR}/autonomy.db"
export PROJECT_ROOT="${RUNTIME_DIR}"
export APP_VERSION

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

log "启动 部署版本=${APP_VERSION} 监听=${AUTONOMY_HTTP_ADDR} 库=${AUTONOMY_STORE_DSN}"
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
