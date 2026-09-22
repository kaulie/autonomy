# Agent 状态监控页（dashboard）

一只 runtime 里同时活着 planner 和一堆受委托的 worker（[delegation.md](delegation.md)），
再加上有的 agent 是 `persistent`（重启后还在）、有的是 `ephemeral`（用完即弃）。要有人盯住这队 agent，
需要一眼看到「谁在、是什么、在干什么、还活着吗」。这一页就是这个：**一个只读的状态页**，拉一个 JSON
feed，渲染成一张会自己刷新的表。

它不是新的一层服务，也不改 runtime：feed 的每个字段都从 runtime 已经持有的东西读出来 —— store 里的
agent 行（[store.md](store.md) 的 `AgentStore`）合并进程内活的 agent 句柄（`role` / `purpose` 是运行时
状态，行里不持久化）。

## 两个端点

| 端点 | 是什么 |
|------|--------|
| `GET /api/agents` | 数据：所有 agent 的实时状态（`agents[]` + `count` + `generated_at`）。`?include_deleted=1` 带上已软删除的。 |
| `GET /dashboard` | 页面：自包含 HTML，按间隔轮询上面那个端点，渲染成一张表并自动刷新。 |

字段与语义见 [http-api.md](http-api.md) 的「`GET /api/agents`」一节。一句话：
`agent_id` / `name` / `role` / `purpose` / `lifecycle` / `state` / `health` / `working` / `agent_run_id` /
`current_task` / `llm_provider` / `model`。

- `role` / `purpose`：来自活的 agent 句柄。一只重启后还没被重新命中的 agent，行在、但 role 为空 ——
  页面渲染成 `—`，不编一个值。
- `health`：`ok`（还在）或 `deleted`（已被 let go，`state=deleted`）。
- `working`：当前 task 上有一只在途 run（`reason_turns.status=running`）时为 `true`，`agent_run_id` 是那个 run。

## 跑起来（演示）

服务监听端口默认 `4300`（部署契约里的端口，`SERVICE_PORT` > `PORT` > `4300`）。

本机直接跑（不装任何前端工具）：

```bash
# 1) 起服务（sqlite 默认；库里没有 agent 时页面显示 "no agents"，这也是对的）
go run ./cmd/autonomyd
# 或走部署脚本：SERVICE_PORT=4300 scripts/start.sh

# 2) 打开页面：它每 3 秒拉一次 /api/agents 并刷新表格
#    http://127.0.0.1:4300/dashboard

# 3) 直接看数据（页面的同一个 feed）
curl -s http://127.0.0.1:4300/api/agents | jq .
curl -s 'http://127.0.0.1:4300/api/agents?include_deleted=1' | jq .
```

要让它有内容，先接受一条任务（[http-api.md](http-api.md)）：

```bash
go run ./cmd/autonomy -description "开放服务契约的前端入口"
# 这条命令会创建 task/agent 并在跑；再刷新 /dashboard 就能看到那只 agent 和它的 current task。
```

页面上的刷新间隔可以改（1–120 秒），勾选 "include deleted" 也能看已 let go 的 agent。feed 拉不到时
页面显示红字 `feed unavailable: …` —— 「页面卡住」和「确实没动静」不能长得一样。

## 测试

```bash
go test ./src/ -run 'TestListAgents|TestAgentStatusList|TestAgentDashboard' -count=1
```

- `TestListAgentsReadsEveryRowInOrder`：`AgentStore.ListAgents` 按 created_at 返回所有行（含软删除）。
- `TestAgentStatusListMergesStoreAndLiveHandle`：feed 把 store 行和活句柄（role / purpose）合并，
  在途 run 报成 `working`，软删除的默认不出现、要了才出现。
- `TestAgentDashboardEndpoints`：`GET /api/agents` 是 JSON feed，`GET /dashboard` 是轮询它的 HTML 页。
- `TestAgentStatusListWithoutStore`：没有 store 时是错误，不是空列表。

## 边界

- 只读：两个端点都不写任何东西。
- 前端 0 构建：页面就是一个字符串常量（`src/agent_dashboard_page.go`），没有打包器、没有外部资源。
- 加在 `AgentStore` 端口上的只有一个 `ListAgents()`（[store.md](store.md) 的可插拔 engine，sqlite / postgres 各实现一份）。
