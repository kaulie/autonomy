# LLM Messages

## 目标

把一次交互的「用户输入」和「LLM 返回」拆成**两条独立记录**：各自可被引用、查询、审计；
返回记录通过 `parent_id` **溯源**到它回答的那一条具体输入。

- 不再“一行同时代表输入和输出”，而是**一行 = 一条消息**。
- 返回 → 输入 的指向是显式外键（`assistant.parent_id → user.id`），不靠位置猜测。
- 与 provider 无关：中立模型先行，接入新 LLM 不改表。

## 数据模型

```
reason_turns (1)  ────  run header：provider/status/usage/耗时/run_id（内容列保留）
      │  id
      ├──▶ llm_messages (N) ── 两条独立记录：user 输入 + assistant 返回（parent_id 溯源）
      └──▶ llm_events   (N) ── provider 原始流（可选，见 llm-event-stream.md）
```

- `seq` 是 run 内顺序：`0` = user 输入，`1` = assistant 返回（为将来多轮留位）。
- `role`：`user` | `assistant`。
- `parent_id` 只出现在 assistant 行，指向它回答的 user 行 id —— 这条边就是「返回 → 输入」的溯源链。
- `content` 存逐字原文；`normalized_content` 是派生形式（例如从 ```json fence 中抽取的 JSON）。

## DDL

```sql
CREATE TABLE IF NOT EXISTS llm_messages (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  turn_id            INTEGER NOT NULL DEFAULT 0,   -- → reason_turns.id（属于哪次 run）
  task_id            TEXT    NOT NULL DEFAULT '',
  agent_id           INTEGER NOT NULL DEFAULT 0,
  step               INTEGER NOT NULL DEFAULT 0,
  seq                INTEGER NOT NULL DEFAULT 0,   -- run 内顺序：user=0, assistant=1
  role               TEXT    NOT NULL DEFAULT '',  -- user | assistant
  parent_id          INTEGER,                      -- assistant → 它回答的 user 消息 id（溯源）
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
      ↓  run 结束
Finish(res)
  → FinishReasonTurn(handle, res)：更新 header，写 assistant 消息(seq=1, parent_id=InputMessageID)
```

- **流式路径**（plan / agent 模式）走 `BeginLLMTrace` / `Finish`，见 `src/llm_trace.go`。
- **一次性路径** `InsertReasonTurn` 在同一事务里写 header + user + assistant 两条消息并互链。
- `Store.ListLLMMessages(turnID)` 按 `seq` 返回该 run 的消息。

## 保留：header 内容列不移除

`reason_turns.input / raw_output / normalized_output` **暂时保留**（双写）。原因：完全向后兼容，
既有消费方零改动。`llm_messages` 是可引用的独立记录；将来如需收敛为单一真源，再单独移除 header 内容列。

## 迁移 / 回填

- 打开数据库时自动建表与索引。
- 对**早于消息表**存在的 `reason_turns` 行做幂等回填：`input` → user 消息、`raw_output` → assistant
  消息（`parent_id` 互链）。只回填尚无消息的 run，重复打开不重复写入。

## 代码位置

| 关注点 | 文件 |
|--------|------|
| `Store` 契约 / `LLMMessage` / `ReasonTurnHandle` | `src/store.go` |
| 表结构 / 读写 / 回填 | `src/sqlite_store.go` |
| header + 消息写入编排 | `src/llm_trace.go` |
