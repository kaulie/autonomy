#!/usr/bin/env bash
#
# 停止 autonomy（TERM → 等待 → KILL）。
#
# 进程自己会优雅收尾（src/graceful.go 的 Autonomy.Shutdown，cmd/autonomyd 抓 SIGTERM）：
# 不再启动新 run、在途 run 最多等 AUTONOMY_SHUTDOWN_GRACE（默认 10s）自己回来、剩下的
# 停掉并落库，最后关会话与 Store。所以这里的等待窗口是那个 grace 的上界 ——
# 想给它更长时间就同时调大 AUTONOMY_STOP_TIMEOUT（本脚本，默认 15s）与
# AUTONOMY_SHUTDOWN_GRACE（服务端）。等不到才 KILL，那一刀想写什么都写不进去。
#
# 找进程不只看 pid 文件。部署平台 rsync --delete 只保留 backend/data/、backend/server.log
# 等少数路径，backend/runtime.pid 会被清掉；旧 autonomyd 还占着 SERVICE_PORT。
# 只认 pid 文件时 stop 会「视为未运行」并成功退出，随后 start 因端口占用把整次部署打成失败
# （pipeline-ba9a2e00 / pipeline-4d3462fa：远程 4300 被同一 pid 连续占了两天）。
# 没有有效 pid 文件时，再看本 runtime 的 bin/autonomyd 是否听在契约端口上；是就停它。
# 别人的进程占着端口：不动，留给 start.sh 报错。
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNTIME_DIR="${RUNTIME_DIR:-$(cd "${DIR}/.." && pwd)}"
BIN="${RUNTIME_DIR}/bin/autonomyd"
PID_FILE="${RUNTIME_DIR}/backend/runtime.pid"
PORT="${SERVICE_PORT:-${PORT:-4300}}"
# 等待窗口（秒）：默认 15s（≥ AUTONOMY_SHUTDOWN_GRACE 默认的 10s）。
WAIT_SECONDS="${AUTONOMY_STOP_TIMEOUT:-15}"

log() { echo "[stop] $*"; }

is_our_runtime_process() {
  local pid="$1" cmd="" exe=""
  [ -n "${pid}" ] || return 1
  cmd="$(ps -o command= -p "${pid}" 2>/dev/null || true)"
  case "${cmd}" in
  *"${BIN}"*) return 0 ;;
  esac
  if [ -r "/proc/${pid}/exe" ]; then
    exe="$(readlink "/proc/${pid}/exe" 2>/dev/null || true)"
    case "${exe}" in
    "${BIN}"|"${BIN} (deleted)") return 0 ;;
    esac
  fi
  return 1
}

# 还在跑（不是空 PID、也不是已经收尸的僵尸）。父进程还没 wait 时，
# kill -0 对僵尸仍成功，不能单靠它判断「没停掉」。
is_running() {
  local pid="$1" st=""
  [ -n "${pid}" ] || return 1
  kill -0 "${pid}" 2>/dev/null || return 1
  st="$(ps -o state= -p "${pid}" 2>/dev/null || true)"
  case "${st}" in
  Z*) return 1 ;;
  esac
  return 0
}

# 契约端口上听着的、且是本 runtime 二进制的 pid（可能有多个，逐个停）。
our_listeners() {
  command -v lsof >/dev/null 2>&1 || return 0
  case "${PORT}" in
  *[!0-9]*|"") return 0 ;;
  esac
  local p
  for p in $(lsof -nP -iTCP:"${PORT}" -sTCP:LISTEN -t 2>/dev/null || true); do
    if is_our_runtime_process "${p}"; then
      printf '%s\n' "${p}"
    fi
  done
}

stop_pid() {
  local pid="$1"
  if ! is_running "${pid}"; then
    return 0
  fi
  log "TERM → pid=${pid}（优雅停止，最多等 ${WAIT_SECONDS}s）"
  kill "${pid}" 2>/dev/null || true
  local attempts="$(( WAIT_SECONDS * 2 ))"
  local _
  for _ in $(seq 1 "${attempts}"); do
    if ! is_running "${pid}"; then
      log "已停止 pid=${pid}"
      return 0
    fi
    sleep 0.5
  done
  log "未在 ${WAIT_SECONDS}s 内退出，KILL → pid=${pid}"
  kill -9 "${pid}" 2>/dev/null || true
  # KILL 之后不再因僵尸 / 竞态把 stop 判失败：部署重启依赖本脚本返回 0。
  log "已强制停止 pid=${pid}"
  return 0
}

stopped=0

if [ -f "${PID_FILE}" ]; then
  pid="$(tr -d '[:space:]' < "${PID_FILE}" || true)"
  if is_running "${pid}"; then
    stop_pid "${pid}"
    stopped=1
  else
    log "进程 ${pid:-?} 不存在，清理 pid 文件"
  fi
  rm -f "${PID_FILE}"
else
  log "没有 pid 文件"
fi

# pid 文件被 rsync 清掉、或里面写的不是真正听端口的那一个时，按端口再收一次自己的进程。
leftover=0
while read -r holder; do
  [ -n "${holder}" ] || continue
  leftover=1
  log "端口 ${PORT} 上是本 runtime 的残留进程 pid=${holder}，继续停"
  stop_pid "${holder}"
  stopped=1
done <<EOF
$(our_listeners)
EOF

rm -f "${PID_FILE}"

if [ "${stopped}" -eq 0 ] && [ "${leftover}" -eq 0 ]; then
  log "视为未运行"
fi
