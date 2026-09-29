# 部署细节

## 谁负责部署

部署**只**由部署平台完成：源码 [`agent-control-plane-deployment`](https://github.com/kaulie/agent-control-plane-deployment)，本机安装目录 `~/runtime/agent-control-plane-deployment`，HTTP `:4220`。

平台从 GitHub ref 调用本仓库 `build.sh`，得到 `outputs/`，再按服务契约 rsync 到 runtime 并执行 `restartCmd`（`scripts/restart.sh` = stop + start）。

本仓库侧：

- **没有**「对本服务发起部署」的 HTTP 入口。
- **禁止**在 agent 进程里同步跑 deploy / restart 脚本（会把正在跑命令的 gateway 杀掉）。
- **禁止**手改 `/Users/gaolei/runtime/**`、手改 `deployment-<hash>/` 快照、覆盖 runtime 的 `backend/.env`。

`service.deploy` 能力触发的是**别的服务**在平台上的流水线，不是给自己 `restart.sh`。

平台入口（需要上线时由人 / 平台调用，不是本仓库默认步骤）：

```bash
curl -sS -X POST http://127.0.0.1:4220/api/deploy-notify \
  -H 'content-type: application/json' \
  -d '{"serviceId":"autonomy","ref":"main"}'
```

## 端口与监听

优先级：**`SERVICE_PORT` > `PORT` > `4300`**。`.env` 里的旧 `AUTONOMY_HTTP_ADDR` **不能**把端口钉死（避免契约换端口后卡在旧值）。

主机：`AUTONOMY_HTTP_HOST` > `SERVICE_HOST` > `127.0.0.1`。本机平台默认 loopback。远程对外才设 `AUTONOMY_HTTP_HOST=0.0.0.0`（见 `docs/remote-deploy.md`）。绑 `0.0.0.0` 时探活仍打 `127.0.0.1`。

**本机开发不要占用 `4300`**，那是线上契约端口。本地手起用别的 `SERVICE_PORT`，或确认线上已停。

探活：`GET /health`（`/healthz` 同处理）。期望含 `"status":"ok"` 以及当前 `llm_backend`。

## 运行目录布局

`scripts/start.sh` 约定（`RUNTIME_DIR` 由平台注入，否则是脚本的上一级）：

```
$RUNTIME_DIR/
  bin/autonomyd
  bin/cursor-sdk-bridge          # 发版包自带
  scripts/{start,stop,restart,listen_addr}.sh
  src/agent_policy/              # PROJECT_ROOT 指到 RUNTIME_DIR
  src/clinesdk/bridge/ …         # 发版包自带；依赖解到包外缓存
  src/codexsdk/bridge/ …
  backend/.env                   # 首次生成，权限 600；平台 rsync --delete 时保留
  backend/data/                  # 仅 pid / 日志，不是库
  backend/runtime.pid
  backend/server.log
```

sqlite 库**不在** runtime 目录：默认 `~/database/autonomy/autonomy.db`。换位置用 `AUTONOMY_STORE_DSN` 或 `AUTONOMY_DATA_DIR`。换了就等于换库。

远程机器示例路径是 `/opt/autonomy` + systemd 单元 `scripts/autonomyd.service`（`Type=forking`）。本机平台不用 systemd。

**不要**把 `/Users/gaolei/Projects/deepseek_web_cursor` 或其它项目的共享 worktree 当成 autonomy 的 runtime。

## 启动自检

`start.sh` 在拉起 `autonomyd` 之前：

1. 按所选 `AUTONOMY_LLM_BACKEND` 准备桥（包内 cursor 二进制；Cline/Codex 的 `bridge-deps.tgz` 按 sha256 解到 `${AUTONOMY_CACHE_DIR:-$RUNTIME_DIR/.cache}/…` 并 symlink）。
2. 自检桥（或 Claude 的 `claude --version`）。不过 → 非 0 退出 → **部署失败、留在上一版**，而不是起来后每条任务都挂。

直接跑 `bin/autonomyd` / `go run ./cmd/autonomyd` **不会**装桥、也不会做这套自检。远程请走 `start.sh`。

## 运行期配置（`backend/.env`）

只放部署参数，不放「谁付费」：

- `AUTONOMY_REASONER=llm`（或 `local`）
- `AUTONOMY_LLM_BACKEND=cursor|cline|codex|claude`
- `AUTONOMY_STORE_ENGINE` / `AUTONOMY_STORE_DSN` / `AUTONOMY_POSTGRES_DSN` / `AUTONOMY_STORE_READ_DSN`
- `AUTONOMY_HTTP_HOST`（远程）
- `CURSOR_SDK_BRIDGE_BIN` / `CURSOR_SDK_BRIDGE_PROXY`（仅当 Cursor 出网必须走代理；**不要**把进程的 `HTTP_PROXY` 传给桥——未配置的代理会让 CreateAgent 无错误地挂住）
- GitHub App：`GITHUB_APP_ID` / `GITHUB_APP_INSTALLATION_ID` / `GITHUB_APP_PRIVATE_KEY_PATH`

账号与 API key 在 `/accounts`（或 `/api/accounts`）。池子没有该 harness 的启用账号时 run 被拒绝，不回落到环境变量。

## 优雅重启

平台若登记了**两个** URL，重启变为 drain 后再 rsync + restart：

| 端点 | 本服务默认 |
|---|---|
| notify | `http://127.0.0.1:4300/api/ops/restart-notify` |
| poll | `http://127.0.0.1:4300/api/ops/restart-status` |

只登一个或都不登：平台按老办法硬重启，可能切在 cycle 中间。

Drain 语义：不再启动新 run；新指令仍受理进 inbox；在途 run 跑完后 `canRestart` 为真。`AUTONOMY_DRAIN_TIMEOUT` 默认 10 分钟（`0` = 永不）避免失败部署楔死服务。`scripts/stop.sh` 发 `SIGTERM`：在途 run 给 `AUTONOMY_SHUTDOWN_GRACE`（默认 10s，须落在 stop 的 15s 窗口内），然后按 stop 路径收尾并写明是重启切断，不是用户停的。

## 与平台其它服务的端口（避开）

| 服务 | 默认端口 |
|---|---|
| autonomy（本服务） | **4300** |
| agent-control-plane（web-cursor） | 4211 |
| 部署平台 | 4220 |
| 服务中心 | 4240 |
| organization | 4244 |

本机开发不要抢这些线上端口。
