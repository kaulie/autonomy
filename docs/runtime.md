# Runtime

## 定义

Runtime 是**可靠执行层**：落实 Action 的生命周期、调度 Provider、收集执行结果，并把可观察事实交回 Agent 循环。

可靠 Runtime，灵活 Agents。

## 职责

- **负责**：排队、执行、超时、取消、重试策略的机械部分；隔离失败；产出 Action 结果与 Event；向 Capability 暴露 `AcquireAgent`（经 `AgentFactory` 注册并按需挂 Cursor 后端）。它给一只 agent 开的**会话**与给 planner 的是同一种（`src/llm_session.go`，见 [session.md](session.md)）：一轮怎么跑、被截断怎么续、记在哪，都只有一份实现。
- **不负责**：理解业务 Goal；自主规划；裁定 Completion Contract 是否满足（可触发 Verification，但不取代 Contract）；也不允许 Capability 私下 `NewClient` / CreateAgent。

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

V1.1：`Autonomy.Run` 把 `Execute` 放到独立 worker goroutine（`dispatchExecute`）；planner 主循环以 `cycleDone` 事件驱动下一轮 Decide / Observe，不再在 `Execute` 调用栈上阻塞（同 task 仍串行等待本轮 Result）。

V1.3：一条指令不再是「直接跑」，而是进这只 agent 的 inbox（`src/inbox.go`）；每个 agent 一个消费者，按到达顺序一条条处理，用户指令可以持续接收（忙时入队）。

V1.2：一条指令进来时，runtime 先按 `tasks.agent_id` 找回这条 Task 对接的 agent（`resumeAgentForTask`，`src/agent_resume.go`）：复用本进程已持有的 handle，或从行里重建、`Adopt` 回 `AgentFactory` 并 Resume 它的 provider 会话；只有从未被处理的 Task 才新建 agent。agent 默认不删（`persistent`），见 [agent.md](agent.md)。
