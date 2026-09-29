# 技术栈

以 `go.mod`、`build.sh`、`src/llmbackend/all` 为准。下面不列「计划中的栈」。

## 语言与模块

| 项 | 事实 |
|---|---|
| 语言 | Go **1.25**（`go.mod`：`github.com/kaulie/autonomy`） |
| 主包 | `src/` 根包 `package autonomy`：循环、HTTP、store 端口、会话 |
| 子包 | `src/db`、`src/capability/**`、`src/llmbackend/**`、`src/cursorsdk`、`src/clinesdk`、`src/codexsdk`、`src/context_builder`、`src/githubauth`、`src/bridgesdk` |
| 命令 | `cmd/autonomyd`、`cmd/autonomy`、`cmd/hello`、`cmd/models` |
| UI | **服务端 HTML**（`src/*_page.go`、`src/dashboard/templates/`）。没有独立前端工程、没有 Vite/React |

## 直接依赖（`go.mod` require）

| 模块 | 用途 |
|---|---|
| `connectrpc.com/connect` | Cursor SDK Bridge 的 Connect 客户端（`src/cursorsdk`） |
| `google.golang.org/protobuf` | 生成的 SDK stub |
| `modernc.org/sqlite` | sqlite engine（纯 Go，`CGO_ENABLED=0` 可编） |
| `github.com/jackc/pgx/v5` | postgres engine |

driver 只能出现在 `src/db`。根包碰 `database/sql` / 具体 driver 会让 `TestOnlyTheStorageEngineMayImportADriver` 失败。

## LLM harness

| Harness | 实现 | 运行时依赖 |
|---|---|---|
| `cursor`（默认） | `src/llmbackend/cursor` + `src/cursorsdk` | 独立二进制 `cursor-sdk-bridge`（gitignore，`scripts/fetch-bridge.sh` pin 版本） |
| `cline` | `src/llmbackend/cline` + `src/clinesdk/bridge/bridge.mjs` | Node ≥ 22、`@cline/sdk`（lock 入库，`node_modules` 不入库） |
| `codex` | `src/llmbackend/codex` + `src/codexsdk/bridge` | Node + `@openai/codex-sdk`（驱动本机 `codex` CLI） |
| `claude` | `src/llmbackend/claude` | 本机 `claude` CLI，无 Node 桥 |

链接方式：`import _ "github.com/kaulie/autonomy/src/llmbackend/all"`。缺 blank import 会在 `autonomyd` 启动日志里暴露，而不是到任务才炸。

`AUTONOMY_REASONER=local` 是离线 reasoner，与 harness 无关。`AUTONOMY_LLM_BACKEND` 只属于 **autonomyd 进程**。

## 构建与契约工具

| 工具 | 用途 |
|---|---|
| `build.sh` | 发版包装（见 [release.md](release.md)） |
| `swag` | 从注解生成 `docs/swagger.json` 后上报服务中心；**不**生成 `docs.go`，runtime 不依赖 swaggo |
| `buf` | `proto/sdk/v1` → `src/cursorsdk/gen`（`buf.gen.yaml`） |
| `scripts/register-contract.sh` | 调服务中心的 `register-go-service.sh`（可浅克隆 `kaulie/service-registry`） |

Go module/build 缓存默认 `${AUTONOMY_BUILD_CACHE:-~/.cache/autonomy/build}`，**不**放 `$TMPDIR`（macOS 会清临时目录，半截缓存会表现为「标准库丢包」）。`GOPROXY` 未设时 `build.sh` 用 `https://goproxy.cn,direct`。

## 本仓库没有的东西

- 无 `.github/workflows`（本检出无 CI workflow；PR 是否有检查以 GitHub 当时配置为准，不要假设有 Actions）。
- 无 Docker / Compose 作为一等部署路径。
- 无 `package.json` 在仓库根；Node 只存在于两个 bridge 子目录。
- 无应用内发版 API。

## 测试栈

`go test ./...`。同包测试必须留在被测包目录（见 `docs/testing.md`）。真打 provider 的测试用 `*_live_test.go` + 环境变量门（`CURSOR_LIVE` / `CLINE_LIVE` 等），默认跳过。Cline 桥单测：`cd src/clinesdk/bridge && npm test`。
