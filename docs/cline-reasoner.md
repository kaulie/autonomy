# Cline reasoner（常驻 agent 后端）

把 autonomy 的 **LLM 后端**从 Cursor SDK 换成 Cline SDK（`@cline/sdk`），让 `code_edit` 之类的
capability 拿到的是一个**常驻 Cline session**（同一会话跨多次 prompt 保留上下文、工作区状态与
prompt cache），而不是"每次一问一答"。

## 为什么是"桥"

Cline 的 agent 内核（`@cline/agents` / `@cline/llms` / `@cline/core`）**只有 TypeScript/Node**，
autonomy 是 Go。所以 Go 侧 spawn 一个 Node 子进程（`src/clinesdk/bridge/bridge.mjs`），由它持有
Cline session 并把 SDK 的**原生事件**流式回传；Go 负责进程生命周期、请求/响应关联、事件扇出，以及
把原生事件映射成中立的 `LLMEvent`（`src/llm_event_cline.go`）—— 数据库与 trace 层不感知 provider。

```
Agent (Go, src/cline_agent.go)
  └─ clinesdk.Client (Go, src/clinesdk)
       └─ stdio NDJSON ─▶ bridge.mjs (Node)
                            └─ @cline/sdk ClineCore ─▶ provider (deepseek / anthropic / …)
```

这条路线是实测选出来的（spike 结论）：

| 方案 | 结论 |
|------|------|
| `cline --json` 子进程 | NDJSON 事件很全，但 **`--id` 续接会话在 `--json` 模式下不可用**（4 种调用姿势都报错），且 NDJSON 里没有 `sessionId` → 无法做常驻会话 |
| `cline --acp`（Agent Client Protocol） | 协议化 + `loadSession:true`，但 `authenticate` 只认 `cline`/`cline-pass`/`openai-codex`（OAuth），**api-key provider 进不去** |
| **`@cline/sdk` in-process（本方案）** | 常驻 session、`subscribe` 事件流、usage/cost 全都有；代价是需要 Node 运行时 + 一个桥进程 |

参考实现：gateway 的 `web-cursor/backend/src/providers/cline`（同样的 `ClineCore.create()` +
`subscribe` + `start/send`），本桥只保留无人值守所需的部分。

## 协议（stdio NDJSON，一行一个 JSON）

```
bridge → runtime
  {"type":"ready","protocol":"cline-bridge/1","pid":1,"node":"v22","sdk":"0.0.82"}
  {"type":"event","requestId":"req-3","agentId":"cls_…","sessionId":"cls-…","event":{…SDK 原生事件…}}
  {"type":"result","id":"req-3","ok":true,"result":{…run 摘要…}}
  {"type":"result","id":"req-3","ok":false,"error":{"code":"…","message":"…"}}

runtime → bridge
  {"id":"req-3","cmd":"send","params":{"agentId":"cls_…","prompt":"…","mode":"yolo"}}
```

命令：`ping` | `models` | `createAgent` | `send` | `stop` | `close` | `usage` | `shutdown`。
桥**不解释** provider 事件，只透传（外加 `requestId`/`agentId`/`sessionId` 便于 Go 关联）。

## 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `AUTONOMY_LLM_BACKEND` | `cursor` | 设为 `cline` 时，acquired agent（`code_edit` 等）与 `LLMReasoner` 都走 Cline |
| `AUTONOMY_CLINE_PROVIDER` | 空 → 用 `cline auth` 保存的 provider | Cline provider id：`deepseek` / `anthropic` / `openai` / `openrouter` … |
| `AUTONOMY_CLINE_MODEL` | 空 → 用 `cline auth` 保存的 model | 模型 id，如 `deepseek-v4-pro`。**注意**：`AUTONOMY_LLM_MODEL` 是默认(Cursor)后端的模型，不会用于 Cline |
| `AUTONOMY_CLINE_API_KEY` | 空 → 用 `cline auth` 保存的凭据 | provider key；通常不用设 |
| `AUTONOMY_CLINE_BASE_URL` | 空 | 兼容 OpenAI 的自建端点等 |
| `AUTONOMY_CLINE_SYSTEM_PROMPT` | 见下 | 覆盖 session system prompt（SDK **必须**有非空 system prompt） |
| `AUTONOMY_CLINE_INTERACTIVE` | `0` | 桥默认以 **headless** 方式启动 SDK（`interactive: false`）。设为 `1` 恢复 web-cursor 那种"有 UI 应答"的模式；**无 UI 时不要开**，否则 SDK 的交互请求没人回答，表现就是"零事件卡死" |
| `AUTONOMY_CLINE_TRACE` | `0` | 设为 `1`（或复用 `AUTONOMY_LLM_DEBUG=1`）后，桥会把 SDK 的**每个事件**打到 stderr，用来诊断"到底有没有事件在流" |
| `AUTONOMY_CLINE_DATA_DIR` / `CLINE_DATA_DIR` / `CLINE_DIR` | `~/.cline` | 定位已保存的 Cline 配置（仅在 provider/model 未显式设置时使用） |
| `AUTONOMY_CLINE_NODE_BIN` | `node` | Node 可执行文件 |
| `AUTONOMY_CLINE_BRIDGE_SCRIPT` | `src/clinesdk/bridge/bridge.mjs` | 桥脚本路径 |
| `AUTONOMY_LLM_TIMEOUT` | `3m` | **run 空闲预算**：有事件就续期，静默超过它才中断 |

