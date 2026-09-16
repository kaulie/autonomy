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
| `AUTONOMY_CLINE_INTERACTIVE` | `1` | 新建 session 是否以 interactive 启动。**默认 `1`，这是"常驻会话"的前提**：SDK 对 **非** interactive session 是"单次 run"语义 —— run 一结束就 `finalizeSingleRun` → `shutdownSession`（`sessions.delete` + emit `ended`），下一次 `send` 直接 `session_not_found: session not found: cls-…`；interactive session 则 `completeInteractiveTurn` → idle 保留。这里没有 UI，**桥就是 host**：自己驱动 `send`，autonomy 的 prompt 是自足的（agent 的 yolo preset `enableAskQuestion=false`，不会等人）。设 `0` / `off` / `false` 回到单次 run 语义（只用来复现上面那个报错） |
| `AUTONOMY_CLINE_TRACE` | `0` | 桥 stderr 的 trace 级别：**`0`（默认）= 静默**（可读日志在 autonomy 侧，见"日志"一节）；`signal` = 只打里程碑（thinking 块、tool-call，带内容）；`1`（或 `AUTONOMY_LLM_DEBUG=1`）= 全量，每个事件一行（含 `chunk` 回声）。显式设了 `AUTONOMY_CLINE_TRACE` 时它优先于 `AUTONOMY_LLM_DEBUG` |
| `AUTONOMY_CLINE_TRACE_MAX` | `600` | 桥 `signal` 行里**每个字段**（thinking 文本、工具入参/输出）的字符预算，超出截断并标 `…(+Nch)` |
| `AUTONOMY_LLM_TRACE` | `1` | autonomy 侧的 `llm_messages` 日志：每个消息行一条（见"日志"一节）；`0` / `off` / `silent` 关掉 |
| `AUTONOMY_LLM_TRACE_MAX` | `500` | 该日志里每个字段的字符预算 |
| `AUTONOMY_CLINE_DATA_DIR` / `CLINE_DATA_DIR` / `CLINE_DIR` | `~/.cline` | 定位已保存的 Cline 配置（仅在 provider/model 未显式设置时使用） |
| `AUTONOMY_CLINE_NODE_BIN` | `node` | Node 可执行文件 |
| `AUTONOMY_CLINE_BRIDGE_SCRIPT` | `src/clinesdk/bridge/bridge.mjs` | 桥脚本路径 |
| `AUTONOMY_LLM_TIMEOUT` | `3m` | **run 空闲预算**：有事件就续期，静默超过它才中断 |
| `AUTONOMY_LLM_TURN_RETRIES` | `1` | **被输出上限截断的 round 的重试次数**：worker 的 run 因 `Model reached the maximum output token limit…` 失败时，在**同一个 session** 上再发一轮 `src/agent_policy/TURN_TRUNCATED.md`（"从上一个完成的片段继续、这一轮小一点"）；`0` 关闭。turn 级的失败不该赔掉整次委派 —— 见"排查「输出被截断」" |

**provider/model 的解析顺序**（SDK 两者都必填，缺了会内部 `undefined.trim()` 崩）：

1. `AUTONOMY_CLINE_PROVIDER` + `AUTONOMY_CLINE_MODEL`（显式）
2. `cline auth` 保存的配置（`<data-dir>/settings/providers.json` 的 `lastUsedProvider` + 它的 `model`）
3. 都没有 → 桥直接返回 `missing_provider` 错误，提示去设 env 或 `cline auth`

所以：**只要这台机器 `cline auth` 过，`AUTONOMY_LLM_BACKEND=cline` 一个变量就能跑**（桥会打印解析结果）。

## 会话语义

- 一个 autonomy `Agent` ↔ **一个常驻 Cline session**：第一次 `send` 由 `ClineCore.start` 起会话，
  之后每次 `send` 都是 `ClineCore.send`，上下文与 prompt cache 复用。
- **第一条 message 就是 AGENT_V2 frame**：session 的第一轮 prompt = frame + 本轮 delta，之后每轮只有 delta
  （会话自己记得 frame，`Agent.needsLLMFrame()` 判断，见 docs/execution-loop.md）。
