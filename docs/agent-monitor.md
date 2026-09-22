# Agent 实时状态监控面板

一句话：把 runtime 已经知道的 agent 事实，投影成一张**只读**的实时面板——谁在跑、谁闲着、谁被卡住、谁已经结束。

它不引入新的状态，也不写任何东西：面板是 `agents` 行 + 进程内 factory（role / backend / workspace 这些不落库的运行时字段）+ `tasks`（planner 判定与 task 结局）+ inbox 积压 + 在途 reason turn 的**聚合视图**（`Autonomy.AgentsOverview`，src/agent_monitor.go）。

## 三个端点

| 端点 | 作用 |
|------|------|
| `GET /api/agents` | 只读聚合快照（JSON）：`summary` 计数 + 每个 agent 一行 |
| `GET /api/agents/stream` | 同一个快照，用 **SSE** 按间隔推送（默认 2s，`?interval=N`，1–300s） |
| `GET /monitor` | 单页监控面板（HTML/JS，`go:embed` 进二进制，随服务一起部署） |

三者的产品都在 `src/http_server.go` 里带 swag 注解，契约由 `scripts/register-contract.sh` 自动登记（`src/contract_test.go` 保证注解与路由表一致）。

## 每个 agent 的字段

`id` / `name` / `role`（planner \| worker）/ `backend`（local \| cursor \| cline）/ `model` / `llm_provider` / `lifecycle`（ephemeral \| persistent）/ `status` / `state`（原始运行时状态）/ `current_task` + `task_status` / `last_heartbeat` / `workspace` / `queued_messages` / `active_run_id` / `deleted_at`。

- **last_heartbeat** = `agents.updated_at`：runtime 最后一次写这行 agent 的时间。
- **role / backend / workspace** 优先取进程内 factory 的活 handle；取不到时按行回退——被某个 task 指名的 agent 是 planner，否则 worker；backend 由 `llm_provider` 推导（无 provider = local）；workspace 由 `AgentWorkspacePath(name)` 推导。

## 状态投影（running \| idle \| blocked \| done）

按优先级：

1. `deleted_at` 非空 → **done**（已被 let go）
2. 正在跑（`state=running`，或该 agent 的 task 上有在途 reason turn）→ **running**
3. 有排队消息未开始（inbox backlog > 0）→ **blocked**
4. 当前 task 的状态是 `blocked` / `need_input` → **blocked**
5. 当前 task 已结束（`completed` / `stopped` / `error` / `unverified`）→ **done**
6. 其余 → **idle**

投影是运行时的既有事实，不是第二份真相：`status` 旁边始终保留原始 `state` 与 `task_status`，面板详情里两者都看得到。

## 实时更新与退化

面板优先用 `EventSource` 连 `GET /api/agents/stream`；浏览器不支持或连接断开时自动退化为 3s 轮询 `GET /api/agents`（面板右上角「实时(SSE)」按钮可手动切换）。SSE 每帧是一条 `event: agents` + `data: <AgentMonitorResponse JSON>`。

## 扩展来源

响应的 `sources` 目前是 `["autonomy"]`。同一份 `AgentMonitorEntry` 形状是留给后续并入其它来源（agent-control-plane）的：面板只认这个形状，加来源不用改面板。

## 本地跑

```bash
AUTONOMY_STORE_ENGINE=sqlite AUTONOMY_STORE_DSN=/tmp/autonomy.db \
AUTONOMY_HTTP_ADDR=127.0.0.1:4300 go run ./cmd/autonomyd
# 打开 http://127.0.0.1:4300/monitor
```

单元测试：`go test ./src/ -run 'TestAgentsOverview|TestAgentMonitor'`。
