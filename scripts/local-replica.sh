#!/usr/bin/env bash
#
# local-replica.sh —— 在本地立一个 PostgreSQL 副本（streaming replica），作为远端主库的
# standby，让 runtime 「写远端、读本地」（见 docs/local-replica.md、docs/store.md「读写分离」）。
#
# 副本是**一整份数据库**，不是一份导出：pg_basebackup 把主库的数据目录搬过来，之后一直
# 跟着主库的 WAL 走（hot standby，只读、可查询）。所以 localhost 上的这份库和远端那份库
# 是同一个库 —— 角色、口令、schema、数据都在里面，runtime 只要换一个连接串就能读它。
#
# 用法：
#   scripts/local-replica.sh init      从主库做一次基础备份，把副本建起来并启动
#   scripts/local-replica.sh configure 只把本机设置写进现有的副本目录（init 会自动调用）
#   scripts/local-replica.sh start     启动副本（127.0.0.1:$REPLICA_PORT，只读）
#   scripts/local-replica.sh stop      停止副本
#   scripts/local-replica.sh status    副本状态：在不在恢复、落后多少、主库看到什么
#   scripts/local-replica.sh env       打印「写远端、读本地」的两个 DSN（供 backend/.env 用）
#   scripts/local-replica.sh install-agent    装 launchd 代理：登录后自动把副本拉起来
#   scripts/local-replica.sh uninstall-agent  卸掉代理
#
# 配置：优先读一个本地配置文件（默认 ~/database/autonomy/replica.env，权限 600，不入 git），
# 环境变量其次。变量见下；init 至少需要 PRIMARY_HOST 与副本角色的口令（~/.pgpass 或
# PRIMARY_PASSWORD）。
#
# 主库侧要准备的东西（一次性，需要主库的管理员；脚本不替你做，因为那是在改**远端**：
# 建角色、开复制、放 WAL 留存）：
#   CREATE ROLE autonomy_replica WITH LOGIN REPLICATION PASSWORD '...';
#   SELECT pg_create_physical_replication_slot('laptop_replica');
#   ALTER SYSTEM SET max_slot_wal_keep_size = '2GB';   -- 槽留在主库上的 WAL 有上限
#   -- 并在 pg_hba.conf 里放行本机出口 IP：
#   hostssl replication autonomy_replica <本机出口 IP>/32 scram-sha-256
#   然后 SELECT pg_reload_conf();
# status 会告诉你主库那边看不看得见这个副本。
set -euo pipefail

REPLICA_ENV_FILE="${REPLICA_ENV_FILE:-${HOME}/database/autonomy/replica.env}"
if [ -f "${REPLICA_ENV_FILE}" ]; then
  # shellcheck disable=SC1090
  set -a; . "${REPLICA_ENV_FILE}"; set +a
fi

# 主库：写走它。
PRIMARY_HOST="${PRIMARY_HOST:-}"
PRIMARY_PORT="${PRIMARY_PORT:-5432}"
PRIMARY_USER="${PRIMARY_USER:-autonomy_replica}"
PRIMARY_SSLROOTCERT="${PRIMARY_SSLROOTCERT:-${HOME}/database/autonomy/pg-autonomy-ca.pem}"
PRIMARY_SSLMODE="${PRIMARY_SSLMODE:-verify-ca}"
REPLICA_SLOT="${REPLICA_SLOT:-laptop_replica}"

# 本地副本：读走它。端口默认 5433，因为 5432 常常是本来就有的那个本地 postgres。
REPLICA_DIR="${REPLICA_DIR:-${HOME}/database/autonomy/replica}"
REPLICA_PORT="${REPLICA_PORT:-5433}"
REPLICA_SOCKET_DIR="${REPLICA_SOCKET_DIR:-/tmp}"
REPLICA_HOST="${REPLICA_HOST:-127.0.0.1}"
REPLICA_AGENT_LABEL="${REPLICA_AGENT_LABEL:-com.autonomy.local-replica}"
REPLICA_AGENT_PLIST="${HOME}/Library/LaunchAgents/${REPLICA_AGENT_LABEL}.plist"

