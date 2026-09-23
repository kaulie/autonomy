# LLM Event Stream

## 目标

把每一次和 LLM 的交互**完整落库**：不仅有输入输出，还包括 provider 的 run stream 事件
（assistant 增量、tool 调用、thought、status、result …）。目标：

- 可回放：任意一次交互的完整时间线（prompt → 每个事件 → 终态）。
- 可分析：token/耗时/工具调用、失败原因可查询。
- **可扩展**：持久化层只依赖与 provider 无关的中立模型，接入新的 LLM 不改表、不改 trace。

> **默认不写原始流**：`llm_events` 一行一个 provider 事件（逐 token），而它是一张叶子表 —— 只服务回放，
> 没有别的表读它。所以默认只落 run header（`reason_turns`）与对话（`llm_messages`），
> `llm_events` 保持空表；需要回放/分析时显式打开 `AUTONOMY_LLM_EVENTS=1`（见 [开关](#开关autonomy_llm_events)）。
> 本文其余部分描述的都是**打开之后**的行为。

## 数据模型

三张表：一个 run header + 消息记录 + 一条事件流：

```
reason_turns (1)  ────  run header：provider/status/usage/耗时/run_id（内容列保留）
      │  id
      ├──▶ llm_messages (N) ── user 输入 + assistant 返回两条独立记录（parent_id 溯源）
      └──▶ llm_events   (N) ── provider run stream：逐条原始事件（保真）
```

- `reason_turns` 既是原有「一次 reasoner 对话」记录，也是 run header。新增 run 级元数据列
  （`run_id`/`status`/usage/耗时 …）。
- `llm_messages` 是 **新增** 子表，把「用户输入」和「LLM 返回」拆成两条独立记录，返回通过
  `parent_id` 溯源到具体输入 —— 详见 [llm-message.md](llm-message.md)。
- `llm_events` 是 **新增** 子表，`turn_id` 指向 header，按 `seq` 排序，`payload` 保留 provider
  原始事件 JSON。**默认不写**（`AUTONOMY_LLM_EVENTS=1` 才写）：它是叶子表，回放是它唯一的下游。

## 引用关系：`llm_events` 是一张叶子表

「它是独立表吗，其他表有引用吗」两个方向都答一遍，而两个方向都没有 `FOREIGN KEY`：

```
reason_turns                llm_events                 其它表
     ▲                          │
     └── turn_id, run_id ───────┘                     agents / tasks / llm_messages /
         （软链，无 FK）                                 execution_* / completion_contract /
                                                        verification / agent_messages …
   出边：有（指向 reason_turns）   入边：没有（没有任何表指向 llm_events）
```

- **出边（它引用谁）**：`llm_events.turn_id → reason_turns.id`；`run_id` 是 header 上 `run_id` 的**冗余副本**
  （省一次 join，`FinishReasonTurn` 在 run 结束时回填）。两条都是**软链**——schema 里没有任何
  `FOREIGN KEY`，所以即使 `PRAGMA foreign_keys = ON` 也管不到它们：删掉 header **不会**级联删掉它的
  `llm_events` 行，反过来也一样（见 [保留策略](#保留策略)）。
- **入边（谁引用它）**：**没有**。整份 schema 一条 `FOREIGN KEY` 都没有，也没有哪张表带
  `llm_events_id` / `event_id` 这样的列。唯一认识它的代码是 `ConversationStore` 端口
  （`AppendLLMEvents` / `ListLLMEvents`）：写方只有 `LLMTrace`，读方是 store 自己的聚合兜底
  （`FinishReasonTurn` 的再聚合、`backfillReasonTurnMessages`）。HTTP 读接口目前只读
  `llm_messages`，本表还没有对外出口。
- **行为上也独立**：`AUTONOMY_LLM_EVENTS` **默认关**，整张表默认不写（`=1` 才写），`reason_turns` 头与 `llm_messages`
  照常（见下文开关），说明没有别的表把「这次 run 发生过什么」寄托在 `llm_events` 上。
- **所以**：换 / 删 / 重建 / 清空 `llm_events` 只影响原始事件流的回放，不影响任何别的表的完整性；
  它自身的完整性也**没有任何人保证**——没有级联、没有清理任务、`turn_id` 指向一行已不存在的 header 时也没有报错。

自查任意一个库（两条都应为空）：

```sql
-- 入边：指向 llm_events 的外键
SELECT m.name, fk."from" FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) fk
WHERE m.type = 'table' AND fk."table" = 'llm_events';
-- 入边：形如 llm_events_id / event_id 的列
SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p
WHERE m.type = 'table' AND m.name <> 'llm_events'
  AND (lower(p.name) LIKE '%llm_event%' OR lower(p.name) LIKE '%event_id%');
```

这条断言是**有测试钉住的**：`TestNoOtherTableReferencesLLMEvents`
（`src/llm_event_stream_test.go`）——将来谁真的要加一条入边，得先把这个测试改掉。

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
  kind         TEXT    NOT NULL DEFAULT '',  -- 中立细分类（规范）assistant_delta|tool_call_completed|…
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
CREATE INDEX IF NOT EXISTS idx_llm_events_turn_kind ON llm_events(turn_id, kind);
```

设计要点：

- `payload` 存原始 JSON，provider 新增事件类型时不丢字段、无需改表。
- `channel/event_type/role/name/text_delta` 是查询友好的冗余列，不参与语义判断。
- `UNIQUE(turn_id, seq)`：live stream 掉线后走 `WaitLiveRun` 回退时会重放，`INSERT OR IGNORE`
  保证不重复写同一条事件。
- `run_id` 在 run 开始时可能未知；`FinishReasonTurn` 会回填到此前写入的事件上。

## 写入流程

```
BeginLLMTrace(agent, taskID, cycle, mode, input)  → 建 header + 写 user 消息（status=running）
   ↓  每个 provider 事件
LLMTrace.Emit(LLMEvent)                            → 缓冲，按批写 llm_events
   │                                               ＋ 边聚合边写 llm_messages（消息一完成就落库 + 打日志）
   ↓  run 结束
LLMTrace.Finish(LLMRunResult)                      → 冲掉未闭合的聚合行 + 回写 header + 写 assistant 消息（link input）+ 回填 run_id
```

- 事件按 `llmEventFlushSize`（默认 64）批量落库，`Finish` 时强制 flush；**原始流默认关闭**，
  此时 `Emit` 不缓冲、不写表。
- `llm_messages` **始终写**（user 输入、thinking/tool 聚合行、assistant 返回），而且是**边跑边写**：
  长 run 中途就能查表/看日志，不必等 `Finish`（见 [llm-message.md](llm-message.md)）。
  `AUTONOMY_LLM_EVENTS` 只控制 `llm_events`（默认不写，需显式打开）。
- 落库是 **best-effort**：失败只打 stderr，绝不让模型调用失败。没有 store 时 trace 是安全 no-op。
- 日志就是"消息"：每条 `llm_messages` 落库行打一行 `[autonomy] llm seq=… <role> …`
  （`src/llm_message_log.go`，`AUTONOMY_LLM_TRACE=0` 可关，`AUTONOMY_LLM_TRACE_MAX` 限宽）；
  原始事件流不进日志，provider 桥各自的 trace 是 opt-in（如 `AUTONOMY_CLINE_TRACE=1`）。

### 开关：`AUTONOMY_LLM_EVENTS`

控制**是否落库原始事件流**（`llm_events`）：

| 取值 | 行为 |
|------|------|
| 未设置 / `0` / `false` / `off` / `no` / `disable` / `disabled`（大小写不敏感） | **默认**：只写 `reason_turns` run header 与 `llm_messages`，跳过 `llm_events`（不缓冲、不写入） |
| `1` / `true` / `on` / `yes` / `enable` / `enabled` | 打开原始流：header + 全部 provider 事件都落库 |

关闭（默认）时 `reason_turns` 与 `llm_messages` 仍照常记录（前者是 run header，后者是输入/返回记录，其它消费方依赖），
`event_count` 保持 0。开关在 `BeginLLMTrace` 时读取一次——一次 run 的中途改环境变量不会影响它。

「独立表」这件事因此是**默认行为**，不是特例：平时就没有人在写 `llm_events`，打开它只为回放/分析
（见 [引用关系](#引用关系llm_events-是一张叶子表)）。

代码位置：

| 关注点 | 文件 |
|--------|------|
| 中立事件模型 / provider 适配器注册 | `src/llmbackend/events.go` |
| Cursor 事件映射 | `src/llmbackend/events_cursor.go` |
| Cline 事件映射 | `src/llmbackend/events_cline.go`（见 [cline-reasoner.md](cline-reasoner.md)） |
| header + 事件写入 / 边跑边写的消息编排 | `src/llm_trace.go` |
| 事件流 → 消息聚合 | `src/llm_message.go`（见 [llm-message.md](llm-message.md)） |
| 每行消息 → 日志 | `src/llm_message_log.go` |
| 表结构 & 读写 | `src/sqlite_store.go` |
| 落库调用点 | `src/reasoner.go`（plan 模式）、`src/runtime.go`（agent 模式） |

## 扩展：接入新的 LLM

持久化层只认识 `LLMEvent`。接入新 provider 只需三步：

1. 实现 `llmbackend.streamAdapter`（`src/llmbackend/events.go`）：把它自己的 native 事件映射成 `LLMEvent`。

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
   trace := BeginLLMTrace(agent, taskID, cycle, mode, prompt)
   // ... provider 流每来一个事件：
   if ev, ok := MapNativeLLMEvent(agent.LLMProvider, native, started); ok {
       trace.Emit(ev)
   }
   trace.Finish(LLMRunResult{ProviderRunID: id, Status: LLMStatusFinished, RawOutput: text})
   ```

## provider 中立契约：`kind` + 规范 payload 键

`llm_events` 每一行都有三层表达，**新增 provider 只需实现一个 `LLMStreamAdapter`**：

| 层 | 列/字段 | 语义 |
|---|---|---|
| 粗粒度 | `channel` | `assistant / thought / tool / status / result / error / meta` |
| **细粒度（规范）** | **`kind`** | `assistant_delta`、`assistant`、`thought_delta`、`thought`、`thought_end`、`tool_call_started`、`tool_call_delta`、`tool_call_completed`、`status`、`usage`、`run_result`、`error`、`meta` |
| 原文（逐字保留） | `event_type` + `payload` | provider 自己的判别符与字段，一个都不改 |

`kind.Family()` 把粒度变体归到同一语义（**消费方不必关心流式粒度**）：

```
assistant  ← assistant, assistant_delta
thought    ← thought, thought_delta, thought_end
tool_call  ← tool_call_started, tool_call_delta, tool_call_completed
```

**规范 payload 键**（与 provider 原生键并存，原生键永不改名/丢弃）：

| 键 | 含义 | Cursor 来源 | Cline 来源 |
|---|---|---|---|
| `text` | 事件自身的文本（增量或整块） | `text` / `message.content[].text` | `text` / `accumulated` / `reasoning` |
| `duration_ms` | 耗时（可选：provider 上报时填） | `thinking_duration_ms`（思考）、工具耗时 | 工具 `durationMs`；思考由聚合层按事件跨度推导 |
| `call_id` | 工具调用 id | `call_id` | `toolCallId` |
| `name` | 工具名 | `name` | `toolName` |
| `args` / `result` | 工具入参与输出 | `args` / `result` | `input` / `output` |
| `status` | 运行状态；工具事件为 `running/completed/failed` | `status` | 由事件类型归一（`content_start`→running、`content_end`→completed） |
| `stream` / `chunk` | 流式工具输出分片 | — | `update.stream` / `update.chunk` |
| `input_tokens` / `output_tokens` / `cache_read_tokens` / `cache_write_tokens` / `total_tokens` | token 用量 | `usage.*` | `inputTokens` / `outputTokens` / `cacheReadTokens` … |
| `cost_usd` | 成本（**USD**，仅在 provider 上报 USD 时填） | —（Cursor 流不含成本） | `totalCost` / `cost` |

**粒度差异是 provider 能力差异，不是语义差异**（有意不抹平）：Cursor 的思考/回答是**整块**（一条消息 + 上报时长），Cline 是**逐 token 增量** + 块结束标记；思考块结束事件把全文放在 `payload.text`，但**不重复写入 `text_delta`**（否则聚合出的 thinking 消息会翻倍）。

**失败的原因必须进流，不能在别处**：一次 run 失败时，provider 的那句话（`Insufficient Balance` …）可能只出现在 run result 里 —— SDK 的 error 事件本身可能是 `{"error":{}}`，或者那句话作为「run 的最后一个事件」落在请求窗口关闭之后（客户端按 request 关联事件，窗口外的会被丢掉）。Cline bridge 因此做两件事（`src/clinesdk/bridge/config.mjs`：`errorReason` / `withErrorReason` / `runResultErrorEvent`）：

1. 转发的 error 事件先被补齐 `error.message`（`error ?? message` 会优先选那个空对象，所以按「谁真的说了话」挑）；
2. result 里有、但流里没送达的原因，在**返回 result 之前**作为一条 error 事件补进本次请求窗口，payload 标 `"source":"run_result"` —— 于是 `llm_events` 与 `reason_turns.error_message` 说法一致，UI 的事件视图也看得到。

**一致性由测试兜底**：`src/llm_event_contract_test.go` 是契约的**可执行版本** —— 表里每个语义都要求两个后端给出同一 `family` + 同一 `channel` + 同一批规范键。新增 provider 时照表补样例即可，漂移会直接测挂。

查询示例：

```sql
-- 每个 run 的思考增量与工具调用（不看 provider 差异）
SELECT kind, count(*) FROM llm_events WHERE turn_id = ? GROUP BY kind;
-- 某次工具调用的入参/结果（两个后端字段名一致）
SELECT kind, json_extract(payload,'$.name'), json_extract(payload,'$.args'), json_extract(payload,'$.result')
FROM llm_events WHERE json_extract(payload,'$.call_id') = ? ORDER BY seq;
```

## 后端对齐：thinking / 状态事件契约

两个后端在**网关层**已经统一（web-cursor 的 `providers/{cursor,cline}/mapper.js` 都把各自的思考事件映射成统一的
`thinking` 事件），在 **autonomy 层**同样对齐：落库的 `llm_events` 与聚合出的 `llm_messages` 形状一致。

| 语义 | Cursor 原生 | Cline 原生 | autonomy 中立事件 | 下游怎么用 |
|---|---|---|---|---|
| 思考中（增量） | SDK `thinking` 消息（**整块**文本 + `thinking_duration_ms`） | `agent_event content_start/update:reasoning`（逐 token 增量） | `channel=thought`，`TextDelta`=思考文本 | 出现 thought delta 即显示"思考中" |
| 思考块结束 | 同一条 `thinking` 消息 | `agent_event content_end:reasoning`（重复整块文本；`TextDelta` 置空以免重复） | `channel=thought`，`event_type=agent_event:content_end:reasoning` | 结束该思考块 |
| 思考耗时 | payload `thinking_duration_ms`（SDK 上报） | 由首个→最后 reasoning 事件的跨度推导 | `llm_messages.normalized_content = {"duration_ms":N}`（聚合层统一，优先用上报值） | "思考了 Xs" |
| 运行状态 | SDK `status` 消息（status/message） | core `status` + `agent_event notice` | `channel=status`（payload `status`/`message`） | spinner / 错误文案 |
| 最终回答 | SDK `assistant` 文本 | `agent_event content_end:text` / `done` | run header `raw_output` + `llm_messages` 的 assistant 行 | 结果渲染 |
| 工具调用 | SDK `tool_call` | `agent_event content_*:tool` | `channel=tool`，payload 含中立键 `call_id`/`args`/`result`（+ stdout `chunk`） | 工具卡片 / 调用-结果合并 |
| 用量与成本 | SDK `usage` | `agent_event usage` + run header | `reason_turns` 的 token/cost 列（cline 含 cacheRead/cost，常驻 send 用累计差值） | 计费/统计 |

**粒度差异（已归一）**：Cursor 一条消息给整块思考，Cline 给逐 token 增量，块结束时重复整块文本。聚合层因此
把连续 thought 事件拼成 **1 条 thinking 消息**，并屏蔽 `content_end:reasoning` 的重复文本（否则内容会翻倍）。

**尚未打通的一环**：autonomy 目前只把这些事件**落库**（`llm_messages`，以及打开开关后的 `llm_events`），还没有面向 UI 的实时
出口（SSE/WebSocket）。UI 要从 autonomy 实时显示"还在 thinking"，需要补这个出口（或在 UI 侧读库）。这属于
"core 迁到 autonomy" 的下一步，见 `docs/cline-reasoner.md` 的边界小节。

`LLMProvider` 常量（`cursor` / `cline` / `deepseek_harness`）与 `channel` 分类是中立的；表结构不变。

## 保留策略

原始流**默认就不写**（`AUTONOMY_LLM_EVENTS` 未设置），所以这张表默认不再增长：写入量最大的 `assistant`
逐 token 增量不会落库，只有显式打开回放/分析时才有数据。

已经写下的历史行当下**不做清理**（先保留）。因为它是叶子表 —— 没有入边、没有级联、没有别的消费者
（见 [引用关系](#引用关系llm_events-是一张叶子表)）——清空它只需要 `DELETE FROM llm_events`（或
`VACUUM` 回收空间）：`reason_turns` / `llm_messages` 与其余表都不受影响，只是这些旧 run 不再有逐事件回放。
后续如需进一步控制体积，可基于 `channel` 做策略，例如只在长期保留非 `assistant` 增量事件，或对
`text_delta` 做压缩/合并；届时再加清理任务与索引。
