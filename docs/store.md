# Store & Engine

## 目标

把所有数据库操作收敛到一个**与具体数据库无关**的接口，具体后端（SQLite / Postgres /
MySQL …）只是可插拔的 **engine** 实现。切换数据库 = 注册并选择一个新 engine，上层代码不变。

- **统一接口**：`Store`（`src/store.go`）——持久化契约的并集，也是 engine 要实现的那一份。
- **六个端口**：`TaskStore` / `AgentStore` / `InboxStore` / `ConversationStore` / `ExecutionStore` /
  `VerificationStore`（同文件）——上层**按需**依赖，而不是整个 `Store`。
- **引擎 SPI**：`StoreEngine`（`src/store_engine.go`）——一个 engine 对应一种数据库/方言。
- **内建引擎**：`sqlite`（`src/sqlite_engine.go` + `src/sqlite_*.go`），默认启用。

## 分层

```
调用方（autonomy / llm_trace / verification / http 读接口 …）
        │  只依赖它用到的那一个端口
        ▼
TaskStore  AgentStore  ConversationStore  ExecutionStore  VerificationStore
        │                    └──────────┬──────────┘
        ▼                               ▼
   Store = 五个端口的并集（src/store.go）
        ▲  由 engine 提供实现
        │
StoreEngine（Name / DefaultDSN / Open）
        ├── sqliteEngine   （内建，默认）→ SQLiteStore
        └── 未来的 postgresEngine / mysqlEngine / …
```

谁依赖哪个端口，就是「它到底需要什么数据」的说明：

| 端口 | 内容 | 上层谁用 |
|------|------|----------|
| `TaskStore` | tasks | `persistTask`、HTTP 读接口（进度 / 停止 / 受理） |
| `AgentStore` | agents | `persistAgent`、`softDeleteAgent`、HTTP agent 状态 |
| `ConversationStore` | `reason_turns` 头 + `llm_messages` 对话 + `llm_events` 原始流 | `LLMTrace`（写）、HTTP 轮询（读） |
| `ExecutionStore` | 计划 / 计划步骤 / 执行步骤 / 交互 | `saveExecutionPlan` 等、verifier 读步骤产出、HTTP 进度 |
| `VerificationStore` | Completion Contract + verdict | `pinCompletionContract` / `pinnedCompletionContract` / `saveVerification` |
| `InboxStore` | 每个 agent 的消息队列（`agent_messages`） | `Inbox`（`src/inbox.go`）：入队 / claim / 收尾 / 回收 / 计数 |

端口只是**同一份 store 的切片**（`Store` 内嵌五个），所以 engine 一次实现全部、上层一次注入，
但没人需要认识与自己无关的那部分：加一个消费者不必看到 30 多个方法，加一个 engine 也能按
端口分块实现、分块验证。取用方式：

- 进程内：`activeTaskStore()` / `activeConversationStore()` / …（`src/store.go`）。
- 持有 `Autonomy` 时（HTTP 读接口）：`r.taskStore()` / `r.conversationStore()` / …。

## 边界是被测试钉住的

「上层不认识数据库」不只是约定，`src/store_ports_test.go` 会失败：

- **`TestOnlyTheStorageEngineMayImportADriver`**：遍历仓库里所有**非 engine 的生产 .go 文件**
  （engine 文件 = `sqlite_*.go`），任何一个
  1. import 了 `database/sql`、`modernc.org/sqlite`、`lib/pq`、`go-sql-driver/mysql`、`pgx` …，
  2. 或提到了 `SQLiteStore` / `OpenSQLiteStore` / `sqliteEngine` / `sql.Open(`，

  即视为越界 → 测试失败。driver 只能活在 engine 里，`sqlite_engine.go` 也不带 driver
  （它调用 `OpenSQLiteStore`）。
- **`TestTheUpperLayerWritesThroughItsPorts`**：用一个**不是 SQLite** 的 store（`recordingStore`）
  驱动全部上层写入（task / agent / run / plan+step+interaction / verdict / contract），
  断言它们只以端口调用的形式落地、字段与顺序不变。
- 编译期断言（同文件）：`SQLiteStore` 满足每一个端口与 `Store`。

关键不变式：