**provider/model 的解析顺序**（SDK 两者都必填，缺了会内部 `undefined.trim()` 崩）：

1. `AUTONOMY_CLINE_PROVIDER` + `AUTONOMY_CLINE_MODEL`（显式）
2. `cline auth` 保存的配置（`<data-dir>/settings/providers.json` 的 `lastUsedProvider` + 它的 `model`）
3. 都没有 → 桥直接返回 `missing_provider` 错误，提示去设 env 或 `cline auth`

所以：**只要这台机器 `cline auth` 过，`AUTONOMY_LLM_BACKEND=cline` 一个变量就能跑**（桥会打印解析结果）。

## 会话语义

- 一个 autonomy `Agent` ↔ **一个常驻 Cline session**：第一次 `send` 由 `ClineCore.start` 起会话，
  之后每次 `send` 都是 `ClineCore.send`，上下文与 prompt cache 复用。
- **模式/工作目录是粘的**：Cline session 创建时就定了 `plan`（只读）或 `yolo`（工具自动批准）以及 cwd。
  autonomy 的 `ReasonModePlan` → Cline `plan`，其余 → `yolo`；需要另一种时**再建一个 session 并保留旧的**
  （key = `mode` + cwd）。**不会在别的 session 正在启动时去 close 它** —— 那样可能让新 run 静默卡住；
  所有 session 在该 agent `Release`/dispose 时一起关闭。
- ephemeral agent（capability 通过 `Runtime.AcquireAgent` 拿到的）在 `Release` 时 `close` session；
  常驻 agent 保留 handle 以备恢复（与 Cursor 路径一致）。

## 事件映射（`src/llm_event_cline.go`）

| Cline 原生 | channel | 说明 |
|---|---|---|
| `agent_event:content_start/text`、`content_update/text` | `assistant` | `TextDelta` = `text`（增量）或 `accumulated` |
| `agent_event:content_end/text` | `assistant` | 该段完整文本 |
| `agent_event:content_start/reasoning`、`content_end/reasoning` | `thought` | `TextDelta` = `reasoning` |
| `agent_event:content_start/tool`、`content_update/tool`、`content_end/tool` | `tool` | payload 额外补上中立键 `call_id` / `args`（入参）/ `result`（输出）/ `chunk`（stdout 流） |
| `agent_event:usage` | `meta` | token 用量（另有 run header 的 usage 列） |
| `agent_event:done` | `result` | 终态标记（reason / text / iterations） |
| `agent_event:error` | `error` | provider 报错 |
| `status` | `status` | session 生命周期（running/…） |
| `session_snapshot` | `meta` | session 元数据（cwd、model、capabilities…） |
| `chunk` (stream≠agent) | `tool` | 进程 stdout/stderr 输出 |
| `chunk` (stream=agent) | **丢弃** | 它是 `agent_event` 的逐字 JSON 回声，落库会重复 |

`llm_events.event_type` 保留 provider 判别符（如 `agent_event:content_end:tool`），`payload` 保留原生
payload —— 与原 Cursor 适配器同样的保真度策略，因此 `llm_messages` 的聚合（thinking / tool）无需改动。

## Token 与成本

- 首次 `start` 的 result 自带 usage；**常驻 `send` 的 result 不带**，桥用 `getAccumulatedUsage` 的
  **差值**（`usageSource: "accumulated_delta"`）补上。
- 成本来自 provider 上报的 `totalCost`（USD），Go 侧换算成 `cost_cents` 存表（`CostKnown` 区分
  "没有成本"与"成本为 0"）。

## 启用与验证

