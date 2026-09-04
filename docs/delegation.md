# Delegation

## 定义

Delegation 是 Agent 把部分工作**委托**给另一 Agent（或可承担责任的执行主体）的关系：形成当前 Task 的动态责任链。

## 职责

- **负责**：表达「谁把什么子目标交给谁」；传递必要 Context / 约束 / 子契约；在完成后通过事件回到委托方。
- **不负责**：把用户可见的最终 Owner 替换成多个并列负责人；绕过父 Task 的 Completion Contract。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Parent Agent / Task | 委托方 |
| Child Agent / Sub-goal | 接受方与子目标 |
| Scope | 委托范围与不可越权边界 |
| Child Contract | 子完成标准（不得削弱父 Contract 的最终要求） |
| Status | 进行中 / 完成 / 失败 |

示例链：

Task Owner → Coding Agent → Research Agent → …

用户仍只看到一个 Task Owner。

## 关系

- [Agent](agent.md) ↔ Agent 的动态层级
- 常伴随子 [Task](task.md) 或等价工作单元
- 完成时发出 [Event](event.md) 供 Owner 再规划
- 选择接受方可参考 [Capability](capability.md) + [Trust](trust.md) + Cost + Availability

## 不变式

1. One Task, One Owner, Many Specialists：对外单一最终责任人。
2. 子契约服务于父目标，不能把父 Completion Contract「降级」成更易满足的标准。
3. Hierarchy 是责任链，不是静态 CEO→VP 组织图。
4. 今天的 Owner 可以成为明天的 Specialist。

## 演化注记

V1：可以没有真实多 Agent，只保留 Delegation 接口（甚至 Owner 自委托到内置能力执行器）。不得把代码写成无法插入第二 Agent。
