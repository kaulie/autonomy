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
- [Agent](agent.md)（尤其 Task Owner）消费 Event 后进入 [execution-loop](execution-loop.md)
- Event 可为 [Verification](verification.md) 提供输入线索
- 与固定 Step1→Step2→Step3 相对：这里是 状态变化 → 事件 → 再决策

## 不变式

1. 系统主驱动是事件与状态，不是中央 Workflow 推进器。
2. 委托完成应发事件给 Owner，而不是隐式假设「子 Agent 会继续替 Owner 做完全部目标」。
3. 失败、超时、能力缺口同样是一等事件。

## 演化注记

V1：内存内同步回调也可冒充事件总线，但概念上要保留 Event，避免把控制流写死成函数硬编码串联。
