# LLM Event Stream

## 目标

把每一次和 LLM 的交互**完整落库**：不仅有输入输出，还包括 provider 的 run stream 事件
（assistant 增量、tool 调用、thought、status、result …）。目标：

- 可回放：任意一次交互的完整时间线（prompt → 每个事件 → 终态）。
- 可分析：token/耗时/工具调用、失败原因可查询。
- **可扩展**：持久化层只依赖与 provider 无关的中立模型，接入新的 LLM 不改表、不改 trace。

## 数据模型

两张表，一个 header + 一条事件流：

```
reason_turns (1)  ────  run header：一次 LLM 交互的输入/输出/状态/用量
      │  id
      ▼
llm_events  (N)  ────  provider run stream：逐条原始事件（保真）
```

- `reason_turns` 既是原有「一次 reasoner 对话」记录，也是 run header。新增 run 级元数据列
  （`run_id`/`status`/usage/耗时 …）。
- `llm_events` 是 **新增** 子表，`turn_id` 指向 header，按 `seq` 排序，`payload` 保留 provider
  原始事件 JSON。

## DDL

```sql
-- run header（在原 reason_turns 上扩展；此处只列新增列）
ALTER TABLE reason_turns ADD COLUMN llm_agent_id       TEXT    NOT NULL DEFAULT '';
ALTER TABLE reason_turns ADD COLUMN run_id             TEXT    NOT NULL DEFAULT '';
ALTER TABLE reason_turns ADD COLUMN status             TEXT    NOT NULL DEFAULT '';
ALTER TABLE reason_turns ADD COLUMN error_code         TEXT    NOT NULL DEFAULT '';
ALTER TABLE reason_turns ADD COLUMN error_message      TEXT    NOT NULL DEFAULT '';
ALTER TABLE reason_turns ADD COLUMN duration_ms        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reason_turns ADD COLUMN event_count        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reason_turns ADD COLUMN input_tokens       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reason_turns ADD COLUMN output_tokens      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reason_turns ADD COLUMN cache_read_tokens  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reason_turns ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reason_turns ADD COLUMN reasoning_tokens   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reason_turns ADD COLUMN total_tokens       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reason_turns ADD COLUMN cost_cents         REAL;    -- 未知为 NULL
ALTER TABLE reason_turns ADD COLUMN started_at         TEXT;
ALTER TABLE reason_turns ADD COLUMN ended_at           TEXT;

CREATE INDEX IF NOT EXISTS idx_reason_turns_run ON reason_turns(run_id);

-- 事件流本体
CREATE TABLE IF NOT EXISTS llm_events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  turn_id      INTEGER NOT NULL DEFAULT 0,   -- → reason_turns.id
  run_id       TEXT    NOT NULL DEFAULT '',  -- provider run id（冗余，免 join）
  seq          INTEGER NOT NULL DEFAULT 0,   -- 一次交互内 0-based 单调递增，排序键
  offset_token TEXT    NOT NULL DEFAULT '',  -- provider 断点续传游标
  channel      TEXT    NOT NULL DEFAULT '',  -- 中立粗分类 assistant|tool|thought|status|result|error|meta
  event_type   TEXT    NOT NULL DEFAULT '',  -- provider 原始判别符，逐字保留
  role         TEXT    NOT NULL DEFAULT '',  -- user|assistant|tool|system
  name         TEXT    NOT NULL DEFAULT '',  -- 工具名 / step 名
  text_delta   TEXT    NOT NULL DEFAULT '',  -- 该事件携带的增量文本（便于廉价重放）
  payload      TEXT    NOT NULL DEFAULT '{}',-- provider 原始事件 JSON（全保真）
  elapsed_ms   INTEGER NOT NULL DEFAULT 0,   -- 相对 run 开始的毫秒
  created_at   TEXT    NOT NULL              -- 事件到达时间
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_llm_events_turn_seq ON llm_events(turn_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_run  ON llm_events(run_id, seq);
CREATE INDEX IF NOT EXISTS idx_llm_events_kind ON llm_events(turn_id, channel, event_type);
```

