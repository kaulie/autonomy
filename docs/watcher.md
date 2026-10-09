# Watcher（把尚未发生的外部变化订成将来的 Event）

## 定义

Watcher 是 autonomy **持续观察**外部对象的模块：在事实发生之前登记一份观察，对象到达 `until` 之后交出一条 [Event](event.md)，经 [event-gateway](event-gateway.md) 进入 World。

它和 Gateway 对偶：

| | Event Gateway | Watcher |
|---|---|---|
| 方向 | **推**：世界已经变了，有人来报 | **拉**：世界还没变，先订一份观察 |
| 时机 | 事实到达的那一瞬 | 事实出现之前，一直跑到出现 |
| 产出 | Event | Change → Envelope → Event |

它**不是** Capability，也**不是** Workflow 里的 wait 步骤。决策循环登记之后立刻返回；观察在进程后台跑。

## 职责

- **负责**：按 kind 调用 Probe 读对象、比较 fingerprint、在变化时发出 Change、在 `until` 满足后停掉那条观察。
- **不负责**：规定下一步调用谁；合并 PR / 触发部署；验真（仍是 `pr.check` / `deployment.monitor`）；发明 task / agent；把 GitHub 或部署平台的 HTTP 客户端写进模块本体。PR 的快照来自 [event-center](event-center.md)，不是 GitHub REST。

## 模块边界

和 [context-builder](context-builder.md)、[event-gateway](event-gateway.md) 一样，**本体独立**：

| 层 | 位置 | 知道什么 |
|---|---|---|
| 模块 | `src/watcher/` | Spec / Probe / Observation / Change / Watcher。不知道 task、inbox、prompt、runtime |
| Probe | `pull_request.go` / `deployment.go` | 该对象的词汇（open/merged，succeeded/failed）。读世界的方式是注入的 Snapshot |
| runtime 接线 | `src/watch.go` | PR Snapshot 接 event-center；deployment 仍接 HTTPObserver；Change → event gateway |
| 能力 | `src/capability/watch.go` | **一个** `watch`：kind + target。新对象加 Probe，不加 `*.watch` |
| HTTP | `POST`/`GET /api/watches` | 与能力同一份登记 |

## 一种能力，多种对象

Constructs 里只有 `watch`。Planner 写：

```
watch { "kind": "pull_request", "target": "https://github.com/kaulie/agent-watchdog/pull/9", "until": "merged" }
watch { "kind": "deployment", "target": "pipeline-…" }
```

`pr` / `deployment` 是填写 kind+target 的别名，不是第二种能力。不要发明 `pr.watch` / `asset.watch`。

V1 Probe：

| kind | 默认 until | 到达时的 Event type |
|---|---|---|
| `pull_request` | `merged` | `github.pull_request.merged`（关闭则为 `…closed`） |
| `deployment` | `succeeded` | `deployment.succeeded`（失败则为 `…failed`） |

再加一类资产：实现 `watcher.Probe`，在 `newWorldWatcher` 里登记。Gateway、能力、HTTP 形状不变。

## 事件怎么到达 agent

与 Gateway 相同：Change 被 runtime 写成 Envelope，`subject.task_id` 已有 agent 时入队 `observation`。Watcher 自己不唤醒。

默认不把决策循环挂住。`watch=true` 是这次调用内的有界轮询（默认 60s，上限 10 分钟），超时仍留下后台观察。

后台间隔默认 15s（`AUTONOMY_WATCH_INTERVAL`，最小 2s；旧名 `AUTONOMY_PR_WATCH_INTERVAL` 仍可读）。进程内状态，重启即忘。

## 不变式

1. Watcher 不调用 Capability，不 ingest —— 只发 Change。
2. 新对象是新 Probe，不是新能力。
3. 观察不占决策循环。
4. `until` 已满足或对象不存在，不登记后台 watch（已经发生的事实用 Gateway  ingest，不靠事后补 watch）。
