# 发布细节

## 发版是什么

一次发布 = 部署平台对某个 git ref 跑本仓库根目录的 `build.sh`，得到 `outputs/`，把 `APP_VERSION`（8 位短 hash）打进二进制，再 rsync 到 runtime。

`build.sh` 头部写明调用方只有两种：

- 控制面流水线：`POST :4220/api/deploy-notify {serviceId:"autonomy"}`
- 平台自己的 `release.sh autonomy [ref]`

本仓库**没有**第二条「本地一键上线」脚本。`outputs/` 在 `.gitignore`。`VERSION` / `COMMIT` / `GIT_REPO_URL` 由**调用方**写入发版包，本脚本不写。

## `build.sh` 必须满足的平台约定

- cwd = 仓库根；`APP_VERSION` 由平台注入。
- 必须产出 `outputs/`，且其中必须有 `scripts/restart.sh`（平台硬性要求）。
- 运行期可变内容不进包：`.env`、pid、日志、库都不在 `outputs/`。
- 末尾幂等登记服务契约（`scripts/register-contract.sh`）。

## 产物清单（对照脚本，不是愿望单）

```
outputs/
  bin/autonomyd                          # CGO_ENABLED=0，-ldflags 写入 main.version
  bin/cursor-sdk-bridge                  # 目标平台的 pin 版本；取不到是警告，不是构建失败
  scripts/start.sh stop.sh restart.sh listen_addr.sh autonomyd.service
  src/agent_policy/                      # 提示词原样带上
  src/clinesdk/bridge/                   # 源码 + lock + bridge-deps.tgz
  src/codexsdk/bridge/                   # 同上
```

Cline / Codex 桥依赖三个来源，按序：checkout 的 `node_modules` → 构建机缓存（lock 的 sha256）→ `npm ci --omit=dev`。三者都失败 = **构建失败**（除非 `AUTONOMY_ALLOW_MISSING_<NAME>_BRIDGE=1`）。缺桥的包会被 `start.sh` 启动自检拒掉，不如在构建时报清。

Cursor 桥取不到：警告。用 cursor 后端部署时 `start.sh` 会拒绝启动；cline / codex / claude 不受影响。版本与平台归一化**只**在 `scripts/fetch-bridge.sh`（`--print-version` / `--print-platform`），`build.sh` 问它，自己不记第二份 pin。

## 跨平台打包

控制面给另一台 OS 打包时注入 `GOOS` / `GOARCH`：

```bash
GOOS=linux GOARCH=amd64 ./build.sh
```

会传到 `go build` 与 `fetch-bridge.sh`。构建机自己的 `third_party/bin/cursor-sdk-bridge`（本机开发用）在目标平台不同时**既不采用也不覆盖**。缓存按 `<版本>/<平台>/` 分目录。

## 契约登记

注解是唯一真源：

- General API Info：`cmd/autonomyd/main.go`
- 每个 handler：`src/http_server.go`
- 对齐测试：`src/contract_test.go`

`register-contract.sh`：`swag init` → `docs/swagger.json`（gitignore）→ 服务中心 `register-go-service.sh`。幂等：没变化不写库。

默认登记参数（可被环境覆盖）：

| 变量 | 默认 |
|---|---|
| `SERVICE_NAME` | `autonomy` |
| `DEPARTMENT_ID` | `D0005` |
| `INSTANCES` | `127.0.0.1:4300` |
| `REGISTRY_URL` | `http://127.0.0.1:4240` |
| `HEALTH_PATH` | `/health` |
| `GIT_REPO` | `https://github.com/kaulie/autonomy` |
| `SWAG_MAIN` | `cmd/autonomyd/main.go` |

远程实例把 `INSTANCES` 加上可达地址。中心不在本机则设 `REGISTRY_URL`。`cmd/autonomy` 也会被自动探测成入口，所以必须显式 `SWAG_MAIN`。

只产 JSON，不产 `docs.go`，避免把 swaggo 拖成运行期依赖。

## 本地验证发版包（不是上线）

```bash
./build.sh
RUNTIME_DIR="$(pwd)/outputs" bash outputs/scripts/start.sh
curl -fsS "http://127.0.0.1:4300/health"
```

这只证明包装能起。把它当成「已部署到线上 runtime」是错的。

## 版本怎么对

部署后看 `GET /health` 与进程启动行 `[autonomy] version=…`。二进制里的 `main.version` 来自构建时的 `APP_VERSION`。本地 `go run` 没有平台注入时是 `"dev"`。
