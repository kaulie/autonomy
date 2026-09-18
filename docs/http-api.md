# HTTP API

Autonomy 对外的任务 HTTP 接口。部署平台按服务契约调用 `scripts/restart.sh`：注入 `SERVICE_PORT`（优先于 `PORT`）、`RUNTIME_DIR`、`APP_VERSION`，默认端口 `4230`，探活 `GET /health`。

本地：

```bash
./build.sh
RUNTIME_DIR="$(pwd)/outputs" bash outputs/scripts/start.sh
```

不走脚本时仍可用 `AUTONOMY_HTTP_ADDR=:4230 go run ./cmd/autonomy`。未设置该变量则走 CLI 演示任务。

## 端点

### `GET /health`

部署平台对每个服务统一探的路径。`GET /healthz` 是同一处理函数的别名。

### `POST /api/tasks`

接受一条任务，立刻返回 `task_id` / `agent_id`，`Autonomy.Run` 在后台 goroutine 执行（不锁住 HTTP 请求）。

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
{ "task_id": "task-…", "agent_id": 10001, "status": "running" }
```

### `GET /api/tasks/{task_id}`

查询任务进展：状态、错误、计划轮次与每轮已执行步数。

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
- 接受任务后若 `agent_id` 仍为 0，可立刻用 `GET /api/tasks/{task_id}` 再取（agent 在 `Run` 开头创建）。