```bash
./scripts/install-cline-bridge.sh          # npm install @cline/sdk（node_modules 已 gitignore）
export AUTONOMY_LLM_BACKEND=cline          # 若已 `cline auth`，这样就能跑（provider/model 自动解析）
# 需要显式指定时：
export AUTONOMY_CLINE_PROVIDER=deepseek
export AUTONOMY_CLINE_MODEL=deepseek-v4-pro
export AUTONOMY_CLINE_API_KEY=sk-...       # 可选：已 `cline auth` 则不需要
export PROJECT_ROOT=$(pwd)
export AUTONOMY_REASONER=llm
go run ./cmd/autonomy                      # LLMReasoner 与 code_edit 现在都走 Cline
```

- 无 Node 的单元/集成测试：`go test ./src/...`（用 `src/clinesdk/fakebridge` 假桥覆盖协议与映射）。
- **真实链路**（需要 Node + 依赖 + 凭据）：
  ```bash
  CLINE_LIVE=1 AUTONOMY_CLINE_PROVIDER=deepseek AUTONOMY_CLINE_MODEL=deepseek-v4-pro \
  AUTONOMY_CLINE_API_KEY=sk-... go test ./src/clinesdk -run TestClineBridgeLive -v -timeout 6m
  ```
  该测试会：起桥 → 第一次 prompt 断言文本与 usage → 第二次 prompt 断言复用同一 session →
  第三次 prompt 断言**会话记得第一次的提问**。

## 实测记录（deepseek-v4-pro，2026-09-14）

| 场景 | 结果 |
|---|---|
| 桥协议自检（Node 驱动） | `ready` → `ping`（216 个 provider id）→ `createAgent` → `send` 正常 |
| Go → 桥 → SDK → deepseek（`TestClineBridgeLive`） | 首次 prompt：`text="pong."`，events=20，usage in 1637/out 4/cacheRead 1536/cost $0.0000530 |
| 常驻会话第 2 轮 | `text="hello-from-cline"`（真的跑了 shell 工具），usage in 3444/out 105/cacheRead 3072（`accumulated_delta`） |
| 常驻会话第 3 轮（记忆） | `"You asked me to reply with exactly \"pong\" and not use any tools."` ✅ |
| autonomy 层（`TestClineAgentLive`） | `text="pong"`，usage in 1671/out 32/cost 0.0755¢；事件 channel 分布 assistant=3 / thought=30 / meta=5 / status=2 / result=1 |

坑（已修）：SDK 在 **没有 system prompt** 时会内部 `undefined.trim()` 抛错（`Cannot read
properties of undefined (reading 'trim')`），所以桥会兜底一个通用 prompt，autonomy 侧
`defaultClineSystemPrompt()` 也会给默认值。

## 排查"零事件卡死"

症状：`decide: cline wait: run idle for 3m0s: no provider activity`，且桥在 `start session=…` 之后
**一条事件都没有**。这说明 run 在 provider 应答前就停了，按顺序看：

1. `AUTONOMY_CLINE_TRACE=1` 重跑：确认是否真的零事件（正常首轮应有几百条 `status/iteration_start/
   content_start:reasoning`…）。
2. 零事件 → 先怀疑**交互式卡住**（`AUTONOMY_CLINE_INTERACTIVE` 不要开）或 **provider 侧排队/限流**
   （换个 provider/model 或稍后重试）。
3. 有事件但中途静默超过 `AUTONOMY_LLM_TIMEOUT`（默认 3m）→ 调大该值即可；看门狗只在**静默**时触发，
   事件持续流动不会打断长 run。
4. 报错信息已经带提示：零事件时会是 `… (no SDK events arrived at all: check the provider status/quota
   for deepseek/deepseek-v4-pro; AUTONOMY_CLINE_TRACE=1 traces provider events)`。

## 已知边界（后续可做）

1. 切模式换 session 时**没有**用 `readLiveMessages` 播种历史（web-cursor 会）；当前依赖 prompt 自带上下文。
2. 桥目前一个进程服务所有 session；若 Node 侧崩溃，`Client.ensure` 目前不会自动重启（会显式报错）。
3. 只接了 agent 路径 + `LLMReasoner`（后者通过 `AUTONOMY_LLM_BACKEND=cline` 一同生效）；
   `AUTONOMY_REASONER=local` 仍与 provider 无关。
4. 工具执行仍由 Cline 在 agent workspace 内完成（与 Cursor 路径一致），autonomy 的 capability/policy
   只治理 autonomy 自己的动作。