- **上层只认识端口**，不认识 SQL、方言、表名；所有 DB 访问都经 `activeStore()` 及其端口切片。
- **engine 独占其 schema 与迁移**：DDL、`ON CONFLICT`、`INSERT OR IGNORE`、占位符（`?`）、
  `PRAGMA`、以及「Go 字段 ↔ 列值」的编码（`src/sqlite_encoding.go`）全部封装在 engine 内。
- **数据模型中立**：`LLMEvent` / `LLMUsage` / `ReasonTurn` / `AgentMessage` 等模型本身与数据库无关，engine 不参与解释。
- **写者不止一个**：runtime 有多个 goroutine 在写（一轮自己的记录、inbox 消费者在跑同一条 task 的下一条消息）。
  engine 把 `_pragma=busy_timeout(10000)` 放进 DSN（对每条连接生效），让第二个写者**等锁**而不是拿到
  `SQLITE_BUSY`；`journal_mode=WAL` 让读者不被写者挡住。
- **表之间的引用是软链**：schema 里没有一条 `FOREIGN KEY`，链接写在列名与注释里（`llm_messages.turn_id`
  → `reason_turns.id`、`execution_step_interaction.reason_turn_id` → `reason_turns.id` …），因此没有级联。
  其中 `llm_events` 是**叶子**：只有出边、没有入边，删/重建它只影响原始事件回放
  （[llm-event-stream.md](llm-event-stream.md)，断言由 `TestNoOtherTableReferencesLLMEvents` 钉住）。


## 配置

| 环境变量 | 含义 | 默认 |
|----------|------|------|
| `AUTONOMY_STORE_ENGINE` | 选择 engine，按注册名（大小写不敏感） | `sqlite` |
| `AUTONOMY_STORE_DSN` | 覆盖 engine 的默认连接串（sqlite 即数据库文件路径） | 未设置时用 engine 的 `DefaultDSN()` |

`OpenDefaultStore()` 读取以上变量并分发到对应 engine：

- `sqlite` 的 `DefaultDSN()` = `$PROJECT_ROOT/data/autonomy.db`。
- 未设置 `AUTONOMY_STORE_DSN` 且 engine 无默认 DSN（如网络数据库）→ 报错，要求显式配置。

```bash
# 默认：sqlite @ $PROJECT_ROOT/data/autonomy.db
go run ./cmd/autonomyd

# 指定 engine / DSN
export AUTONOMY_STORE_ENGINE=sqlite
export AUTONOMY_STORE_DSN=/tmp/autonomy.db
```

## 扩展：接入一种新数据库

1. 实现 `StoreEngine`（`src/store_engine.go`）：

   ```go
   type StoreEngine interface {
       Name() string                   // 注册名，对应 AUTONOMY_STORE_ENGINE
       DefaultDSN() (string, error)    // 无默认值时返回 error
       Open(dsn string) (Store, error) // 连接 + 迁移，返回 Store
   }
   ```

   在 `Open` 里完成该数据库的连接与 **schema 迁移**（迁移属于 engine，不上浮）。

2. 实现 `Store` 的各个方法（可参考 `src/sqlite_store.go`）。

3. 注册并启用：

   ```go
   func init() {
       if err := RegisterStoreEngine(myEngine{}); err != nil {
           panic(err)
       }
   }
   ```

   ```bash
   export AUTONOMY_STORE_ENGINE=myengine
   export AUTONOMY_STORE_DSN="..."
   ```

`RegisterStoreEngine` 拒绝 nil / 空名 / 重复注册，让配置错误在启动时立即暴露；
`StoreEngineNames()` 可列出当前已注册的 engine。

## 现状表结构

`sqlite` engine 当前维护 `tasks` / `agents` / `reason_turns` / `llm_messages` / `llm_events` 五张表、
每个 agent 的收件箱 `agent_messages`（见 [inbox.md](inbox.md)），加上运行时的计划与执行四张表（`execution_plan` / `execution_step_plan` / `execution_step` / `execution_step_interaction`，见 [execution-step.md](execution-step.md)）
（输入/返回消息见 [llm-message.md](llm-message.md)，原始事件流见 [llm-event-stream.md](llm-event-stream.md)）。
其它 engine 只需实现同样的 `Store` 语义，表结构可自由设计。

