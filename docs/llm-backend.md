# LLM 后端（cursor / cline）与怎么切换

一次决策轮、一次委派，真正打给模型的那一步发生在 **runtime 进程**里（本地是
`go run ./cmd/autonomyd`，部署时是 `bin/autonomyd`）。`cmd/autonomy` 只是 HTTP 客户端
（[http-api.md](http-api.md)）——所以 `go run ./cmd/autonomy` 打出来的
`decide: …` 是 **runtime 的结论**，给它加环境变量没有任何作用。

**切后端 = 让 runtime 以另一个后端启动。** 这就是全文。

## 谁决定后端

`AUTONOMY_LLM_BACKEND`（`src/cline_client.go: defaultAgentBackend()`）：

| 值 | 后端 | 说明 |
|----|------|------|
| 空 / `cursor` | Cursor SDK 桥 | 默认 |
| `cline` / `cline_sdk` | Cline SDK 桥（[cline-reasoner.md](cline-reasoner.md)） | Node 桥 + `@cline/sdk` |

它决定 `LLMReasoner`（planner 的决策轮）与 capability 拿到的 agent（`code_edit` →
`Runtime.AcquireAgent`，`src/runtime.go`）跑在哪个 provider 上。`AUTONOMY_REASONER=local`
是离线 reasoner，与后端无关。

## 当前在哪个后端？问 runtime 自己

```bash
curl -s 127.0.0.1:4300/health
# {"status":"ok","llm_backend":"cursor","llm_model":"composer-2"}
```

`/health` 答的是**本进程将要用的**后端与它默认的模型（`src/http_server.go`）。`llm_model`
在 Cline 且没设 `AUTONOMY_CLINE_MODEL` 时是缺省的——那时模型由桥按 `cline auth` 保存的
provider/model 解析，runtime 自己并不知道。runtime 的日志里也有同样的事实，例如
`[autonomy] agent agent-10001 (cursor) continues task task-29 on a new session`。

## 本地切到 Cline

```bash
./scripts/install-cline-bridge.sh             # npm install @cline/sdk 到 src/clinesdk/bridge（Node ≥ 22，node_modules 不入库）
export AUTONOMY_LLM_BACKEND=cline
export AUTONOMY_CLINE_PROVIDER=deepseek       # 可省：用 `cline auth` 保存的 provider
export AUTONOMY_CLINE_MODEL=deepseek-v4-pro   # 可省：同上；AUTONOMY_LLM_MODEL 是 Cursor 的，不通用
export PROJECT_ROOT=$(pwd)
go run ./cmd/autonomyd                        # runtime 起在这里；另开一个终端跑 cmd/autonomy
```

桥脚本默认按 **cwd** 找 `src/clinesdk/bridge/bridge.mjs`
（`src/clinesdk/bridge.go: defaultBridgeScript()`，也会试 `../`、`../../`）；它不在时用
`AUTONOMY_CLINE_BRIDGE_SCRIPT` 指定绝对路径，Node 不在 PATH 上用 `AUTONOMY_CLINE_NODE_BIN`。
端口/后端/模型这些"运行期配置"的完整清单见 [cline-reasoner.md](cline-reasoner.md) 的环境变量表。

## 部署的 runtime 切到 Cline

部署包只有 `bin/autonomyd`、`scripts/`、`src/agent_policy/` 和 `backend/`：**发版包不带
Cline 桥**（`build.sh` 只打 cursor bridge，`@cline/sdk` 需要 npm install，见 build.sh 的注释）。
所以部署运行时切 Cline 是两步：

1. 在 `~/runtime/<service>/backend/.env` 里加（`scripts/start.sh` 生成的模板里这几行已有注释版）：

   ```bash
   AUTONOMY_LLM_BACKEND=cline
   AUTONOMY_CLINE_PROVIDER=deepseek
   AUTONOMY_CLINE_MODEL=deepseek-v4-pro
   # 包外的桥：一个装好 @cline/sdk 的 checkout 的桥脚本
   AUTONOMY_CLINE_BRIDGE_SCRIPT=/path/to/autonomy/src/clinesdk/bridge/bridge.mjs
   ```

2. **走部署平台重启**（`~/runtime/agent-control-plane-deployment` 的 UI / 管线）。不要在
   agent 进程里同步跑重启脚本——那会把网关一起带走。

起来后 `curl -s 127.0.0.1:4300/health` 应看到 `"llm_backend":"cline"`，`backend/server.log`
里会出现 `[cline-bridge] …`。想真正把桥打进发版包（部署机上没有 checkout 可用时），改
`build.sh` 让它随包发出 `src/clinesdk/bridge`——那是打包决定，不是运行期开关。

## 触发这一切的那条报错怎么读

```
[task task-29] status error: decide: empty model response (status=error msg=Increase limits
for faster responses You're out of usage. Switch to Auto, or ask your admin to increase your limit
to continue.)
```

`text_bytes=0`：**模型一个字都没答**，provider 把这个 run 以 `status=error` 结束。账号额度耗尽、
限流、模型下架都会长成这样——不是 prompt 的问题，也不是 autonomy 的 bug。两个后端
（`src/cursor_agent.go` / `src/cline_agent.go`）都把这种 run 记成失败并在错误里点名
`AUTONOMY_LLM_BACKEND`：空答案唯一有效的修法是换后端 / 换账号 / 换模型（Cursor 侧换模型是
`AUTONOMY_LLM_MODEL`）。重试没有意义——不在 `AUTONOMY_LLM_TURN_RETRIES` 的覆盖范围内，
provider 报错、余额不足都不重试（`src/llm_turn_retry.go`）。

再接别的 provider 是 `LLMStreamAdapter`（[llm-event-stream.md](llm-event-stream.md)）的事，
不是这条路径的事。
