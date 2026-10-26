#!/usr/bin/env bash
#
# store_env.sh — start.sh 在载入 backend/.env 之后 source 这一份。
#
# 部署起来的进程读写同一份库：写侧连接串优先用部署平台注入的 DATABASE_URL，否则
# AUTONOMY_STORE_DSN / AUTONOMY_POSTGRES_DSN。backend/.env 跨部署保留，里面可能还
# 留着旧的 AUTONOMY_STORE_DSN，以及本地副本的 AUTONOMY_STORE_READ_DSN
# （127.0.0.1:5433）。DATABASE_URL 有值就盖过旧连接串；读侧不再生效：观察类的读
# 和写都走写侧。engine 自己仍认识读侧变量，给不经 start.sh 的实验和读写分离单测用；
# 线上路径是这里。

# store_writer_target 打印写侧的 host:port（或 host= 那种 DSN 的主机），不打印口令。
store_writer_target() {
  local dsn="${DATABASE_URL:-${AUTONOMY_STORE_DSN:-${AUTONOMY_POSTGRES_DSN:-}}}"
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
      case "${dsn}" in
        *password=*|*://*:*@*) printf '%s' "写侧" ;;
        *) printf '%s' "${dsn}" ;;
      esac
      ;;
  esac
}

# apply_database_url 让部署平台注入的 DATABASE_URL 成为这份进程的库连接。
# backend/.env 跨部署保留，里面的 AUTONOMY_STORE_DSN 会把库钉在旧地址上；
# 和 SERVICE_PORT 盖过 .env 里的旧端口一样，DATABASE_URL 非空就用它。
# postgres URL（postgres://、postgresql://）或 host=/dbname= 关键字串同时把
# 引擎选成 postgres，避免还按 sqlite 去打开一条网络连接串。
# rewrite_database_url_cert 把 sslrootcert 换成这台机器上的 CA。
# 平台只存一份 DATABASE_URL（本机路径）。文件不在这儿、但
# ~/database/autonomy/pg-autonomy-ca.pem 在，就换成本机这份，两边都是同一张 CA。
rewrite_database_url_cert() {
  local url="$1" cert home_cert
  case "${url}" in
    *sslrootcert=*) ;;
    *) printf '%s' "${url}"; return ;;
  esac
  cert="$(printf '%s' "${url}" | sed -n 's/.*[?&]sslrootcert=\([^&]*\).*/\1/p')"
  cert="$(printf '%s' "${cert}" | sed 's/%2[Ff]/\//g')"
  if [ -n "${cert}" ] && [ -f "${cert}" ]; then
    printf '%s' "${url}"
    return
  fi
  home_cert="${HOME}/database/autonomy/pg-autonomy-ca.pem"
  if [ ! -f "${home_cert}" ]; then
    printf '%s' "${url}"
    return
  fi
  printf '%s' "${url}" | sed "s#sslrootcert=[^&]*#sslrootcert=${home_cert}#"
}

# apply_pgsslmode 用部署平台的 PGSSLMODE 盖过连接串里的 sslmode。
# 本机 Postgres 常常没开 SSL，URL 里留着 verify-ca 会连不上；单独设
# PGSSLMODE=disable 即可，不用改整条 DATABASE_URL。
apply_pgsslmode() {
  local url mode
  url="$1"
  mode="$(printf '%s' "${PGSSLMODE:-}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')"
  case "${mode}" in
    disable|allow|prefer|require|verify-ca|verify-full) ;;
    *) printf '%s' "${url}"; return ;;
  esac
  case "${url}" in
    *://*)
      case "${url}" in
        *\?*)
          case "${url}" in
            *sslmode=*)
              printf '%s' "${url}" | sed "s/sslmode=[^&]*/sslmode=${mode}/"
              ;;
            *) printf '%s' "${url}&sslmode=${mode}" ;;
          esac
          ;;
        *) printf '%s' "${url}?sslmode=${mode}" ;;
      esac
      ;;
    *sslmode=*)
      printf '%s' "${url}" | sed "s/sslmode=[^ ]*/sslmode=${mode}/"
      ;;
    *) printf '%s' "${url} sslmode=${mode}" ;;
  esac
}

apply_database_url() {
  local url scheme
  url="$(printf '%s' "${DATABASE_URL:-}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  if [ -z "${url}" ]; then
    return 0
  fi
  url="$(rewrite_database_url_cert "${url}")"
  url="$(apply_pgsslmode "${url}")"
  export DATABASE_URL="${url}"
  export AUTONOMY_STORE_DSN="${url}"
  scheme="$(printf '%s' "${url}" | cut -d: -f1 | tr '[:upper:]' '[:lower:]')"
  case "${scheme}" in
    postgres|postgresql)
      export AUTONOMY_STORE_ENGINE=postgres
      ;;
    *)
      case "${url}" in
        *://*) ;;
        *host=*|*dbname=*) export AUTONOMY_STORE_ENGINE=postgres ;;
      esac
      ;;
  esac
  echo "[start] 数据库连接来自 DATABASE_URL：$(store_writer_target)"
}

# unify_store_reads_onto_writer 丢掉读侧副本，让随后拉起的进程读和写都用写侧。
# 在 source backend/.env 之后、exec 之前调用。
unify_store_reads_onto_writer() {
  if [ -n "${AUTONOMY_STORE_READ_DSN:-}" ] || [ -n "${AUTONOMY_POSTGRES_READ_DSN:-}" ]; then
    echo "[start] 读写同一份库：忽略读侧副本，读和写都走 $(store_writer_target)"
  fi
  unset AUTONOMY_STORE_READ_DSN AUTONOMY_POSTGRES_READ_DSN
}
