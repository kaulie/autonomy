# Store & Engine

## 目标

把所有数据库操作收敛到一个**与具体数据库无关**的统一接口，具体后端（SQLite / Postgres /
MySQL …）只是可插拔的 **engine** 实现。切换数据库 = 注册并选择一个新 engine，上层代码不变。

- **统一接口**：`Store`（`src/store.go`）——上层唯一依赖的持久化契约。
- **引擎 SPI**：`StoreEngine`（`src/store_engine.go`）——一个 engine 对应一种数据库/方言。
- **内建引擎**：`sqlite`（`src/sqlite_engine.go` + `src/sqlite_store.go`），默认启用。

## 分层

```
调用方（autonomy / reasoner / llm_trace …）
        │  只依赖
        ▼
Store 接口（与数据库无关：UpsertTask / UpsertAgent / InsertReasonTurn / …）
        ▲  由 engine 提供实现
        │
StoreEngine（Name / DefaultDSN / Open）
        ├── sqliteEngine   （内建，默认）→ SQLiteStore
        └── 未来的 postgresEngine / mysqlEngine / …
```

关键不变式：

- **上层只认识 `Store`**，不认识 SQL、方言、表名；所有 DB 访问都经 `activeStore()`。
- **engine 独占其 schema 与迁移**：DDL、`ON CONFLICT`、`INSERT OR IGNORE`、占位符（`?`）、
  `PRAGMA` 等方言细节全部封装在 engine 内（见 `src/sqlite_store.go`）。
- **数据模型中立**：`LLMEvent` / `LLMUsage` / `ReasonTurn` 等模型本身与数据库无关，engine 不参与解释。

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
go run ./cmd/autonomy

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

`sqlite` engine 当前维护 `tasks` / `agents` / `reason_turns` / `llm_messages` / `llm_events` 五张表
（输入/返回消息见 [llm-message.md](llm-message.md)，原始事件流见 [llm-event-stream.md](llm-event-stream.md)）。
其它 engine 只需实现同样的 `Store` 语义，表结构可自由设计。

**失败必须落成可查的数据，而不是只留在终端上**：

- `tasks.status` 是结果，`tasks.error` 是原因：这一轮 **decide 失败**（`decide: ...`）或**最后一个 cycle 的
  action 失败**。写在「记下结果」的两处，不在起跑时写（见 [task.md](task.md)）。
- `reason_turns.status` / `error_code` / `error_message` 是每一轮 LLM run 自己的结果：provider 说
  `finished` 却一个字都没回也算**失败**（`status=error`），它自己那句话（如 `Insufficient Balance`）留在
  `error_message` 里 —— 否则一次没吐字的 run 在库里看起来是「跑完了」。
  **流里也有这句话**：provider 的 error 事件缺 message、或那句话落在请求窗口之后时，Cline bridge 会把它补回事件流
  （见 [llm-event-stream.md](llm-event-stream.md)），所以 `llm_events` 与 run header 说法一致；`llm_messages`
  只渲染「对话」（输入、思考、工具、run 的返回文本），不是 run 元数据的记录 —— 因此失败原因不在那里，模型没吐字时
  那一行 assistant 是空的，它只带 `status`。

## 代码位置

| 关注点 | 文件 |
|--------|------|
| `Store` 统一接口 | `src/store.go` |
| engine SPI + 注册表 + 选择 | `src/store_engine.go` |
| sqlite engine（默认 DSN / 注册） | `src/sqlite_engine.go` |
| sqlite 实现（DDL / 迁移 / 读写） | `src/sqlite_store.go` |
| 进程内单例与 bootstrap | `src/autonomy.go`（`activeStore()`，`BootstrapAutonomy`） |
