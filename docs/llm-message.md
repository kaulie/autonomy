# LLM Messages

## 目标

把一次交互拆成**多条独立记录**：各自可被引用、查询、审计；
每条记录通过 `parent_id` **溯源**到它回答的那一条具体输入。

- 不再“一行同时代表输入和输出”，而是**一行 = 一条消息**。
- 返回/中间产物 → 输入 的指向是显式外键（`parent_id → user.id`），不靠位置猜测。
- 与 provider 无关：中立模型先行，接入新 LLM 不改表。
- **中间活动也被记录**：thinking 与 tool 调用/返回也从原始流聚合进消息层，
  这样消息层读到的都是**整条消息**，而不是逐 token 的碎片（碎片只留在 `llm_events`）。

## 数据模型

```
reason_turns (1)  ────  run header：provider/status/usage/耗时/run_id（内容列保留）
      │  id
      ├──▶ llm_messages (N) ── user 输入 + thinking/tool 聚合行 + assistant 返回（parent_id 溯源）
      └──▶ llm_events   (N) ── provider 原始流（逐事件，可选，见 llm-event-stream.md）
```

- `seq` 是 run 内顺序：`0` = 该 run 的输入；`1..N` = thinking/tool 聚合行（按事件发生顺序）；
  assistant（最终返回）排在最后，为 `N+1`。无中间行时 assistant 仍为 `1`。
- `role`：`user` | `agent` | `assistant` | `thinking` | `tool`。
  `user` 与 `agent` 都是"这条 run 的输入**由谁写**"：`user` = 用户（人）的任务，`agent` = **另一个 agent 委托给它的子任务**
  （capability 把工作交给 worker agent）。用户只写顶层 task 的输入，往下每一层都是 agent 对 agent，
  所以委托出去的 run 不能再显示成 `user` 在说话。