- **常驻的前提是 `interactive: true`**（`AUTONOMY_CLINE_INTERACTIVE` 默认就是它）。SDK 里"常驻"与
  "interactive"是同义词：非 interactive 的 session 被当作单次 run，run 一结束 `finalizeSingleRun` →
  `shutdownSession` 就把它从活动表删掉（并 emit `ended`），下一次 `send` 报
  `session_not_found: session not found: cls-…` —— 这正是"第 2 轮永远失败"的根因。interactive
  session 走 `completeInteractiveTurn` → idle 留在表里，直到 autonomy 主动 `stop` / dispose。
  桥没有 UI，它自己就是 host（见 `config.mjs` 的 `interactiveSession`）。
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

## 日志（run 日志 = `llm_messages`，桥默认闭嘴）

run 的**可读日志在 autonomy 侧**：一条 `llm_messages` 行 = 一行日志，**行落库时即打**（见
`src/llm_message_log.go`，机制见 docs/llm-message.md）。也就是 thinking 块 / tool 调用+返回 /
assistant 返回 / user 输入各一行，**没有**逐 token 噪声：

```
[autonomy] llm seq=0 user: 看一下桥里 trace 是怎么打日志的
[autonomy] llm seq=1 thinking (1.834s): 先看仓库结构和现有测试，再决定改哪里
[autonomy] llm seq=2 tool execute_command call=toolu_01 args={"command":"ls src/clinesdk"} -> bridge  bridge.go …
[autonomy] llm seq=3 assistant: 找到了：桥在 bridge.mjs 里，事件是原样透传的
```

- `AUTONOMY_LLM_TRACE=0`（或 `off`/`silent`）关掉这组日志；`AUTONOMY_LLM_TRACE_MAX`（默认 500）
  是每个字段的字符预算，超出截断并标 `…(+Nch)`。
- **桥不参与**：`AUTONOMY_CLINE_TRACE` 默认 `0` = 桥什么都不打（逐字事件属于 `llm_events` 的保真回放，
  不是日志）。要桥这一层的信息时才开：
  - `AUTONOMY_CLINE_TRACE=signal`：桥打里程碑（thinking 块 / tool 调用，带内容，见下）；
  - `AUTONOMY_CLINE_TRACE=1`（或 `AUTONOMY_LLM_DEBUG=1`）：桥打**每个**事件（`[cline-bridge] trace: event chunk/chunk …`），
    用来排查"到底有没有事件在流"；同时 Go 侧 `AUTONOMY_LLM_DEBUG=1` 会打 `[cline event +…] <事件标签>`。
- 保活类日志照旧：`[cline Wait] still running events=N last=… silent=…s`（15s 一次）与
  `[cline Wait] begin/aborted` 等阶段行。

`signal` 档（仅桥、可选）打什么：

| 事件 | 输出 |
|---|---|
| `content_end/reasoning` | `think <thinking 全文>`（压成一行，按宽度截断；`redacted` 时是 `think <redacted>`） |
| `content_end/text` | `assistant <模型这一轮说的话>` |
| `content_start/tool` | `tool <name> start call=<id> args=<入参 JSON>` |
| `content_end/tool` | `tool <name> end call=<id> ok <耗时>ms out=<输出>`（失败时 `failed error=<消息>`） |
| `iteration_start` / `iteration_end` | `iteration <n> start` / `iteration <n> end tools=<k>` |
| `usage` | `usage in=… out=… cache_read=… cost=$…` |
| `notice` / `done` / `error` / `status` / `ended` | 各一行 |

