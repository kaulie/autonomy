# Agent

## 定义

Agent 是对某个 Task（或其被委托的子目标）负责的**自主执行主体**：在不确定性下分析、规划、选择能力、委托与重规划。

Agent ≠ Capability。只有需要自主决策时才需要 Agent；单纯「能做某事」是 Capability / Provider。

## 职责

- **负责**：How / Who / When / Which Capability / Which Strategy；对目标推进负责；在事件后重新观察与规划。
- **不负责**：发明业务使命；改写 Completion Contract；代替 Runtime 可靠地执行底层动作生命周期。

人定义 What / Why / Success / Policy；Agent 接受 Task 后，将其视为当前必须负责完成的事情（使命感来自契约，而非自创使命）。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Identity | Agent 标识 |
| Role (dynamic) | 当前是 Task Owner、Specialist，还是 Capability Provider 的承载者 |
| Owned / Accepted Tasks | 正在负责的工作 |
| Declared Capabilities | 对外暴露的能力语义（可注册） |
| Trust / Performance | 历史表现摘要（可后置） |
| Permissions | 可行使的权限边界 |

## 关系

- [Task](task.md)：One Task, One Owner；内部可有许多 Specialist
- [Delegation](delegation.md)：可委托给其他 Agent
- [Capability](capability.md)：发现、选择、组合能力
- [Event](event.md)：接收状态变化并触发再决策
- [Runtime](runtime.md)：将决策落实为可靠执行
- [Verification](verification.md)：对照 Contract 判断 Done / Continue

## 不变式

1. 身份动态：今天可以是 Task Owner，明天可以是 Specialist。
2. Hierarchy 是责任链，不是静态组织架构。
3. 不得因当前只有一个 Agent，就把模型设计成永远只能有一个 Agent。
4. Agent 自报「做完了」不能绕过 Completion Contract。

## 演化注记

V1：单个 Owner Agent + 固定能力即可。接口保留注册、委托、多 Agent 责任链，以便演进到动态 Agent Network。