# runtime 侧的连接串（env 子命令打印用）。
APP_DB="${APP_DB:-autonomy}"
APP_USER="${APP_USER:-autonomy}"

log() { echo "[replica] $*"; }
die() { echo "[replica][错误] $*" >&2; exit 1; }

# 用哪个 PostgreSQL：副本必须和主库同一个大版本（物理复制搬的是同一种 WAL 和页格式），
# 所以这里优先找 postgresql@16，找不到再退回 PATH 上的 pg_config。
detect_pgbin() {
  if [ -n "${PGBIN:-}" ] && [ -x "${PGBIN}/pg_ctl" ]; then
    echo "${PGBIN}"; return
  fi
  for dir in /usr/local/opt/postgresql@16/bin /opt/homebrew/opt/postgresql@16/bin; do
    if [ -x "${dir}/pg_ctl" ]; then echo "${dir}"; return; fi
  done
  if command -v pg_config >/dev/null 2>&1; then
    echo "$(pg_config --bindir)"; return
  fi
  echo ""
}

PGBIN="$(detect_pgbin)"
[ -n "${PGBIN}" ] || die "找不到 PostgreSQL 16 的可执行文件（brew install postgresql@16），或显式给出 PGBIN=..."

primary_conninfo() {
  local parts="host=${PRIMARY_HOST} port=${PRIMARY_PORT} user=${PRIMARY_USER} dbname=replication"
  parts="${parts} sslmode=${PRIMARY_SSLMODE} application_name=${REPLICA_SLOT}"
  if [ "${PRIMARY_SSLMODE}" != "disable" ] && [ -f "${PRIMARY_SSLROOTCERT}" ]; then
    parts="${parts} sslrootcert=${PRIMARY_SSLROOTCERT}"
  fi
  echo "${parts}"
}

require_primary() {
  [ -n "${PRIMARY_HOST}" ] || die "没有配置主库：设 PRIMARY_HOST（或在 ${REPLICA_ENV_FILE} 里写）"
}

# --- 副本自己的启动参数 ------------------------------------------------------------
# 主库的 postgresql.conf 是被整份搬过来的（Ubuntu 那份），端口和 socket 路径都不适合这台
# 机器，所以副本目录里再补一段**本机**设置。auto.conf 里的 primary_conninfo 是
# pg_basebackup -R 写的，不在这里动。
write_replica_conf() {
  local conf="${REPLICA_DIR}/postgresql.conf" local_conf="${REPLICA_DIR}/replica.local.conf"
  cat >"${local_conf}" <<EOF
# 这台机器上的副本（scripts/local-replica.sh 写，重复 configure 会覆盖）：只监听本机，
# 端口 ${REPLICA_PORT}（5432 常常是本机那个本来就有的 postgres）。
port = ${REPLICA_PORT}
listen_addresses = '${REPLICA_HOST}'
unix_socket_directories = '${REPLICA_SOCKET_DIR}'
# 恢复期间也接受只读查询：读本地就是这个意思（读到的可能比主库晚一点点）。
hot_standby = on
# 主库上那个复制槽的名字：WAL 从它那里取，主库的 pg_stat_replication 也按它报。
primary_slot_name = '${REPLICA_SLOT}'
EOF
  # Ubuntu 的集群把 postgresql.conf / pg_hba.conf 放在 /etc/postgresql/<ver>/main，**不在**
  # 数据目录里 —— 所以基础备份不会把它们带过来（它只搬数据目录）。副本自己写一份最小的：
  # 不写就没有 hba，postgres 会拒绝所有连接，副本等于白搭。
  if [ ! -f "${conf}" ]; then
    echo "# 本地副本的 postgresql.conf（主库那份在它的 /etc 下，没被搬过来，见 docs/local-replica.md）。
# 本机设置都在 replica.local.conf 里，从下面这行 include 进来。
include_if_exists = 'replica.local.conf'" >"${conf}"
  elif ! grep -q "include_if_exists = 'replica.local.conf'" "${conf}"; then
    # 主库的 postgresql.conf 被搬过来了（比如主库把 conf 放在数据目录里）：本机那几条放
    # 末尾 include，为的是让它们压过前面那份。
    printf "\n# 本机副本设置（scripts/local-replica.sh）\ninclude_if_exists = 'replica.local.conf'\n" >>"${conf}"
  fi
  if [ ! -f "${REPLICA_DIR}/pg_hba.conf" ]; then
    cat >"${REPLICA_DIR}/pg_hba.conf" <<EOF
# 副本自己的 pg_hba.conf（同样不在主库的数据目录里，没被搬过来）。副本只监听本机、
# 而且只能读，所以本机 socket 与环回 TCP 放行；角色和口令是随备份一起搬过来的，
# 服务用哪个角色、哪套口令，本地这份副本上一样能用。
local   all             all                                     trust
local   replication     all                                     trust
host    all             all             127.0.0.1/32            scram-sha-256
host    all             all             ::1/128                 scram-sha-256
EOF
  fi
  # 空的 pg_ident.conf：不写的话 postgres 每次启动都会抱怨一句「打不开」，而副本也不用它。
  [ -f "${REPLICA_DIR}/pg_ident.conf" ] || : >"${REPLICA_DIR}/pg_ident.conf"
}