丢掉**只有**逐字噪声：`chunk`（agent 回声 + stdout/stderr 流）、text/reasoning 的 `content_start`
增量、`content_update`（工具流式输出，结果在 `content_end/tool` 里）、`session_snapshot`（元数据 dump）、
`hook`（与上面的 tool/done/error 重复）。

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
| 常驻会话第 2 轮 | `text="hello-from-cline"`（真的跑了 shell 工具），usage in 3486/out 144/cacheRead 3200（`accumulated_delta`） |
| 常驻会话第 3 轮（记忆） | `"You asked me to reply with exactly \"pong\" and not use any tools."` ✅ |
| 常驻会话（plan 档，`TestLLMTraceLivePlanTurns`，2026-09-14 修复后实测） | 第 1 轮 `plan-ack`；第 2 轮同一个 plan session：`"You asked me to reply with exactly “plan-ack” and not use any tools."` ✅（修复前第 2 轮必报 `session_not_found`） |
| autonomy 层（`TestClineAgentLive`） | `text="pong"`，usage in 1671/out 32/cost 0.0755¢；事件 channel 分布 assistant=3 / thought=30 / meta=5 / status=2 / result=1 |
| run 日志 / 边跑边写消息（`TestLLMTraceLiveMessages`，2026-09-14 实测） | 桥只打 `info:` 行（默认静默），autonomy 侧打：`[autonomy] llm seq=0 user: Use the shell tool to run exactly: echo hi-from-llm-trace — …` → `seq=1 thinking (1.013s): The user wants me to run exactly: echo hi-from-llm-trace using shell tool, then reply with just the output. I need to use run_commands with the command. Let me do that.` → `seq=2 tool run_commands call=call_00_axoqJCMiuCepDERsElk08340 args={"commands":["echo hi-from-llm-trace"]} -> [{"query":"echo hi-from-llm-trace","result":"hi-from-llm-trace\n","success":true}]` → `seq=3 assistant: hi-from-llm-trace`；**这 4 行在 run 期间就写进 `llm_messages`**（不是 `Finish` 之后才出现） |

坑（已修）：SDK 在 **没有 system prompt** 时会内部 `undefined.trim()` 抛错（`Cannot read
properties of undefined (reading 'trim')`），所以桥会兜底一个通用 prompt，autonomy 侧
`defaultClineSystemPrompt()` 也会给默认值。

## 排查"零事件卡死"

症状：`decide: cline wait: run idle for 3m0s: no provider activity`，且桥在 `start session=…` 之后
**一条事件都没有**。这说明 run 在 provider 应答前就停了，按顺序看：

1. 先看 **run 日志**（autonomy 侧的 `[autonomy] llm seq=… ` 行）与保活行 `[cline Wait] still running events=N silent=…s`：
   正常 run 至少会有 begin + 心跳；连这个都没有才是真的零事件。要桥这一层的逐字确认就
   `AUTONOMY_CLINE_TRACE=1` 重跑（那时每个 `agent_event` / `chunk` 都会有一行）。
2. 零事件 → 先怀疑 **provider 侧排队/限流**（换个 provider/model 或稍后重试）；如果这个 session 是
   `AUTONOMY_CLINE_INTERACTIVE=0`（单次 run 语义）造成的，桥的 `session-mode=single-run` 会写在
   `start session=…` 那行里。
3. 有事件但中途静默超过 `AUTONOMY_LLM_TIMEOUT`（默认 3m）→ 调大该值即可；看门狗只在**静默**时触发，
   事件持续流动不会打断长 run。
4. 报错信息已经带提示：零事件时会是 `… (no SDK events arrived at all: check the provider status/quota
   for deepseek/deepseek-v4-pro; AUTONOMY_CLINE_TRACE=1 traces provider events)`。

## 排查「Model reached the maximum output token limit before completing the turn」

这条消息**不是 provider 的报错，而是 Cline SDK 自己的判定**：一个 turn 的 finish reason 是
`max-tokens`、且这一轮**没有产出任何 tool call** 时，`@cline/agents` 把整个 run 判为失败
（`node_modules/@cline/agents/dist/index.js` 里 `finishReason==="max-tokens" && toolCalls.length===0`
那一句抛的就是这条消息）。也就是说：**这一轮的输出被输出上限截断了** —— 模型正在写的东西（多半是一个
很大的 tool 调用）没写完，参数成了残 JSON，这一轮等于什么都没做，而 run 到此为止。

一个真实例子（task-93989469290b4c4e，deepseek-v4-flash，54 次模型调用后失败）：

