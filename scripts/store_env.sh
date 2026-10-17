#!/usr/bin/env bash
#
# store_env.sh — start.sh 在载入 backend/.env 之后 source 这一份。
#
# 部署起来的进程读写同一份库：写侧连接串（AUTONOMY_STORE_DSN，否则
# AUTONOMY_POSTGRES_DSN / DATABASE_URL）。backend/.env 跨部署保留，里面可能还
# 留着本地副本的 AUTONOMY_STORE_READ_DSN（127.0.0.1:5433）。那条读侧不再生效：
# 观察类的读和写都走写侧。engine 自己仍认识这两个变量，给不经 start.sh 的实验
# 和读写分离单测用；线上路径是这里。

# store_writer_target 打印写侧的 host:port（或 host= 那种 DSN 的主机），不打印口令。
store_writer_target() {
  local dsn="${AUTONOMY_STORE_DSN:-${AUTONOMY_POSTGRES_DSN:-${DATABASE_URL:-}}}"
  if [ -z "${dsn}" ]; then
    printf '%s' "未配置"
    return
  fi
  case "${dsn}" in
    *://*@*)
      local rest="${dsn#*@}"
      rest="${rest%%/*}"
      rest="${rest%%\?*}"
      printf '%s' "${rest}"
      ;;
    *host=*)
      local host port
      host="$(printf '%s' "${dsn}" | sed -n 's/.*host=\([^ ]*\).*/\1/p')"
      port="$(printf '%s' "${dsn}" | sed -n 's/.*port=\([^ ]*\).*/\1/p')"
      if [ -n "${host}" ] && [ -n "${port}" ]; then
        printf '%s' "${host}:${port}"
      elif [ -n "${host}" ]; then
        printf '%s' "${host}"
      else
        printf '%s' "写侧"
      fi
      ;;
    *)
      printf '%s' "写侧"
      ;;
  esac
}

# unify_store_reads_onto_writer 丢掉读侧副本，让随后拉起的进程读和写都用写侧。
# 在 source backend/.env 之后、exec 之前调用。
unify_store_reads_onto_writer() {
  if [ -n "${AUTONOMY_STORE_READ_DSN:-}" ] || [ -n "${AUTONOMY_POSTGRES_READ_DSN:-}" ]; then
    echo "[start] 读写同一份库：忽略读侧副本，读和写都走 $(store_writer_target)"
  fi
  unset AUTONOMY_STORE_READ_DSN AUTONOMY_POSTGRES_READ_DSN
}