- `parent_id`：除 user 行外，每行都指向本次 run 的 user 行 id —— 这条边就是「产物 → 输入」的溯源链。
- `content` 存逐字原文；`normalized_content` 是派生形式（assistant 的 ```json fence 抽取、
  tool 行的 `{name, args, call_id}`）。

## DDL

```sql
CREATE TABLE IF NOT EXISTS llm_messages (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  turn_id            INTEGER NOT NULL DEFAULT 0,   -- → reason_turns.id（属于哪次 run）
  task_id            TEXT    NOT NULL DEFAULT '',
  agent_id           INTEGER NOT NULL DEFAULT 0,
  cycle              INTEGER NOT NULL DEFAULT 0,
  seq                INTEGER NOT NULL DEFAULT 0,   -- run 内顺序：输入=0, 中间行 1..N, assistant=N+1
  role               TEXT    NOT NULL DEFAULT '',  -- 输入作者: user | agent；产物: assistant | thinking | tool
  parent_id          INTEGER,                      -- 非 user 行 → 本次 run 的 user 消息 id（溯源）
  content            TEXT    NOT NULL DEFAULT '',
  normalized_content TEXT    NOT NULL DEFAULT '',
  llm_provider       TEXT    NOT NULL DEFAULT '',
  model              TEXT    NOT NULL DEFAULT '',
  run_id             TEXT    NOT NULL DEFAULT '',
  status             TEXT    NOT NULL DEFAULT '',
  created_at         TEXT    NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_messages_turn_seq ON llm_messages(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_messages_parent ON llm_messages(parent_id);
CREATE INDEX IF NOT EXISTS idx_llm_messages_task   ON llm_messages(task_id, cycle);
```

设计要点：

- 表结构属于 engine（方言/迁移不外泄）；上层只依赖 `Store` 暴露的 `LLMMessage` / `ReasonTurnHandle`。
- `UNIQUE(turn_id, seq)` + `INSERT OR IGNORE`：重复 finish 或回放不会产生重复消息。
- `content` 与 run header 的 `input / raw_output` **双写**（header 内容列**先保留**，见下）。

## 写入流程

消息是**边跑边写**的：一次 run 的 thinking 块 / tool 调用一旦"不会再变"（thinking 块在下一个
tool 消息开始时结束，tool 消息在它的返回到达时结束），就立刻落库**并打一行日志**——所以长 run 在
跑的过程中就能从表里和 stderr 上看到进展，不必等 `Finish`（`AppendLLMMessages`）。

```
BeginLLMTrace(agent, taskID, cycle, mode, input)
  → BeginReasonTurn：建 header，写 user 消息(seq=0)，返回 handle{TurnID, InputMessageID}
       ↓  Emit(ev) 逐条：llm_events（可选，AUTONOMY_LLM_EVENTS）
       │                    ＋ 聚合：消息一完成就 AppendLLMMessages(seq 固定为创建序) + 打日志
       ↓  run 结束
Finish(res)
  → 冲掉仍未闭合的聚合行（尾部 thinking 块、没返回的 tool）
  → FinishReasonTurn(handle, res)：更新 header，
       读回本 run 的 llm_events 再聚合一遍作为兜底（upsert，(turn_id, seq) 幂等）
       写 assistant 消息(seq = 当前最大 seq + 1，parent_id=InputMessageID)
```

- **流式路径**（plan / agent 模式）走 `BeginLLMTrace` / `Emit` / `Finish`，见 `src/llm_trace.go`。
- **一次性路径** `InsertReasonTurn` 在同一事务里写 header + user + assistant 两条消息并互链
  （本地 reasoner 无原始流，故无中间行）。
- `Store.ListLLMMessages(turnID)` 按 `seq` 返回该 run 的消息。
- `AUTONOMY_LLM_EVENTS=0` 只关**原始流**（`llm_events`）；聚合出的 `llm_messages` 照写，因为它是
  对话本身而不是回放。
- 端到端验证（opt-in，真 provider）：`CLINE_LIVE=1 AUTONOMY_CLINE_PROVIDER=… AUTONOMY_CLINE_MODEL=…
  go test ./src -run TestLLMTraceLiveMessages -v` —— 断言 thinking/tool/assistant 三行都在 `Finish`
  之前就已落库。

## 日志：一行一个消息

每条 `llm_messages` 行落库时同步打一行 stderr（`src/llm_message_log.go`），run 的日志因此就是
"对话"，而不是逐 token 流：

```
[autonomy] llm seq=0 user: 看一下桥里 trace 是怎么打日志的
[autonomy] llm seq=1 thinking (1.834s): 先看仓库结构和现有测试，再决定改哪里
[autonomy] llm seq=2 tool execute_command call=toolu_01 args={"command":"ls"} -> {"exit":0,…}
[autonomy] llm seq=3 assistant: 找到了：桥在 bridge.mjs 里，事件是原样透传的
```

- `AUTONOMY_LLM_TRACE=0` / `off` / `silent` 关掉；默认开。
- `AUTONOMY_LLM_TRACE_MAX`（默认 500）是每个字段的字符预算，超出截断并标 `…(+Nch)`。
- 保活类日志（心跳 / 阶段行）不属于消息层，仍由各 SDK 客户端照常打。

## 聚合规则（原始流 → 消息层）

中间行由 `chatAggregator`（`src/llm_message.go`）从事件流派生，**流式**进行：

- 一次 run 里边跑边喂（`LLMTrace.Emit` → `AppendLLMMessages`）：消息一旦不会再变就落库；
- 整条流一次性派生（`aggregateChatMessages(events)`，finish 兜底 / 回填）就是"喂完再 `flush()`"，
  两条路径共用同一个 aggregator，所以不可能不一致。

| 原始事件（`llm_events`） | 聚合为 | `content` | `normalized_content` |
|---|---|---|---|
| `thought`（连续的 `thinking` chunk 段） | **1 行**（`role=thinking`），把该段所有 `text_delta` 拼接 | 整段推理文本 | 空（`{"duration_ms":N}` 当能算出时长时） |
| `tool`（同一 `call_id` 的 `running` 调用 + `completed` 返回） | **每个 `call_id` 1 行**（`role=tool`） | 工具返回（`result` 的 JSON） | `{name, args, call_id}` |
| `assistant`（逐 chunk） | **不产生行** —— 由 `reason_turns.raw_output` 写为最终 assistant 行 | 最终返回 | 归一化输出 |
| `status` / `meta` / `result` | 跳过（无对话文本） | — | — |

要点：

- 只聚合、不删原始：`llm_events` 仍保留逐事件（逐 token）全保真流水。
- 顺序按事件发生先后；`seq` 在组创建时就定下（= 创建序），所以边跑边写的 `seq` 与最终派生的一致；
  assistant 永远是最后一行。
- `content` 是工具**返回**（结果），调用参数放 `normalized_content`。
- `parent_id` 统一指向本 run 的输入行（seq=0，role 为 `user` 或 `agent`：一次 run 只有一条输入，
  中间产物与返回都回答它）。
- 输入行的 `role` 就是**委托可见性**：`runtimeAgentSession.Prompt` 是 capability 委托给 worker 的入口，
  它用 `BeginLLMTraceFrom(agent, LLMMessageRoleAgent, …)` 开 trace，所以那条 `seq=0` 行记的是
  **agent 在说话**（谁委托的），而不是用户又发了一次任务。

## 保留：header 内容列不移除

`reason_turns.input / raw_output / normalized_output` **暂时保留**（双写）。原因：完全向后兼容，
既有消费方零改动。`llm_messages` 是可引用的独立记录；将来如需收敛为单一真源，再单独移除 header 内容列。

## 迁移 / 回填

- 打开数据库时自动建表与索引。
- 对**早于消息表**存在的 `reason_turns` 行做幂等回填：`input` → user 消息、`raw_output` → assistant
  消息（`parent_id` 互链）；若该 run 已有 `llm_events`，同时聚合出 thinking/tool 中间行。
  只回填尚无消息的 run，重复打开不重复写入。

## 代码位置

| 关注点 | 文件 |
|--------|------|
| `Store` 契约 / `LLMMessage` / `LLMMessageRole` / `ReasonTurnHandle` | `src/store.go` |
| 流式聚合器 + 整条流派生（DB 无关） | `src/llm_message.go` |
| 每行消息 → 日志（`AUTONOMY_LLM_TRACE` / `_MAX`） | `src/llm_message_log.go` |
| 表结构 / 读写 / 聚合写入（upsert）/ 回填 | `src/sqlite_store.go` |
| header + 消息写入编排（边跑边写） | `src/llm_trace.go` |
