# Event

## 定义

Event 是可观察的**事实或状态变化**：驱动下一轮决策的信号，而不是 Workflow 里的下一个固定 Step。

## 职责

- **负责**：把世界变化、Action 结果、委托完成、验证结论等变成可订阅的事实。
- **不负责**：直接规定下一步必须调用谁；替代 Agent 的再规划。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Type | 如 Action.Completed、Task.Completed、Asset.StateChanged、Verification.Failed |
| Source | 产生方（Runtime、Agent、Provider、外部系统） |
| Subject | 相关 Task / Asset / Action / Agent |
| Payload | 事实内容与证据引用 |
| Timestamp / Causality | 时间与因果关联 |

典型循环：

Agent A 完成 → Event → Owner 重新观察 → 再规划 → Agent B → Event → …

## 关系

- [Action](action.md) / [Delegation](delegation.md) / 外部世界 → Event
- 外部世界的入口是 [event-gateway](event-gateway.md)：进程外的事实经 `POST /api/events` 进入 World，并在点名了已有 agent 的 task 时投递 `observation`
- 事实还没发生、需要先盯着一个对象时，是 [watcher](watcher.md)：一种 `watch` 能力，多种 Probe；到达 until 后把 Change 交给 gateway
- [Agent](agent.md)（尤其 Task Owner）消费 Event 后进入 [execution-loop](execution-loop.md)
- Event 可为 [Verification](verification.md) 提供输入线索
- 与固定 Step1→Step2→Step3 相对：这里是 状态变化 → 事件 → 再决策

## 不变式

1. 系统主驱动是事件与状态，不是中央 Workflow 推进器。
2. 委托完成应发事件给 Owner，而不是隐式假设「子 Agent 会继续替 Owner 做完全部目标」。
3. 失败、超时、能力缺口同样是一等事件。

## 演化注记

V1 的 **Event Gateway**（`src/eventgateway`）已经是外部事实的入口，而不是用内存回调冒充总线。进程内 Log 重启即忘；跨重启保留走 Log 端口，不把 Event 写进 Store 并集。内部 loop 自己 emit 的 `Event`（`src/event.go`）仍是运行时事实，与外部 envelope 分开，runtime 把后者映射到 World。
