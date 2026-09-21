# 本地副本：写远端、读本地

## 一句话

远端那台 PostgreSQL 是**唯一的写方**，也是数据本身；本机再立一份它的 streaming
replica（hot standby，只读），让「看得多、改得少」的读落在本地。写走远端，读走本地。

```
       写，以及「读完就写」的读
  runtime ───────────────────────►  远端主库  49.234.45.173:5432  (autonomy)
     │                                  │
     │                                  │  WAL 流（复制槽 laptop_replica）
     │                                  ▼
     └──── 只观察的读 ──────────►  本地副本  127.0.0.1:5433     (只读，热备)
```

- **写**：`AUTONOMY_STORE_DSN` 指远端主库 —— 一次写只有一处落点，没有两份真相。
- **读**：`AUTONOMY_STORE_READ_DSN` 指本地副本 —— HTTP 读接口、数据 API、UI / 评测的轮询
  都在本机完成，不再每次跨网。
- **副本是同一份库**：物理复制搬的是主库的数据目录 + 之后的 WAL，所以角色、口令、schema、
  数据全在里面 —— 服务用哪个角色连主库，就用同一个角色连本地。

分工与一致性由代码保证，不只是靠约定：见 [store.md](store.md)「读写分离」。本文讲怎么把
这份副本立起来、怎么验证它、坏了怎么办。

## 远端要准备什么（一次性，需要主库的管理员）

脚本不替你做这几步 —— 那是在改**远端**：开复制权限、放行本机出口 IP、决定槽能留住多少 WAL。

```sql
-- 一个只用来复制的角色：不是服务角色，不碰任何表。
CREATE ROLE autonomy_replica WITH LOGIN REPLICATION PASSWORD '<生成一个>';
-- 本地副本从这个槽取 WAL；某个副本掉线时，槽会让主库把 WAL 留着。
SELECT pg_create_physical_replication_slot('laptop_replica');
-- 但槽留 WAL 是有代价的：本机要是好几周不上线，主库的磁盘不能因为一个槽被填满。
ALTER SYSTEM SET max_slot_wal_keep_size = '2GB';
SELECT pg_reload_conf();
```

`pg_hba.conf` 里放行本机出口 IP（**hostssl**：复制连接也走 TLS）:

```
hostssl replication     autonomy_replica     <本机出口 IP>/32     scram-sha-256
```

然后 `SELECT pg_reload_conf();`。主库看到的客户端地址就是这个出口 IP —— 家用宽带换 IP 之后，
这一行要跟着改（`scripts/local-replica.sh status` 会告诉你槽上还有没有人连着）。

本机 `~/.pgpass`（权限 600）里放三行，之后所有连接都不用交互式输口令：

```
<主库 IP>:5432:*:autonomy_replica:<复制角色口令>
<主库 IP>:5432:*:autonomy:<服务角色口令>
<主库 IP>:5432:*:postgres:<超级用户口令>          # 可选：管理用
```

## 本地立副本

```bash
brew install postgresql@16          # 主副必须同一个大版本（物理复制搬的是同一种 WAL/页）
export PRIMARY_HOST=49.234.45.173   # 也可以写进 ~/database/autonomy/replica.env（权限 600）
scripts/local-replica.sh init       # 基础备份 → 写本机配置 → 启动
```

`init` 做四件事，每件都有理由：

1. **对齐大版本**：主库 16.x，本机就装 `postgresql@16`；版本不同直接报错，不等到搬到一半
   才失败（WAL 与页格式是同一个大版本内的契约）。
2. `pg_basebackup ... -Xs -R -S laptop_replica`：把数据目录整份搬过来（本例 ~43MB），
   同时流式拉 WAL，并写下 `standby.signal` 与 `postgresql.auto.conf` 里的 `primary_conninfo`
   （含 `sslmode=verify-ca` 与 CA 证书；口令走 `~/.pgpass`，**不进**这个文件）。
3. **补一份本机配置**：Ubuntu 的集群把 `postgresql.conf` / `pg_hba.conf` 放在
   `/etc/postgresql/16/main/`，**不在数据目录里**，所以基础备份不会带过来。副本自己写最小的一份：
   端口 5433（5432 常被本机那个本来就有的 postgres 占着）、只监听 `127.0.0.1`、
   socket 在 `/tmp`、`hot_standby = on`、`primary_slot_name`，以及一份只放行本机的 `pg_hba.conf`。
   —— 不写 hba 的话 postgres 会拒绝所有连接，副本等于白搭。
4. 启动并打印状态。

副本目录默认 `~/database/autonomy/replica`，和本机那个 5432 的 postgres 互不影响。

### 让它活过重启

```bash
scripts/local-replica.sh install-agent      # 装一个 launchd 代理，登录后自动拉起副本
scripts/local-replica.sh uninstall-agent
```

装过代理之后 `start` / `stop` 也走 `launchctl`，两条路（`pg_ctl` 或 launchd）都能管它。

## 打开「读本地」

```bash
scripts/local-replica.sh env        # 打印三行（写侧 / 读侧 DSN），口令从 ~/.pgpass 取
```

把这三行放进 runtime 的 `backend/.env`，然后**走部署平台**重启（不要在本机直接重启部署）：

```
AUTONOMY_STORE_ENGINE=postgres
AUTONOMY_STORE_DSN="postgres://autonomy:<口令>@49.234.45.173:5432/autonomy?sslmode=verify-ca&sslrootcert=…"
AUTONOMY_STORE_READ_DSN="postgres://autonomy:<口令>@127.0.0.1:5433/autonomy?sslmode=disable&application_name=autonomy-local-read"
```

