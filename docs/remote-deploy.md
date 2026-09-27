# Remote-server deploy

把发版包装到**另一台机器**上跑：监听从本机 loopback 换成对外地址，进程用现有的 `scripts/start.sh`（或它的 systemd 包装）。

本机部署平台（`127.0.0.1` + 平台的 rsync / restartCmd）一条都不改。远程机器要能被别的主机访问时，才设 `AUTONOMY_HTTP_HOST`。

发版仍只由部署平台打包（`build.sh` → `outputs/`）。这个仓库**没有**「对远程机器发起部署」的入口。

## 和本机部署差在哪

| | 本机平台 | 远程机器 |
|---|---|---|
| 监听 | `127.0.0.1:${SERVICE_PORT}`（默认） | `AUTONOMY_HTTP_HOST=0.0.0.0`（或这块网卡的 IP） |
| 进程 | 平台调 `scripts/restart.sh` | 同脚本，或 `scripts/autonomyd.service` |
| 探活 | `GET /health`（脚本对 loopback 探；绑 `0.0.0.0` 时也是） | 一样 |
| 契约登记 | 默认实例 `127.0.0.1:4300` | `INSTANCES=<公网或内网 IP>:4300` |

端口规则不变：`SERVICE_PORT` > `PORT` > `4300`。`.env` 里的旧 `AUTONOMY_HTTP_ADDR` 仍然不能把端口钉死。

## 主机怎么选

`scripts/listen_addr.sh`（`scripts/start.sh` 会 source）：

1. `AUTONOMY_HTTP_HOST`（环境或 `backend/.env`）
2. `SERVICE_HOST`（平台如果注入，和 `SERVICE_PORT` 同一层）
3. `127.0.0.1`

写成 `0.0.0.0` 或 `::` 时，进程绑全部网卡；探活仍打 `127.0.0.1`，避免 curl 对「全接口」地址不portable。

不走脚本时：`AUTONOMY_HTTP_ADDR=0.0.0.0:4300 go run ./cmd/autonomyd`（或发版包里的 `bin/autonomyd`）。直接跑二进制**不会**装桥、也不会做启动自检——远程机器请走 `start.sh`。

## 装到远程机器

在**有完整 checkout 的构建机**上打出发版包（需要 Go、桥依赖；见 `build.sh` / [scripts/README.md](../scripts/README.md)）：

```bash
./build.sh
# outputs/bin/autonomyd
# outputs/bin/cursor-sdk-bridge
# outputs/scripts/{start,stop,restart,listen_addr}.sh
# outputs/scripts/autonomyd.service
# outputs/src/agent_policy/
```

拷到远程（rsync / scp，目录可改；下面用 `/opt/autonomy`）：

```bash
rsync -a --delete \
  --exclude backend/ --exclude .cache/ \
  outputs/ user@remote:/opt/autonomy/
```

`backend/` 与 `.cache/` 是运行期的（`.env`、pid、日志、桥依赖缓存），覆盖会丢掉账号池所在库之外的本地覆盖项。sqlite 默认库在远程机器的 `~/database/autonomy/autonomy.db`（或 `AUTONOMY_STORE_DSN`），不在这个目录里。

第一次在远程上：

```bash
ssh user@remote
cd /opt/autonomy
# 生成 backend/.env 的最省事办法：先起一次（会写 600 权限的模板）再改主机
export AUTONOMY_HTTP_HOST=0.0.0.0
export SERVICE_PORT=4300
export RUNTIME_DIR=/opt/autonomy
bash scripts/start.sh
# 或写入 backend/.env：
#   AUTONOMY_HTTP_HOST=0.0.0.0
#   AUTONOMY_REASONER=llm
#   AUTONOMY_LLM_BACKEND=cline    # 若这台机器跑 Cline 而不是 Cursor 桥
```

账号不在 `.env`：先打开 `http://<remote>:4300/accounts`（或 `POST /api/accounts`）加上该 harness 的启用账号，再交任务。见 [accounts.md](accounts.md)。

防火墙放行 `4300/tcp`（或你改过的 `SERVICE_PORT`）。只给内网就绑内网 IP，不要 `0.0.0.0`。

## systemd

发版包带 `scripts/autonomyd.service`。默认 `RUNTIME_DIR=/opt/autonomy`、`AUTONOMY_HTTP_HOST=0.0.0.0`、`SERVICE_PORT=4300`。

```bash
sudo cp /opt/autonomy/scripts/autonomyd.service /etc/systemd/system/autonomyd.service
# 安装路径不是 /opt/autonomy 就改 PIDFile / WorkingDirectory / Environment / Exec*
sudo systemctl daemon-reload
sudo systemctl enable --now autonomyd
systemctl status autonomyd
curl -fsS "http://127.0.0.1:4300/health"
```

`Type=forking`：`start.sh` 拉起 `autonomyd`、写 pid、探活成功后退出。停服务走 `scripts/stop.sh`（TERM → 等 → KILL，见 [graceful-restart.md](graceful-restart.md)）。

优雅重启那两个 URL 在远程上改成这台机器能打到的地址，例如：

- `http://127.0.0.1:4300/api/ops/restart-notify`
- `http://127.0.0.1:4300/api/ops/restart-status`

部署平台若跑在**同一台**远程机器上，继续用 loopback。平台在别处时，填这台 autonomy 的可达 URL。

## 服务中心实例

远程实例要被组织里的其它服务找到时，登记时带上它的地址（本机那条可以并存）：

```bash
INSTANCES=127.0.0.1:4300,203.0.113.10:4300 \
  bash scripts/register-contract.sh
```

注册中心默认仍是 `http://127.0.0.1:4240`。中心不在这台远程机器上，就设 `REGISTRY_URL`。

## 核对

```bash
curl -fsS "http://127.0.0.1:4300/health"
# 从另一台机器（防火墙已放行时）：
curl -fsS "http://<remote-host>:4300/health"
# 期望：{"status":"ok","llm_backend":"…","llm_model":"…"}
```

`start.sh` 日志里应看到 `监听=<host>:<port>`，host 是你设的，不是误留的 `127.0.0.1`（除非你就是要只本机）。