设计要点：

- `payload` 存原始 JSON，provider 新增事件类型时不丢字段、无需改表。
- `channel/event_type/role/name/text_delta` 是查询友好的冗余列，不参与语义判断。
- `UNIQUE(turn_id, seq)`：live stream 掉线后走 `WaitLiveRun` 回退时会重放，`INSERT OR IGNORE`
  保证不重复写同一条事件。
- `run_id` 在 run 开始时可能未知；`FinishReasonTurn` 会回填到此前写入的事件上。

## 写入流程

```
BeginLLMTrace(agent, taskID, step, mode, input)   → 建 header（status=running）
   ↓  每个 provider 事件
LLMTrace.Emit(LLMEvent)                            → 缓冲，按批写 llm_events
   ↓  run 结束
LLMTrace.Finish(LLMRunResult)                      → 回写 header（status/usage/时长/输出）+ 回填 run_id
```

- 事件按 `llmEventFlushSize`（默认 64）批量落库，`Finish` 时强制 flush。
- 落库是 **best-effort**：失败只打 stderr，绝不让模型调用失败。没有 store 时 trace 是安全 no-op。

### 开关：`AUTONOMY_LLM_EVENTS`

控制**是否落库原始事件流**（`llm_events`）：

| 取值 | 行为 |
|------|------|
| 未设置 / `1` / `true` / `on` / `yes` | **默认**：header + 全部原始事件都落库 |
| `0` / `false` / `off` / `no` / `disable` / `disabled`（大小写不敏感） | 只写 `reason_turns` run header，跳过 `llm_events`（不缓冲、不写入） |

关闭时 `reason_turns` 仍照常记录（它是一次 LLM 交互的既有记录，其它消费方依赖它），
`event_count` 保持 0。开关在 `BeginLLMTrace` 时读取一次。

代码位置：

| 关注点 | 文件 |
|--------|------|
| 中立事件模型 / provider 适配器注册 | `src/llm_event.go` |
| Cursor 事件映射 | `src/llm_event_cursor.go` |
| header + 事件写入编排 | `src/llm_trace.go` |
| 表结构 & 读写 | `src/sqlite_store.go` |
| 落库调用点 | `src/reasoner.go`（plan 模式）、`src/runtime.go`（agent 模式） |

## 扩展：接入新的 LLM

持久化层只认识 `LLMEvent`。接入新 provider 只需三步：

1. 实现 `LLMStreamAdapter`（`src/llm_event.go`）：把它自己的 native 事件映射成 `LLMEvent`。

   ```go
   type LLMStreamAdapter interface {
       Provider() LLMProvider
       MapEvent(native any, startedAt time.Time) (LLMEvent, bool)
   }
   ```

2. 注册：

   ```go
   func init() { RegisterLLMStreamAdapter(myAdapter{}) }
   ```

3. 在拿到 native 流的地方，把事件喂给 trace 或 `MapNativeLLMEvent(provider, native, started)`：

   ```go
   trace := BeginLLMTrace(agent, taskID, step, mode, prompt)
   // ... provider 流每来一个事件：
   if ev, ok := MapNativeLLMEvent(agent.LLMProvider, native, started); ok {
       trace.Emit(ev)
   }
   trace.Finish(LLMRunResult{ProviderRunID: id, Status: LLMStatusFinished, RawOutput: text})
   ```

`LLMProvider` 常量（`cursor` / `cline` / `deepseek_harness`）与 `channel` 分类是中立的；表结构不变。

## 保留策略

当前**不做清理**（先保留）。后续如需控制体积，可基于 `channel` 做策略，例如只在长期保留非
`assistant` 增量事件，或对 `text_delta` 做压缩/合并；届时再加清理任务与索引。