# 启动/停止优先走 launchd 代理（装了的话），否则用 pg_ctl —— 两条路都只是同一件事。
agent_installed() { [ -f "${REPLICA_AGENT_PLIST}" ]; }

replica_running() {
  [ -f "${REPLICA_DIR}/postmaster.pid" ] && kill -0 "$(head -1 "${REPLICA_DIR}/postmaster.pid" 2>/dev/null)" 2>/dev/null
}

start_replica() {
  if replica_running; then
    log "副本已在运行（pid=$(head -1 "${REPLICA_DIR}/postmaster.pid")）"
    return 0
  fi
  if agent_installed; then
    launchctl kickstart "gui/${UID}/${REPLICA_AGENT_LABEL}" >/dev/null 2>&1 || true
  fi
  if ! replica_running; then
    "${PGBIN}/pg_ctl" -D "${REPLICA_DIR}" -l "${REPLICA_DIR}/replica.log" -w -t 30 start
  fi
  for _ in $(seq 1 30); do
    if "${PGBIN}/pg_isready" -h "${REPLICA_HOST}" -p "${REPLICA_PORT}" >/dev/null 2>&1; then
      log "副本已启动：${REPLICA_HOST}:${REPLICA_PORT}（数据目录 ${REPLICA_DIR}）"
      return 0
    fi
    sleep 1
  done
  die "30s 内副本没有起来，看 ${REPLICA_DIR}/replica.log"
}

stop_replica() {
  if ! replica_running; then
    log "副本没有在运行"
    return 0
  fi
  if agent_installed; then
    launchctl bootout "gui/${UID}/${REPLICA_AGENT_LABEL}" >/dev/null 2>&1 || true
  fi
  if replica_running; then
    "${PGBIN}/pg_ctl" -D "${REPLICA_DIR}" -m fast -w -t 30 stop
  fi
  log "副本已停止"
}