| 观测 | 说明 |
|---|---|
| 同一轮里 `thinking` 13.5k 字符 + `editor` 入参 12.7k 字符 | 一轮里同时写了长思考和"整个文件一次写完"的编辑 |
| 之后连续几轮 `tool_call_started` 的 `args` 是 `{}` | 入参被截成残 JSON，SDK 报 `emitted invalid JSON arguments` |
| 最后一轮只有 178 字符的 thinking 就被切断 | 这一轮没有 tool call → SDK 抛错、run 结束 |

**为什么会被截断**：SDK 给每次请求算的输出预算是
`min(请求的 maxTokens 或模型 maxOutputTokens、contextWindow − 估算输入 − 1024)`（`@cline/llms` 的
`bT()`；什么都没声明时默认 32000），所以有三类诱因：

1. **一轮里塞太多**：整文件写入、把整个文件塞进 heredoc、或 pre-tool 思考太长 —— 本例就是这一类；
2. **会话太长**：上下文逼近 `contextWindow` 时剩余输出预算被压到很小（SDK 会打
   `Estimated prompt tokens exceed model context window`）；
3. **模型/路由声明的输出很小**，或 provider 收不到 `max_tokens` 而用它自己的默认值。

**怎么优化**（按性价比）：

1. **让它分块写**：`src/agent_policy/CODE_EDIT.md` 的 *Turn Budget Policy* 和
   `src/agent_policy/CONSTRAINTS.json` 的 `turn_output_budget` 把"一个 tool 调用约 ≤ 6000 字符 /
   150 行、文件分几次写、pre-tool 思考保持短、被截断就从上一个完成的片段继续"写进 prompt。这两个
   文件是渲染 prompt 时读的 policy 文件（planner 的 frame 与每个 worker 的 prompt 都带
   `{{CONSTRAINTS}}`），改完不需要重新编译。
2. **别让一次截断赔掉整次委派**：被截断是 **turn 级**的失败，session 是好的。`AUTONOMY_LLM_TURN_RETRIES`
   （默认 `1`，`0` 关闭）会在 worker 的 run 因此失败时，在同一个 session 上再发一轮
   `src/agent_policy/TURN_TRUNCATED.md`（"上一轮被截断、它什么都没跑、从上一个完成的片段继续、
   这一轮小一点"）。失败的原始轮与重试各自一行 `reason_turns`（失败的 `status=error` +
   `error_message` 保留），stderr 有 `[autonomy] truncated turn on <agent> (retry 1/1): …`；
   其它失败（provider 报错、余额不足、会话丢了）不重试（`src/llm_turn_retry.go`）。
3. **会话别拖太长**：一轮的输入越大，剩余输出预算越小。真跑到接近 `contextWindow` 时，换一次委派
   （新的 `code_edit` 会拿到新的 worker session）比继续撑更划算。
4. **模型/路由**：需要一次写大文件的任务换声明输出更大的模型（如 `deepseek-v4-pro`）。SDK 有
   `maxTokensPerTurn` 这个参数，但桥目前不传 —— 要显式压/抬每轮上限时，就落在 bridge 的
   `createAgent` 参数上（`src/clinesdk/bridge/bridge.mjs`）。

**已知边界**：planner（`LLMReasoner` 的决策轮）目前**不**吃这个重试 —— 它的一轮要重出的是决策
JSON，恢复方式与 worker 的"继续写代码"不同，留给后续。

## 已知边界（后续可做）

1. 切模式换 session 时**没有**用 `readLiveMessages` 播种历史（web-cursor 会）。所以每次**新建** session
   （切 mode/cwd、桥重启后的首个 run）都重新发一次 AGENT_V2 frame：常驻会话内的后续轮只发增量 delta
   （`Agent.needsLLMFrame()`，见 docs/execution-loop.md 与 `src/llm_frame_test.go`）。
2. 桥目前一个进程服务所有 session；若 Node 侧崩溃，`Client.ensure` 目前不会自动重启（会显式报错）。
3. 只接了 agent 路径 + `LLMReasoner`（后者通过 `AUTONOMY_LLM_BACKEND=cline` 一同生效）；
   `AUTONOMY_REASONER=local` 仍与 provider 无关。
4. 工具执行仍由 Cline 在 agent workspace 内完成（与 Cursor 路径一致），autonomy 的 capability/policy
   只治理 autonomy 自己的动作。
