# HTTP API

Autonomy 对外的任务 HTTP 接口。部署平台按服务契约调用 `scripts/restart.sh`：注入 `SERVICE_PORT`（优先于 `PORT`）、`RUNTIME_DIR`、`APP_VERSION`，契约里 autonomy 的 port 是 `4300`，探活 `GET /health`。

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

`message_id` 是这条指令在 inbox 里的消息 id，`queued` 是它前面还有几条（`0` = 下一条就是它）。

同一个入口也有进程内的同步版本：`Autonomy.Run(AcceptTaskRequest)`（测试用它）。它走的是同一条 accept 路径（同一个 task、
同一条指令消息、同一只 agent），区别只在于它会等这次运行结束才返回，返回这次运行的错误和同一份接受信息
（`*AcceptTaskResponse`）。命令行客户端（`cmd/autonomy`）不链接 runtime，它是这条路线的 HTTP 版本：
`POST /api/tasks` 拿到接受信息，再用 `GET /api/tasks/{id}` 轮询到状态不再是 `running` / `pending`（`-wait`，默认开），
运行的结局就是命令的退出码。请求里的 `description` 为空时，指令内容取这条 task 行已有的描述（`tasks.description`）。

### `POST /api/tasks/{task_id}/stop`

停掉这个 agent 正在处理的那条消息（planner 循环在当前 decide 或当前 cycle 的事件等待处退出），状态记为 `stopped`；
停止本身也记成一条来自 `system` 的消息，排在它停下的那条之后。队列里还没处理的消息不会被丢掉 —— 那是 agent 接下来要做的
（见 [inbox.md](inbox.md)）。

- `200`：`{"task_id","status":"stopped"}`
- `404`：没有这条 task
- `409`：task 不在运行中

### `GET /api/tasks/{task_id}`

查询任务进展：状态、错误，以及每一份 execution plan 和其中每一步的执行情况。

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

## 说明

- `message_seq` **不是** run 内的 `llm_messages.seq`（那个每轮重置）；对外游标用全局 `id`，才能跨 turn 增量拉取。
- 接受任务时 `agent_id` 已经确定：指令是先找到这只 agent（必要时按 `tasks.agent_id` resume，见 [agent.md](agent.md)）再入队的。