# --- init：把这个目录变成主库的一份副本 ----------------------------------------------
cmd_init() {
  require_primary
  local conn
  conn="$(primary_conninfo)"
  # 版本对齐先做，别等搬到一半才失败：物理复制搬的是同一种 WAL 与页格式，大版本必须一致。
  local primary_version replica_version
  primary_version="$(psql -w "$(psql_parts "dbname=postgres")" -tAc 'show server_version' 2>/dev/null | cut -d. -f1 || true)"
  replica_version="$("${PGBIN}/postgres" --version | awk '{print $3}' | cut -d. -f1)"
  if [ -n "${primary_version}" ] && [ "${primary_version}" != "${replica_version}" ]; then
    die "主库是 PostgreSQL ${primary_version}，本机的 ${PGBIN} 是 ${replica_version}：物理复制要求主副同大版本（brew install postgresql@${primary_version}）"
  fi

  if replica_running; then
    log "先停掉正在运行的副本"
    stop_replica
  fi
  if [ -e "${REPLICA_DIR}" ]; then
    log "删掉旧的副本目录（重新做一次基础备份）：${REPLICA_DIR}"
    rm -rf "${REPLICA_DIR}"
  fi
  mkdir -p "$(dirname "${REPLICA_DIR}")"

  log "从 ${PRIMARY_HOST}:${PRIMARY_PORT} 做基础备份（槽 ${REPLICA_SLOT}）→ ${REPLICA_DIR}"
  "${PGBIN}/pg_basebackup" -d "${conn}" -D "${REPLICA_DIR}" -Xs -R -S "${REPLICA_SLOT}" -P -v

  write_replica_conf
  # 备份是主库的角色一起搬过来的，其中就有服务用的那个角色，所以本地这份副本上
  # 「写远端、读本地」用的是同一个角色、同一套口令。
  start_replica
  cmd_status
}
# psql 连主库用的连接参数（口令走 ~/.pgpass，不进命令行）。
psql_parts() {
  echo "host=${PRIMARY_HOST} port=${PRIMARY_PORT} user=${PRIMARY_USER} $1 sslmode=${PRIMARY_SSLMODE} sslrootcert=${PRIMARY_SSLROOTCERT} connect_timeout=8"
}

# --- env：runtime 要的两行配置 --------------------------------------------------------
# 口令不进命令行、也不进 git：从 ~/.pgpass 里读（init 时已经写好复制那条；服务角色那条
# 如果本地没有，就用占位符提醒去哪儿拿）。
app_password() {
  [ -f "${HOME}/.pgpass" ] || return 0
  awk -F: -v h="${PRIMARY_HOST}" -v p="${PRIMARY_PORT}" -v d="${APP_DB}" -v u="${APP_USER}" \
    '($1==h || $1=="*") && ($2==p || $2=="*") && ($3==d || $3=="*") && ($4==u || $4=="*") {print $5; exit}' \
    "${HOME}/.pgpass" 2>/dev/null || true
}

cmd_env() {
  local pw
  pw="$(app_password)"
  [ -n "${pw}" ] || pw="<${APP_USER} 的口令：见 ~/.pgpass / 部署用的那个 .env>"
  cat <<EOF
# 「写远端、读本地」：主库是写侧，本地副本是读侧（docs/local-replica.md）。
# 把这三行放进 runtime 的 backend/.env，再走部署平台重启（不要在本机直接重启部署）。
AUTONOMY_STORE_ENGINE=postgres
AUTONOMY_STORE_DSN="postgres://${APP_USER}:${pw}@${PRIMARY_HOST}:${PRIMARY_PORT}/${APP_DB}?sslmode=${PRIMARY_SSLMODE}&sslrootcert=${PRIMARY_SSLROOTCERT}&application_name=autonomy&connect_timeout=5"
AUTONOMY_STORE_READ_DSN="postgres://${APP_USER}:${pw}@${REPLICA_HOST}:${REPLICA_PORT}/${APP_DB}?sslmode=disable&application_name=autonomy-local-read&connect_timeout=5"
# 读侧连不上时不会失败：只warning并退回主库（docs/store.md「读写分离」）。
EOF
}

# --- launchd：开机/登录后把副本拉起来 -------------------------------------------------
agent_plist() {
  cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${REPLICA_AGENT_LABEL}</string>
  <key>ProgramArguments</key>
  <array>
    <string>${PGBIN}/postgres</string>
    <string>-D</string>
    <string>${REPLICA_DIR}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>${REPLICA_DIR}/replica.log</string>
  <key>StandardErrorPath</key><string>${REPLICA_DIR}/replica.log</string>
</dict>
</plist>
EOF
}

