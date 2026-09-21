#!/usr/bin/env bash
#
# 停止 autonomy（TERM → 等待 → KILL）。
#
# 进程自己会优雅收尾（src/graceful.go 的 Autonomy.Shutdown，cmd/autonomyd 抓 SIGTERM）：
# 不再启动新 run、在途 run 最多等 AUTONOMY_SHUTDOWN_GRACE（默认 10s）自己回来、剩下的
# 停掉并落库，最后关会话与 Store。所以这里的等待窗口是那个 grace 的上界 ——
# 想给它更长时间就同时调大 AUTONOMY_STOP_TIMEOUT（本脚本，默认 15s）与
# AUTONOMY_SHUTDOWN_GRACE（服务端）。等不到才 KILL，那一刀想写什么都写不进去。
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="${RUNTIME_DIR:-$(cd "${DIR}/.." && pwd)}"
PID_FILE="${RUNTIME_DIR}/backend/runtime.pid"
# 等待窗口（秒）：默认 15s（≥ AUTONOMY_SHUTDOWN_GRACE 默认的 10s）。
WAIT_SECONDS="${AUTONOMY_STOP_TIMEOUT:-15}"

log() { echo "[stop] $*"; }

if [ ! -f "${PID_FILE}" ]; then
  log "没有 pid 文件，视为未运行"
  exit 0
fi

pid="$(tr -d '[:space:]' < "${PID_FILE}" || true)"
if [ -z "${pid}" ] || ! kill -0 "${pid}" 2>/dev/null; then
  log "进程 ${pid:-?} 不存在，清理 pid 文件"
  rm -f "${PID_FILE}"
  exit 0
fi

log "TERM → pid=${pid}（优雅停止，最多等 ${WAIT_SECONDS}s）"
kill "${pid}" 2>/dev/null || true
attempts="$(( WAIT_SECONDS * 2 ))"
for _ in $(seq 1 "${attempts}"); do
  if ! kill -0 "${pid}" 2>/dev/null; then
    rm -f "${PID_FILE}"
    log "已停止"
    exit 0
  fi
  sleep 0.5
done

log "未在 ${WAIT_SECONDS}s 内退出，KILL → pid=${pid}"
kill -9 "${pid}" 2>/dev/null || true
rm -f "${PID_FILE}"
log "已强制停止"
