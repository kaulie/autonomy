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

- `seq` 是 run 内顺序：`0` = user 输入；`1..N` = thinking/tool 聚合行（按事件发生顺序）；
  assistant（最终返回）排在最后，为 `N+1`。无中间行时 assistant 仍为 `1`。
- `role`：`user` | `assistant` | `thinking` | `tool`。
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
  step               INTEGER NOT NULL DEFAULT 0,
  seq                INTEGER NOT NULL DEFAULT 0,   -- run 内顺序：user=0, 中间行 1..N, assistant=N+1
  role               TEXT    NOT NULL DEFAULT '',  -- user | assistant | thinking | tool
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
CREATE INDEX IF NOT EXISTS idx_llm_messages_task   ON llm_messages(task_id, step);
```

设计要点：

- 表结构属于 engine（方言/迁移不外泄）；上层只依赖 `Store` 暴露的 `LLMMessage` / `ReasonTurnHandle`。
- `UNIQUE(turn_id, seq)` + `INSERT OR IGNORE`：重复 finish 或回放不会产生重复消息。
- `content` 与 run header 的 `input / raw_output` **双写**（header 内容列**先保留**，见下）。

## 写入流程

```
BeginLLMTrace(agent, taskID, step, mode, input)
  → BeginReasonTurn：建 header，写 user 消息(seq=0)，返回 handle{TurnID, InputMessageID}
      ↓  Emit(ev) 逐条写入 llm_events
      ↓  run 结束
Finish(res)
  → FinishReasonTurn(handle, res)：更新 header，
       读回本 run 的 llm_events → 聚合 thinking/tool → 写中间消息(seq=1..N, parent_id=InputMessageID)
       写 assistant 消息(seq=N+1, parent_id=InputMessageID)
```

- **流式路径**（plan / agent 模式）走 `BeginLLMTrace` / `Finish`，见 `src/llm_trace.go`。
- **一次性路径** `InsertReasonTurn` 在同一事务里写 header + user + assistant 两条消息并互链
  （本地 reasoner 无原始流，故无中间行）。
- `Store.ListLLMMessages(turnID)` 按 `seq` 返回该 run 的消息。

## 聚合规则（从原始流 → 消息层）

中间行由纯函数 `aggregateChatMessages(events)`（`src/llm_message.go`）从本 run 的
`llm_events` 派生；聚合放在纯函数里、由 engine 调用，逻辑与数据库无关：

| 原始事件（`llm_events`） | 聚合为 | `content` | `normalized_content` |
|---|---|---|---|
| `thought`（连续的 `thinking` chunk 段） | **1 行**（`role=thinking`），把该段所有 `text_delta` 拼接 | 整段推理文本 | 空 |
| `tool`（同一 `call_id` 的 `running` 调用 + `completed` 返回） | **每个 `call_id` 1 行**（`role=tool`） | 工具返回（`result` 的 JSON） | `{name, args, call_id}` |
| `assistant`（逐 chunk） | **不产生行** —— 由 `reason_turns.raw_output` 写为最终 assistant 行 | 最终返回 | 归一化输出 |
| `status` / `meta` / `result` | 跳过（无对话文本） | — | — |

要点：

- 只聚合、不删原始：`llm_events` 仍保留逐事件（逐 token）全保真流水。
- 顺序按事件发生先后；assistant 永远是最后一行。
- `content` 是工具**返回**（结果），调用参数放 `normalized_content`。
- `parent_id` 统一指向本 run 的 user 行（一次 run 只有一条 user 输入，中间产物与返回都回答它）。

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
| 原始流 → 聚合消息（纯函数，DB 无关） | `src/llm_message.go` |
| 表结构 / 读写 / 聚合写入 / 回填 | `src/sqlite_store.go` |
| header + 消息写入编排 | `src/llm_trace.go` |