cmd_install_agent() {
  [ -d "${REPLICA_DIR}" ] || die "副本还没建起来：先跑 init"
  mkdir -p "$(dirname "${REPLICA_AGENT_PLIST}")"
  agent_plist >"${REPLICA_AGENT_PLIST}"
  log "已写入 ${REPLICA_AGENT_PLIST}"
  replica_running && stop_replica
  launchctl bootstrap "gui/${UID}" "${REPLICA_AGENT_PLIST}" 2>/dev/null || launchctl load -w "${REPLICA_AGENT_PLIST}"
  start_replica
  log "代理已加载：登录后会自动把副本拉起来（卸载：$0 uninstall-agent）"
}

cmd_uninstall_agent() {
  if agent_installed; then
    launchctl bootout "gui/${UID}" "${REPLICA_AGENT_PLIST}" 2>/dev/null || launchctl unload -w "${REPLICA_AGENT_PLIST}" || true
    rm -f "${REPLICA_AGENT_PLIST}"
    log "代理已卸载（副本如果要停：$0 stop）"
  else
    log "没有装代理"
  fi
}

usage() {
  sed -n '3,25p' "$0" | sed 's/^# \{0,1\}//'
}

# --- status：副本在不在、落后多少、主库那边看到什么 ----------------------------------
cmd_status() {
  if ! replica_running; then
    echo "副本：未运行（数据目录 ${REPLICA_DIR}）"
  else
    local pid
    pid="$(head -1 "${REPLICA_DIR}/postmaster.pid")"
    echo "副本：运行中 pid=${pid} ${REPLICA_HOST}:${REPLICA_PORT} 数据目录=${REPLICA_DIR}"
    # 走本机 socket：副本的 pg_hba 里 local 是 trust（副本只读、只监听本机），所以这里
    # 不需要口令 —— 状态查询不该被口令挡住。
    psql -h "${REPLICA_SOCKET_DIR}" -p "${REPLICA_PORT}" -U "${APP_USER}" -d "${APP_DB}" -Atc "
      SELECT '副本角色：' || CASE WHEN pg_is_in_recovery() THEN '在恢复中（只读副本）' ELSE '居然不是副本（可写）—— 这个连接串不能当读侧用' END
           || E'\n回放位点：' || pg_last_wal_replay_lsn()
           || E'\n最后回放的事务：' || COALESCE(pg_last_xact_replay_timestamp()::text, '（还没回放过）')
           || COALESCE('（距今 ' || (now() - pg_last_xact_replay_timestamp())::text || '）', '')" 2>&1 | sed 's/^/  /'
  fi
  if [ -n "${PRIMARY_HOST}" ]; then
    echo "主库：${PRIMARY_HOST}:${PRIMARY_PORT}"
    # -w：口令只从 ~/.pgpass 来，脚本绝不在这里问人（没配好就报错，见文档里那几行）。
    # 复制角色的权限不够看 pg_stat_replication 的那些列（state / replay_lag 对非
    # pg_read_all_stats 是空的），所以这里只报「槽上有没有人连着」—— 真正的滞后看上面
    # 「最后回放的事务」，那是副本自己说的。
    psql -w "$(psql_parts dbname=postgres)" -Atc "
      SELECT '  槽 ' || slot_name || '：'
           || CASE WHEN active_pid IS NULL THEN '没人连着'
                   ELSE COALESCE(state, '连着（状态列要超级用户或 pg_read_all_stats 才看得见）') END
           || COALESCE('，落后 ' || replay_lag::text, '')
      FROM pg_replication_slots s LEFT JOIN pg_stat_replication r ON r.pid = s.active_pid
      WHERE slot_name = '${REPLICA_SLOT}'" 2>&1 | sed 's/^/  /' || true
  fi
}

case "${1:-}" in
  init) cmd_init ;;
  configure) require_primary; write_replica_conf; log "本机副本设置已写入 ${REPLICA_DIR}" ;;
  start) require_primary; start_replica ;;
  stop) stop_replica ;;
  restart) stop_replica; require_primary; start_replica ;;
  status) cmd_status ;;
  env) cmd_env ;;
  install-agent) cmd_install_agent ;;
  uninstall-agent) cmd_uninstall_agent ;;
  ""|-h|--help|help) usage ;;
  *) die "不认识的命令：$1（init / start / stop / restart / status / env / install-agent / uninstall-agent）" ;;
esac
