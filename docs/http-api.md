# HTTP API

Autonomy 对外的 HTTP 接口，两半：**任务接口**（受理指令、查进展、查 agent 状态、轮询对话流）与
**数据接口**（把日志当数据读：列表 / 详情 / facets / task 选择器 / 自述，给评测侧用，见
「[数据 API](#数据-api评测侧读日志不再读库)」）。契约本身由代码里的注解生成（见文末「服务契约」）。部署平台按服务契约调用 `scripts/restart.sh`：注入 `SERVICE_PORT`（优先于 `PORT`）、`RUNTIME_DIR`、`APP_VERSION`，契约里 autonomy 的 port 是 `4300`，探活 `GET /health`。

发版包（`build.sh`）自带两份东西，部署上直接能用：runtime 本体 `bin/autonomyd`，以及 cursor bridge `bin/cursor-sdk-bridge`（`third_party/` 是 gitignore 的下载产物，不带它的话部署上的 llm 任务会失败在 `cursor bridge ping`；`scripts/start.sh` 会把它指给 `CURSOR_SDK_BRIDGE_BIN`）。

本地：

```bash
./build.sh
RUNTIME_DIR="$(pwd)/outputs" bash outputs/scripts/start.sh
```

不走脚本时用 `go run ./cmd/autonomyd`（`AUTONOMY_HTTP_ADDR` 未设置则监听 `:4300`，即契约里的端口）——这就是 runtime 本身：
进程里装着库、store 与 agent。`cmd/autonomy` 是它的**客户端**，不链接 runtime 的任何代码，
只用下面这些 HTTP 调用：

```bash
go run ./cmd/autonomy -description "开放服务契约的前端入口"     # POST /api/tasks，再轮询进展（-wait 默认开）
go run ./cmd/autonomy -task task-28 -description "接着上次那条"  # 同一个 task 再给一条指令
go run ./cmd/autonomy -task task-28 -progress                    # GET /api/tasks/{id}，看一眼
go run ./cmd/autonomy -task task-28 -stop                        # POST /api/tasks/{id}/stop
go run ./cmd/autonomy -broadcast project-749a0238 -description "上线窗口挪到今晚"  # POST /api/broadcast
go run ./cmd/autonomy -broadcast all -description "今天 18:00 全员停服演练"        # 所有 project
```

默认对着 `http://127.0.0.1:4300`（契约里 autonomy 的端口；`-server` / `AUTONOMY_API_URL` 可改，本地手起的 runtime 用它指过去）。不给任何参数时发的是演示指令
（task 默认 `task-28`，`context_ref` 默认 `project=project-2` —— runtime 启动时播种的那个世界，见 `cmd/autonomyd/world.go`）。
命令的退出码就是这个任务的状态（`-progress` 是它读到的那个）：`error` / `unverified` 非零，其余为 0；
`-json` 打印 API 原样的响应。

## 端点

### `GET /health`

部署平台对每个服务统一探的路径。`GET /healthz` 是同一处理函数的别名。

### `POST /api/tasks`

接受一条任务指令：立刻返回 `task_id` / `agent_id`，并把这条指令作为**消息**放进这只 agent 的 inbox
（`instruction`，见 [inbox.md](inbox.md)）。agent 正忙也不会被拒 —— 消息排队等它（这就是「指令可以持续接收」）。
指令由 agent 自己的消费者处理，HTTP 请求不被锁住。

```json
{
  "description": "开放服务契约的前端入口",
  "domain": "software_development",
  "goal_type": "dev_feature",
  "task_id": "optional-client-id",
  "context_ref": { "project": "project-2" }
}
```

响应 `202`:

```json
{ "task_id": "task-…", "agent_id": 10001, "status": "pending", "message_id": 7, "queued": 2 }
```

`message_id` 是这条指令在 inbox 里的消息 id；`queued` 是这只 agent **前面**还有几条没处理完的消息（`queued` 或正在处理，
**含正在跑的那条**）：`0` = 这条就是它正在做或马上要做的，`n` = 前面还有 n 条要先过。

> **id 的区间**：`message_id`（`agent_messages.id`）与对话流的 `message_seq`（`llm_messages.id`）从
> **1000000** 起（`MessageIDBase`），agent id 从 **10000** 起（`AgentIDBase`）。消费者（比如控制面的时间线）
> 自己也从 1 编号时，两边的 id 放同一张表也分得清，不必自己再映射一层；这是一条**下限**，不是迁移 ——
> 老库里已有的 id 原样保留，重开库只会把序列往上调、不会倒退（见 [store.md](store.md)）。

同一个入口也有进程内的同步版本：`Autonomy.Run(AcceptTaskRequest)`（测试用它）。它走的是同一条 accept 路径（同一个 task、
同一条指令消息、同一只 agent），区别只在于它会等这次运行结束才返回，返回这次运行的错误和同一份接受信息
（`*AcceptTaskResponse`）。命令行客户端（`cmd/autonomy`）不链接 runtime，它是这条路线的 HTTP 版本：
`POST /api/tasks` 拿到接受信息，再用 `GET /api/tasks/{id}` 轮询到状态不再是 `running` / `pending`（`-wait`，默认开），
运行的结局就是命令的退出码。请求里的 `description` 为空时，指令内容取这条 task 行已有的描述（`tasks.description`）；
`goal_type` / `context_ref` 同理：不带就用行里已有的（`tasks.goal_type` / `tasks.context_ref`，见 [task.md](task.md)）。

### `POST /api/broadcast`

一次广播：把**同一句话**投递给一批 agent —— **某个 project 下面的**所有 agent，或**所有 project 下面的**所有 agent
（概念与不变式见 [broadcast.md](broadcast.md)）。

它不新造投递机制：每个目标收到的消息与 `POST /api/tasks` 投给它的**逐字段相同**（`user` 发的 `instruction`，
由那只 agent 自己的消费者按顺序处理，忙就排队），区别只是这次一次说给多个目标听，并每个目标答一行结果。

```json
{ "content": "上线窗口挪到今晚 20:00", "project_id": "project-749a0238" }
{ "content": "今天 18:00 全员停服演练", "all_projects": true }
```

| 字段 | 含义 |
|------|------|
| `content` | 说的话本身（必填），逐字成为每个目标那一轮运行的输入 |
| `project_id` | 只发给这个 project 的 agent（= 这个 project 的 task 的 owner agent） |
| `all_projects` | 发给所有 project 的 agent（要说出来，不能靠省略猜） |

`project_id` 与 `all_projects` **二选一**：两个都给，或都不给（没说清范围），返回 `400`；`content` 为空同样 `400`。

响应 `200`：`scope`（`project` / `all`）+ 计数 + **每个目标一行** `deliveries[]`：

```json
{
  "scope": "project", "project_id": "project-749a0238",
  "targets": 3, "delivered": 2, "skipped": 1, "failed": 0,
  "deliveries": [
    { "task_id": "task-a", "agent_id": 10001, "project_id": "project-749a0238",
      "status": "delivered", "message_id": 42, "queued": 0 },
    { "task_id": "task-b", "status": "skipped", "reason": "the task has no agent" }
  ]
}
```

- `status: delivered` 时 `message_id` / `queued` 与 `POST /api/tasks` 的答复同义（这条消息在队里的 id，以及它前面还有几条）；
- `status: skipped` 表示这个目标投不了，`reason` 是目标自己的事实：那条 task 还没有 agent，或它的 agent 已经被 let go
  —— 不为了一次广播去复活它；
- `status: failed` 表示这次投递失败（`reason` 是 runtime 自己的话），**其余目标不受影响**。

广播不等运行：消息进队就返回，各只 agent 各自按顺序处理（要跟进用 `GET /api/tasks/{id}`，要停用 `POST /api/tasks/{id}/stop`）。

### `POST /api/tasks/{task_id}/stop`

停掉这个 agent 正在处理的那条消息（planner 循环在当前 decide 或当前 cycle 的事件等待处退出），状态记为 `stopped`；
停止本身也记成一条来自 `system` 的消息，排在它停下的那条之后。队列里还没处理的消息不会被丢掉 —— 那是 agent 接下来要做的
（见 [inbox.md](inbox.md)）。

- `200`：`{"task_id","status":"stopped"}`
- `404`：没有这条 task
- `409`：task 不在运行中

### `GET /api/tasks/{task_id}`

查询任务详情：**这条 task 是什么** + 进展。

「是什么」来自行本身（`tasks.goal_type` / `tasks.context_ref`，见 [task.md](task.md)）加上把那层引用解析出来的结果：

| 字段 | 含义 |
|------|------|
| `goal_type` | 受理时的目标类型（`tasks.goal_type`） |
| `context_ref` | 它引用的世界，如 `{"project": "project-749a0238"}`，或 `{"task": "task-2ecd5e15ae3047f0"}`（跟着那条 task 的世界） |
| `project` | **所属 project**：`{"id", "name", "description", "domain", "git_repo_url", "organization"}`。`git_repo_url` 这个键**始终在**（知道就是仓库地址，不知道就是空串）：平台的 project 注册表已不再提供仓库，autonomy 在这条路上**不推导**它 —— 一条 task 的仓库属于它的**世界**（组织 → 服务中心的服务清单，每个服务带自己的 repo），不属于 project 行。 |
| `project.organization` | **project 所属的组织（部门）**：`{"id": "D0005", "name": "AI研发部"}` |

`project` 由两处合成，都不强求：runtime 自己注册过的 context container 提供它知道的（`name` / `description` / `domain`），
平台的 **project 注册表**（控制面 `GET /api/projects`，`PROJECTS_API_URL` 覆盖，默认 `http://127.0.0.1:4211`）提供 project 的名字、
仓库与**所属部门** —— 一个 project 属于哪个组织是平台的事实，autonomy 只读、不另立一份注册表。注册表读不到（未启动/超时 2s）
不影响这个接口：详情照常返回，`project` 只剩 id（和本进程世界知道的那点信息），不会因为一个注册表挂了而失败。注册表结果缓存 30s
（失败缓存 5s），所以每次读详情不会真的每次都去问。

`context_ref` 指向的是一条 **task** 时，`project` 是**那条 task 的** project：本进程先按 `GetTask` 读它的行（world 写在行里），
本进程没有那条 task 时问平台的 **task 注册表**（控制面 `GET /api/tasks/{taskId}`，`TASKS_API_URL` 覆盖，默认同控制面）——
和决策周期用的是同一次解析（[context-builder.md](context-builder.md)），所以详情与 prompt 答案一致。

进展：状态、错误，以及每一份 execution plan 和其中每一步的执行情况。

`plans[].steps[]` 按计划顺序：`status` 为 `pending`（计划了还没跑）、`ok` 或 `failed`。已跑的步带上实际 `input` / `output` / `error`。

### `GET /api/tasks/{task_id}/agents/{agent_id}`

查询该 agent 的工作状态。若正在工作（有 `reason_turns.status=running`，或 `agents.state=running`）：

- `working: true`
- `agent_run_id`：当前 provider run id（`reason_turns.run_id`；流式中段可能仍为空，此时看 `turn_id`）

### `GET /api/tasks/{task_id}/agents/{agent_id}/events?last_synced_message_seq=N`

按 `llm_messages.id` 做跨 turn 的单调游标轮询增量对话流（thinking / tool / assistant 等）。

- 请求参数 `last_synced_message_seq`：上次同步到的 `message_seq`（即 `llm_messages.id`）；首次传 `0`
  （`0` 只是"从头拉"，不是消息 id —— 消息 id 从 `MessageIDBase` = 1000000 起）
- 响应里每条事件的 `message_seq` 供下次轮询；`turn_seq` 是 run 内序（每次 run 从 0 起）
- **工具调用**：一次调用是一行（`role: "tool"`），**结果在 `content`**，**调用在 `normalized_content`**
  （`{"name": …, "call_id": …, "args": {…}}`，见 [llm-message.md](llm-message.md)）；thinking 行的
  `normalized_content` 是 `{"duration_ms": …}`。想渲染「tool(名, 入参) → 结果」两个字段都要读。
  注意 tool 行是**调用 + 结果合并后**写的（结果到了这行才算完整），所以没有单独的 "tool_call_started" 事件。
- **每一轮 run 的头跟着这一页走**：`turns[]` 是这一页事件涉及到的那些 turn 的 header
  （去重、按 turn 序），一行一个 run —— `cycle` / `mode`（plan / agent）/ `model` / `provider` / `run_id` /
  `status`（`running` / `finished` / `error`）/ `duration_ms` / `error_code` / `error_message` /
  `started_at` / `ended_at`。**run 的收尾读这里**，不要从"最后一条消息"猜：一条 run 的终态是它自己的
  header（`reason_turns`）。run 的头读不到时这一行只是缺席，不影响这一页事件。
- `role` 的取值：`user`（用户/运行时投递的输入）、`agent`（另一个 agent 委派来的输入）、
  `thinking`、`tool`、`assistant`（这一轮的返回）。**没有** `status` / `plan` / `decision` 行：
  决策与计划是 `execution_plan` + `reason_turns.raw_output`（`agent` 轮的返回文本就是那份决策 JSON），
  要显示 `decision.type` / `reason` 请读任务详情（`plans[]`）。
- `404`：没有这条 task，或没有这只 agent；`agent` 存在但它属于别的 task → `400`（与上一条同义）。

```json
{
  "task_id": "task-…",
  "agent_id": 10001,
  "events": [
    {
      "message_seq": 42,
      "turn_id": 7,
      "turn_seq": 1,
      "cycle": 1,
      "role": "tool",
      "content": "[{\"exit\":0}]",
      "normalized_content": "{\"name\":\"shell\",\"call_id\":\"call-1\",\"args\":{\"cmd\":\"ls\"}}",
      "model": "composer-2",
      "provider": "cursor",
      "run_id": "…",
      "status": "running",
      "created_at": "…"
    }
  ],
  "turns": [
    {
      "turn_id": 7, "cycle": 1, "mode": "plan", "model": "composer-2", "provider": "cursor",
      "run_id": "…", "status": "finished", "duration_ms": 1234,
      "started_at": "…", "ended_at": "…", "created_at": "…"
    }
  ],
  "last_message_seq": 42,
  "next_poll_after_seq": 42
}
```


## 数据 API（评测侧读日志，不再读库）

`agent-benchmark-tool` 原先以 `mode=ro` 直接挂 autonomy 的 SQLite 文件取数，于是库路径与 schema 成了两边的
耦合点。2026-09-20 就发生过一次：benchmarkd 攥着已被归档的旧库 inode，页面照旧显示 236 条旧数据，而
autonomy 真正在写的是 12 条新数据，两边都不报错。以下端点把这层收口到 HTTP —— **库在哪、列叫什么、
agent 名字怎么 join 由 autonomy 负责**，评测侧只认服务地址（`AUTONOMY_API_URL`，默认
`http://127.0.0.1:4300`）。

需求原文（字段级契约）在 benchmark 仓库的 `docs/autonomy-api.md`；本节是 autonomy 侧的落地说明。

统一约定：

| 约定 | 说明 |
|---|---|
| 只读 | 全部是 `GET`，不写任何数据、不触发迁移；读的是 `TurnQueryStore`（[store.md](store.md)）这一个只读端口 |
| JSON / UTF-8 | `Content-Type: application/json; charset=utf-8`；错误是 `{"error": "..."}`（与既有 `errResponse` 一致） |
| 时间 | 库里是什么就透出什么（UTC RFC3339 文本），不换算、不截断精度 |
| 空结果 | `200` + 空数组（不是 404，也不是 `null`） |
| 未知参数 | 忽略，不报错；读不出来的 `limit` / `offset` 当没给 |
| 读不了日志 | `500 {"error":"store not ready"}` —— 「没有数据」和「读不到数据」不能在页面上长得一样 |
| 版本自述 | `GET /api/meta`：服务名、构建版本（部署的 `APP_VERSION`）、列名、`turns` 条数 |

### `GET /api/reason-turns` — 日志列表（过滤 / 排序 / 分页 / 预览）

| 参数 | 取值 | 说明 |
|---|---|---|
| `task_id` `mode` `model` `status` | 精确匹配 | 取值见 facets |
| `agent` | 精确匹配 `agents.name` | 是**名字**，不是 `agent_id` |
| `q` | 子串匹配 `input` / `raw_output` | **字面匹配**：`%` `_` `\` 都当普通字符（转义后再 LIKE） |
| `order` | `id`（默认）/ `created_at` / `duration_ms` / `total_tokens` | 白名单，其他值回落 `id`（**不是**拼接 SQL） |
| `dir` | `desc`（默认）/ `asc` | 其他值当 `desc` |
| `limit` / `offset` | 整数 | 默认 50 / 0，`limit` 上限 500（夹到 500，响应里回报实际值）；`limit=0` 或读不出来就是默认 |
| `preview` | `1` / `true` | 只回 `input` / `output` 的前 `truncate` 个字符（列表页不必拉全文） |
| `truncate` | 整数，默认 400 | 按 **rune** 截断；截断就是前缀，不加省略号 |

```json
{ "turns": [ { "id": 12, "task_id": "task-29", "cycle": 3, "mode": "plan", "agent_id": 10001,
               "agent": "agent-10001", "provider": "cline", "model": "deepseek-v4-flash",
               "status": "finished", "input": "…", "output": "…", "normalized_output": "…",
               "duration_ms": 445086, "total_tokens": 0, "cost_cents": 2.1322308,
               "created_at": "2026-09-20T07:49:46.033072Z" } ],
  "total": 12, "limit": 50, "offset": 0 }
```

`total` 是**过滤后**的总数（分页器用）。排序稳定性：`ORDER BY <order> <dir>, id DESC` —— 同 key 时用 id
兜底，翻页不跳行、不重复。`turn` 的字段与 §4.0 契约逐项对应（库里叫 `llm_provider` / `raw_output`，
对外叫 `provider` / `output`；`cost_cents` 为 `null` 表示 provider 没报成本，与 0 不同）。

### `GET /api/reason-turns/{id}` — 单条全文

`200` 一个 `Turn` 对象，字段同上但**不截断**。`id` 非数字 → `400`；没有这条 → `404
{"error":"turn not found"}`。

### `GET /api/reason-turns/facets` — 筛选下拉的取值

一次给全六类（去重 + 计数，跳过空值，按计数降序、同计数按值升序）：

```json
{ "tasks": [{"value": "task-29", "count": 12}], "agents": [{"value": "agent-10001", "count": 8}],
  "modes": [{"value": "plan", "count": 8}], "models": [{"value": "deepseek-v4-flash", "count": 9}],
  "providers": [{"value": "cline", "count": 9}], "statuses": [{"value": "finished", "count": 9}] }
```

`agents` 是**名字**（`LEFT JOIN agents`）；join 不上的 turn 不计入（旧库里 `agent_id` 是 TEXT 的那种行
也照常读，只是名字为空、不进 facets）。

### `GET /api/tasks` — 对比页的 task 选择器（也是控制面「Autonomy 任务」页）

与 `POST /api/tasks` **同路径不同方法**：读的是候选与它们的运行量。

```json
{ "tasks": [ { "id": "task-29", "description": "…", "status": "blocked", "turns": 12,
               "last_at": "2026-09-20T07:49:46.033072Z",
               "project_id": "project-749a0238", "agent_id": 10001,
               "updated_at": "2026-09-20T07:49:46.033072Z" },
             { "id": "task-28", "description": "…", "status": "error", "turns": 0, "last_at": "",
               "project_id": "", "agent_id": 10000, "updated_at": "2026-09-20T07:00:00.1Z" } ] }
```

- 候选 = `tasks` 表的行 ∪ **只出现在日志里的 task_id**（行被删了、或没落库的历史数据也要能对比）；
  `description` / `status` 取自 `tasks` 表，没有就是 `""`。
- `last_at` 是该 task 最近一条 turn 的 `created_at`，没有 turn 就是 `""`。
- `project_id` 是这条 task **行里自己写下的** project（`tasks.context_ref` 的 `project` 键），`agent_id` 是它的
  owner agent，`updated_at` 是行自己的时间（每次运行推进，所以「多久没动」读它）。**只出现在日志里的 task
  没有行**：三者分别是 `""` / `0` / `""`。
  注意这是**字面**读法，不解析引用链：世界写成 `{"task": "<平台 task id>"}`（跟着那条 task 的世界）时这里就是
  `""` —— 列表是本地一次查询，**不为了一个 project 去问平台注册表**（那会让列表页依赖控制面在不在）。这类 task
  的 `context_ref` 与它真正解析出的 project，读详情（`GET /api/tasks/{taskID}`）看。
- `?project_id=<id>` **只返回这个 project 的 task**（按上面的 `project_id` 过滤）；省略 = 全部；
  **未知 id → `200` + 空数组**（"这个 project 没有 task"是过滤的事实，不是请求的错）。
- 排序：`last_at` 降序（最近在前），同则 `id` 升序。不分页（task 数量级不大，一次渲染下拉）。
- `updated_at` 是 RFC3339 UTC 文本；精度的最后几位取决于 engine 的时间列（sqlite 纳秒、postgres 微秒），
  同一个时刻在两边是同一个时刻、不是同一个字符串。

### `GET /api/tasks/{taskID}/turns` — 一个 task 的执行序列

对比页要按**执行顺序**把 turn 排起来，所以这里按 `created_at ASC, id ASC`（不是 id、也不是时间倒序）。

- 参数：`limit`（默认 1000，上限 1000）。
- 响应：`{"task_id": "task-29", "turns": [ /* 全文，不截断 */ ], "total": 12, "capped": false}`
- `capped=true` 表示被 `limit` 截断（页面提示「只显示前 N 条」）；task 不存在或没有 turn → `200` +
  `turns: []`。

### `GET /api/tasks/{taskID}` — 任务定义

已有端点（`TaskProgress`，见上文），对比页要的就是它里面的 `task_id / description / domain / status /
error / goal_type / context_ref / agent_id / created_at / updated_at`。**没有** `context` / `target` /
`goal` / `expected_state` 这几个字段（`tasks` 表里没有它们，也不为迁移临时加列）：取不到时评测侧降级展示
「原始内容见 planner 入口 prompt」，其余照常对比。

### `GET /api/meta` — 能力与版本自述

```json
{ "service": "autonomy", "version": "cabb1e98",
  "reason_turns": { "cycle_column": "cycle", "raw_output_column": "raw_output" },
  "has_tasks_table": true, "turns": 12 }
```

用途：评测侧不用再 `PRAGMA table_info` 猜列名、猜有没有 `tasks` 表。`version` 是部署的 `APP_VERSION`
（没发过版的 `go run` 是 `dev`）。

### `GET /health` — 已有，多一个 `turns`

健康检查仍是部署平台的探活路径，顺手带上 `turns`（日志条数），这样「探到的库是不是正在写的那个」在
HTTP 上就能看出来。计数是尽力而为：读不到就不带这个字段，**探活不因 store 读不了而失败**。

## 服务契约（注解自动登记）

本服务的对外契约**不是手写的规范文件，而是代码里的 swag 注解**：

```
cmd/autonomyd/main.go    General API Info（@title/@version/@description/@BasePath/@host）
src/http_server.go       每个 handler 一段（@Summary/@Tags/@Param/@Success/@Failure/@Router）
        │
        ├─ swag init -g cmd/autonomyd/main.go -o docs --outputTypes json → docs/swagger.json（产物，gitignore）
        └─ scripts/register-contract.sh → 服务中心 client/ci/register-go-service.sh → PUT 契约 + 实例（幂等）
```

接口改了，注解跟着改，下一次发布契约自动刷新 —— 没人手工维护 OpenAPI。注解**运行期零依赖**：
不 import swaggo（`json.RawMessage` 这类推断不出来的字段用 `swaggertype` 标签说明，也只是标签）。

| 触发点 | 怎么做 |
|---|---|
| `build.sh`（发版） | 末尾自动登记，**默认尽力而为**：服务中心不可达只告警、不阻塞发版（`REGISTER_CONTRACT_STRICT=1` 改硬失败，`SKIP_REGISTER_CONTRACT=1` 跳过） |
| 手工 / 补登记 | `bash scripts/register-contract.sh`（幂等，随时能跑） |
| 本地看一眼注解产物 | `swag init -g cmd/autonomyd/main.go -o docs --outputTypes json` + `python3 -c 'import json;print(list(json.load(open("docs/swagger.json"))["paths"]))'` |
| 一致性 | `go test ./src -run TestSwagAnnotationsMatchTheRoutes`：**注解与路由表必须一一对应**，少一条注解（契约漏接口）或多一条（契约撒谎）都红 |

环境变量（`scripts/register-contract.sh`，都可覆盖）：

| 变量 | 默认 | 说明 |
|---|---|---|
| `SERVICE_NAME` | `autonomy` | 注册用的服务名 |
| `DEPARTMENT_ID` | `D0005`（AI研发部） | 归属部门；服务端会拿组织接口把 ID/名称对齐 |
| `INSTANCES` | `127.0.0.1:4300` | 实例地址，逗号分隔（端口即本服务的契约端口） |
| `REGISTRY_URL` / `REGISTRY_NS` | `http://127.0.0.1:4240` / `default` | 服务中心（只绑本机）；写接口收紧后用 `REGISTRY_TOKEN` |
| `VERSION` | `$APP_VERSION` 或 `git describe` | 上报版本；**契约没变时不会写库**（比对规范 sha256，避免刷 revision） |
| `SWAG_MAIN` | `cmd/autonomyd/main.go` | swag 的入口（`cmd/autonomy` 是客户端，自动探测会选错，所以显式指定） |
| `OWNER` / `HEALTH_PATH` / `TAGS` / `GIT_REPO` | `kaulie` / `/health` / `tasks` / 本仓库 | 契约元数据 |

服务中心的客户端脚本本体在 [service-registry](https://github.com/kaulie/service-registry) 的 `client/ci/`；
`scripts/register-contract.sh` 按「环境变量指定 → 本机检出 → 仓库内 vendored 副本 → 浅克隆兜底」找到它。

## 说明

- `message_seq` **不是** run 内的 `llm_messages.seq`（那个每轮重置）；对外游标用全局 `id`，才能跨 turn 增量拉取。
- 接受任务时 `agent_id` 已经确定：指令是先找到这只 agent（必要时按 `tasks.agent_id` resume，见 [agent.md](agent.md)）再入队的。
