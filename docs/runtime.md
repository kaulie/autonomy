# Runtime

## 定义

Runtime 是**可靠执行层**：落实 Action 的生命周期、调度 Provider、收集执行结果，并把可观察事实交回 Agent 循环。

可靠 Runtime，灵活 Agents。

## 职责

- **负责**：排队、执行、超时、取消、重试策略的机械部分；隔离失败；产出 Action 结果与 Event。
- **不负责**：理解业务 Goal；自主规划；裁定 Completion Contract 是否满足（可触发 Verification，但不取代 Contract）。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Action Queue / Scheduler | 执行调度 |
| Provider Registry Link | 如何找到实现方 |
| Lifecycle Controls | 超时、取消、并发度 |
| Observability | 日志、轨迹、事件发射 |
| Isolation Boundary | 故障与权限隔离 |

## 关系

- 接收 [Agent](agent.md) 的执行意图 → 调用 [Provider](provider.md) → 产生 [Action](action.md) / [Event](event.md)
- 与 Agent 分工：Agent 决定做什么；Runtime 保证「做」的过程可管可控
- 不重新引入「中央 Workflow Engine 作为核心」；Runtime 服务的是 Action，不是固定流程图

## 不变式

1. Runtime 可靠性不意味着 Agent 策略被写死。
2. 执行层成功不能短路 Verification。
3. 同一 Runtime 应能承载未来多 Agent / 多 Provider，而不假设单进程单工具表。

## 演化注记

V1：进程内函数调用即可充当 Runtime。接口上保留 Action 生命周期与事件产出，便于日后换成分布式调度。