- **读侧不给**：行为与过去完全一样，所有读都在主库 —— 这个开关是二元的、可随时退回来。
- **读侧连不上 / 不是副本**：不会启动失败，只是打一条 warning 并把读退回主库
  （副本是优化，数据库才是本体）。打开时会检查它确实在恢复中、且和主库同一个
  `system_identifier`：指错库这种错会安静地给错数据，所以宁可退回主库。

## 验证

```bash
scripts/local-replica.sh status
#   副本：运行中 pid=… 127.0.0.1:5433 数据目录=…
#   副本角色：在恢复中（只读副本）
#   回放位点：0/D421C48   最后回放的事务：2026-09-21 10:07:04+00（距今 …）
#   主库：49.234.45.173:5432
#     槽 laptop_replica：连着
```

只看两件事就够了：**它在恢复中**（`pg_is_in_recovery()` 为真），且**位点在追**
（副本的回放位点追平主库的 `pg_current_wal_lsn()`）。写侧是否真的在写、副本是否真的只读，
用 SQL 直接看：

```sql
-- 主库：它是不是在流式复制、落后多少
SELECT application_name, state, replay_lag FROM pg_stat_replication;
-- 副本：只读，写不进去（这是它不能当写侧用的证据）
INSERT INTO tasks (id, description, status) VALUES ('x', 'x', 'pending');
--   ERROR:  cannot execute INSERT in a read-only transaction
```

端到端（一条真实链路：写到远端 → 在本地读到）在引擎的测试里，三个变量都要给：

```bash
AUTONOMY_POSTGRES_TEST_DSN='host=49.234.45.173 user=postgres dbname=postgres sslmode=require' \
AUTONOMY_POSTGRES_SPLIT_WRITE_DSN='postgres://autonomy:…@49.234.45.173:5432/autonomy?sslmode=verify-ca&sslrootcert=…' \
AUTONOMY_POSTGRES_SPLIT_READ_DSN='postgres://autonomy:…@127.0.0.1:5433/autonomy?sslmode=disable' \
go test ./src/db/ -run TestTheSplitAgainstARealReplica -v
```

它会在主库上建一个**归服务角色所有**的临时库（跑完删掉），等它复制到本地，再用
`OpenPostgresStoreWithFollower` 写一笔、在主库侧立刻读到、在副本侧等到它出现。

## 滞后与一致性

副本的答案是**过去的某一刻**，滞后从毫秒到几分钟都可能（本机断网、主库在灌大批 WAL、
跨系统 collation 差异……）。因此：

- 只**观察**的读（进度、对话流、数据 API、contract/verdict 展示）走副本 —— 慢一点点没关系；
- 读一眼就要**照着写**、或必须看见**自己刚写的**那一版（受理、停止、广播、判决、收件箱计数、
  运行时自己那份记录），走主库 —— 由 `writerReads` 在调用点写明，清单见
  [store.md](store.md)「读写分离」。

**写进副本永远失败**（只读事务）—— 这正是「写远端」的边界：本地这份库只是镜子，不是账本。

## 坏了怎么办

| 症状 | 原因 | 处理 |
|------|------|------|
| 服务起来了，读走了远端 | 副本没起 / 不是副本（warning 里有原因） | `scripts/local-replica.sh status`；`start`；必要时重新 `init` |
| 槽上「没人连着」 | 副本停机，或主库 hba 里那个出口 IP 过期了 | 先 `start`；还是不行就更新主库 hba 那一行并 reload |
| 副本追不上（位点差得越来越远） | 本机断网 / 上行带宽不够 / 主库 WAL 太多 | 等它追；追不上就重新 `init`（几分钟） |
| 槽失效（`wal_status = lost`） | 停机太久，超过 `max_slot_wal_keep_size` 的 2GB，主库不再为它留 WAL | 删槽重建 + 重新 `init`（主库磁盘安全优先，这是有意的） |
| 主库换了 IP / 主机 | 云主机迁移 | 改 `PRIMARY_HOST`（`~/database/autonomy/replica.env` 或环境变量）再 `init` |
| 本地排序结果与主库不同 | 跨系统 glibc / collation 版本（Ubuntu 的库跑到 macOS 上），启动/连接时会有一条 collation 警告 | 只读展示一般无感；真介意就以主库为准，或让副本跑在同版本 Linux 里 |

重新 `init` 会**删掉**副本目录重做一次基础备份（副本上没有它自己的数据，删了不可惜）。

## 不做什么

- **不做自动 failover**：本地副本不会升主，主库挂了服务就是挂了。「远端是账本」这件事不被
  本地方案偷偷改掉。真要换主库，那是另一件事（有备份、有回滚、有明确的人按按钮）。
- **不在本地攒写**：没有「先写本地、再同步上去」这条路 —— 那等于两份真相、两个时间线。
- **不缓存查询结果**：读的是副本上的真表，不是另一层缓存；所以连接串失效时的行为是可预期的
  （退回主库），不会出现「看着像新数据其实是很久以前的缓存」。

## 相关文件

| 关注点 | 文件 |
|--------|------|
| 立副本、起停、看状态、生成 DSN | `scripts/local-replica.sh` |
| 读写分离的规则、配置变量、代码位置 | [store.md](store.md)「读写分离」 |
| 引擎的读侧实现与打开时的检查 | `src/db/postgres_store.go`（`readPool` / `Reading` / `openPostgresFollower`） |
| 上层「这次读要主库」的写法 | `src/store_read_split.go`（`writerReads`） |
| 引擎测试（含真实主副链路） | `src/db/postgres_read_split_test.go` |
