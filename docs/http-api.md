# HTTP API

Autonomy 对外的任务 HTTP 接口。契约本身由代码里的注解生成（见文末「服务契约」）。部署平台按服务契约调用 `scripts/restart.sh`：注入 `SERVICE_PORT`（优先于 `PORT`）、`RUNTIME_DIR`、`APP_VERSION`，契约里 autonomy 的 port 是 `4300`，探活 `GET /health`。

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
| `context_ref` | 它引用的世界，如 `{"project": "project-749a0238"}` |
| `project` | **所属 project**：`{"id", "name", "description", "domain", "git_repo_url", "organization"}` |
| `project.organization` | **project 所属的组织（部门）**：`{"id": "D0005", "name": "AI研发部"}` |

`project` 由两处合成，都不强求：runtime 自己注册过的 context container 提供它知道的（`name` / `description` / `domain`），
平台的 **project 注册表**（控制面 `GET /api/projects`，`PROJECTS_API_URL` 覆盖，默认 `http://127.0.0.1:4211`）提供 project 的名字、
仓库与**所属部门** —— 一个 project 属于哪个组织是平台的事实，autonomy 只读、不另立一份注册表。注册表读不到（未启动/超时 2s）
不影响这个接口：详情照常返回，`project` 只剩 id（和本进程世界知道的那点信息），不会因为一个注册表挂了而失败。注册表结果缓存 30s
（失败缓存 5s），所以每次读详情不会真的每次都去问。

进展：状态、错误，以及每一份 execution plan 和其中每一步的执行情况。

`plans[].steps[]` 按计划顺序：`status` 为 `pending`（计划了还没跑）、`ok` 或 `failed`。已跑的步带上实际 `input` / `output` / `error`。

### `GET /api/tasks/{task_id}/agents/{agent_id}`

查询该 agent 的工作状态。若正在工作（有 `reason_turns.status=running`，或 `agents.state=running`）：

- `working: true`
- `agent_run_id`：当前 provider run id（`reason_turns.run_id`；流式中段可能仍为空，此时看 `turn_id`）

### `GET /api/tasks/{task_id}/agents/{agent_id}/events?last_synced_message_seq=N`

按 `llm_messages.id` 做跨 turn 的单调游标轮询增量对话流（thinking / tool / assistant 等）。

- 请求参数 `last_synced_message_seq`：上次同步到的 `message_seq`（即 `llm_messages.id`）；首次传 `0`
- 响应里每条事件的 `message_seq` 供下次轮询；`turn_seq` 是 run 内序（每次 run 从 0 起）

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
      "role": "thinking",
      "content": "…",
      "run_id": "…",
      "status": "running",
      "created_at": "…"
    }
  ],
  "last_message_seq": 42,
  "next_poll_after_seq": 42
}
```


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