`tasks` 行记「这条 Task 是什么」：`description` / `domain` / `goal_type` / `context_ref`（`{"容器类型": "容器 id"}` 的 JSON）
/ `status` / `error` / `agent_id`。`goal_type` 与 `context_ref` 是**受理时**写下的 —— `UpsertTask` 对它们的空值语义与
`agent_id` 一致：一次不带它们的写入（运行时沿途 upsert 的 Task 值、或只说得出 task id 的续指令）保留行里已有的值，
只有写入方真的给出了新值才覆盖（见 [task.md](task.md)、[http-api.md](http-api.md)）。

**失败必须落成可查的数据，而不是只留在终端上**：

- `tasks.status` 是结果（`running` / `completed` / `blocked` / `need_input` / `error` —— **收束这次运行的那个决策**），
  `tasks.error` 是原因：这一轮 **decide 失败**（`decide: ...`）或**最后一个 cycle 的 action 失败**；`blocked` /
  `need_input` 的原因在 `execution_plan.need` 里，不写进 `error`。写在「记下结果」的两处，不在起跑时写（见 [task.md](task.md)）。
- `reason_turns.status` / `error_code` / `error_message` 是每一轮 LLM run 自己的结果：provider 说
  `finished` 却一个字都没回也算**失败**（`status=error`），它自己那句话（如 `Insufficient Balance`）留在
  `error_message` 里 —— 否则一次没吐字的 run 在库里看起来是「跑完了」。
  **流里也有这句话**：provider 的 error 事件缺 message、或那句话落在请求窗口之后时，Cline bridge 会把它补回事件流
  （见 [llm-event-stream.md](llm-event-stream.md)），所以 `llm_events` 与 run header 说法一致；`llm_messages`
  只渲染「对话」（输入、思考、工具、run 的返回文本），不是 run 元数据的记录 —— 因此失败原因不在那里，模型没吐字时
  那一行 assistant 是空的，它只带 `status`。

## 外部读者与 schema 变更

这个库不只是 autonomy 自己在读：**别的服务按只读方式打开同一份文件**（`agent-benchmark-tool`
的 `benchmarkd` 就是长期挂着的一个，它的 `AUTONOMY_DB` 指向本库），而且往往一开就是好几天。

于是 in-place 改列名/删列有一个必须知道的爆炸半径：**已经打开库、把 schema 缓存下来的读者会当场报错**。

- `reason_turns.step → cycle`（见 `renameColumnIfPresent`，数据与索引一起改）就是这样：常驻的
  `benchmarkd`（11:04 启动时探测到的是 `step`）在 19:34 重命名之后，每个列表请求都变成
  `SQL logic error: no such column: r.step` → 页面 500，**直到它重启/重新部署**。
- 迁移本身照旧（engine 的事，见上），但改列名的人要按这个半径评估：**读者需要重新启动一次**。

对读者那一侧的规矩，写在这里免得下次再踩：

1. **schema 是运行期事实，不是编译期常量**：先 `PRAGMA table_info` 探测列再拼 SQL，
   不要假设列一定在（改名后的名字要认，旧名字也别急着摘）。
2. **对 "no such column" 这类错误重新探测并重试一次**，而不是把启动时的 schema 用到进程结束。
   否则源库的一次正常迁移就会让一个只读的旁观者永久 500。

## 代码位置

| 关注点 | 文件 |
|--------|------|
| `Store` 与五个端口（契约） | `src/store.go` |
| engine SPI + 注册表 + 选择 | `src/store_engine.go` |
| sqlite engine（默认 DSN / 注册） | `src/sqlite_engine.go` |
| sqlite 实现（DDL / 迁移 / 读写） | `src/sqlite_store.go`、`src/sqlite_query.go`、`src/sqlite_execution.go`、`src/sqlite_verification.go`、`src/sqlite_inbox.go` |
| sqlite 列值编码（时间 / NULL / JSON） | `src/sqlite_encoding.go` |
| 端口边界与上层可插拔性的测试 | `src/store_ports_test.go` |
| 进程内单例与 bootstrap | `src/autonomy.go`（`activeStore()`，`BootstrapAutonomy`） |
