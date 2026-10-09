# Event Gateway（外部世界事件的入口）

## 定义

Event Gateway 是 autonomy 感知**外部世界变化**的入口：把进程外发生的事实（部署完成、PR 打开、控制面状态变化、任意 webhook）收成 ontology 里的 [Event](event.md)，写进 World，让 agent 的下一轮决策能看见。

它**不是** Workflow 推进器，也不替 agent 决定下一步。外部系统报告事实；agent 观察后再规划。

## 职责

- **负责**：受理外部事件、规范化成 canonical Event、去重、留下可查询的 log、把最近的事件注入决策周期的 `## World`；当事件点名了一个已有 agent 的 task 时，向该 agent 的 inbox 投递一条 `observation`，让它不必等下一条用户指令就能再观察。
- **不负责**：规定下一步调用谁；发明 task / agent；替代 [Verification](verification.md)；把 GitHub / 部署平台的专有协议写进 runtime 主循环。

## 模块边界

和 [context-builder](context-builder.md) 一样，**本体独立**：

| 层 | 位置 | 知道什么 |
|---|---|---|
| 模块 | `src/eventgateway/` | Envelope → Adapter → Event → Log。不知道 task、agent、prompt、runtime |
| runtime 接线 | `src/event_gateway.go` | 记到 World、注入 prompt、按 `subject.task_id` 入队 observation |
| HTTP | `src/http_server.go` | `POST /api/events`、`GET /api/events` |

Gateway 只在 envelope 不合法时拒绝 ingest。runtime 唤醒 agent 失败只打 `[autonomy] event-gateway: …`，事件已经在 log 里。

## Canonical Event

与 [event.md](event.md) 的逻辑字段对齐：

| 字段 | 含义 |
|---|---|
| `id` | gateway 分配（`evt-` + hex） |
| `source` | 产生方：`github` / `deployment` / `control-plane` / `webhook` / … |
| `type` | 事实类型：`deployment.succeeded`、`github.pull_request.opened`、`Asset.StateChanged` |
| `subject` | 相关 Task / Agent / Asset / Action / Project（均可空） |
| `payload` | 事实内容 |
| `occurred_at` | 世界上发生的时间（省略则用受理时间） |
| `received_at` | gateway 看到它的时间 |
| `idempotency_key` | 与 `source` 一起去重；省略则每次都是新事件 |

V1 的 Adapter 是恒等映射（HTTP body 就是 envelope）。GitHub webhook、部署平台通知做成另一个 `Adapter`（`Match` + `Normalize`）即可，Gateway / Log / HTTP 形状不变。

## 事件怎么到达 agent

1. **写入 World / log**（总是）：下一轮 delta 的 `world.events` 带着最近 N 条（默认 20，`AUTONOMY_EVENT_GATEWAY_PROMPT_LIMIT`）。
2. **唤醒**（可选）：`subject.task_id` 指向一条**已经有 agent** 的 task 时，inbox 多一条 `system` / `observation`，走与 instruction 相同的决策循环（内容是事件 JSON，作为 `additional_input`）。没有 task、或 task 还没有 agent，只记 log，**不发明 planner**。

重复的 `(source, idempotency_key)` 返回原来那条，不再通知。

## HTTP

```bash
curl -sS -X POST http://127.0.0.1:4300/api/events \
  -H 'content-type: application/json' \
  -d '{
    "source": "deployment",
    "type": "deployment.succeeded",
    "subject": {"task_id": "task-…", "asset_id": "svc:autonomy", "project_id": "project-749a0238"},
    "payload": {"pipeline_id": "pipeline-…", "ref": "main"},
    "idempotency_key": "deployment:pipeline-…:succeeded"
  }'
```

- `202` 新事件（`delivered` 表示是否入队了 observation）
- `200` 重复
- `400` 非法 JSON / 缺 `source` 或 `type`
- `503` gateway 关闭

`GET /api/events?source=&type=&task_id=&after=&limit=` 读同一份 log（时间正序）。契约见 [http-api.md](http-api.md)。

外部世界还没变、需要先订一份观察时，走独立模块 [watcher](watcher.md)：一种 `watch` 能力，Probe 覆盖 pull request / deployment / 以后的资产。Watcher 只发 Change；Gateway 才把它变成 Event。

## 环境变量

| 变量 | 作用 | 默认 |
|---|---|---|
| `AUTONOMY_EVENT_GATEWAY` | `0` / `off` / `false` / `no` 关掉 | 开 |
| `AUTONOMY_EVENT_GATEWAY_PROMPT_LIMIT` | 注入 prompt 的最近事件条数 | `20`（最大 100） |
| `AUTONOMY_WATCH_INTERVAL` | Watcher 后台轮询间隔（秒） | `15`（最小 2） |

## 持久化

V1 的 Log 是进程内 `MemoryLog`：重启即忘。接口是给后续 Store 实现留的缝（与 Context Service 的 `Repository` 相同做法），**不**扩成 Store 的第八个并集端口。要跨重启保留事件，换一个 Log 实现即可，Gateway 与 HTTP 不变。

## 不变式

1. 事件是事实，不是下一步指令。Gateway 不调用 Capability。
2. 去重键是 `(source, idempotency_key)`，不是 payload 相等。
3. 唤醒从不创建 task 或 agent。
4. 校验失败才拒绝 ingest；唤醒失败不撤回已记录的事实。

## 代码位置

| 关注点 | 文件 |
|---|---|
| 模块（Event / Envelope / Adapter / Log / Gateway） | `src/eventgateway/` |
| runtime 接线、prompt 注入、observation | `src/event_gateway.go` |
| HTTP | `src/http_server.go`（`handleIngestEvent` / `handleListEvents`） |
| inbox kind `observation` | `src/message.go` |
| Watcher（持续观察 → Change） | `src/watcher/`、`src/watch.go`、能力 `watch`；见 [watcher.md](watcher.md) |
