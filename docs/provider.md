# Provider

## 定义

Provider 是某个 [Capability](capability.md) 的**具体实现方**：把稳定语义落到特定设备、服务、进程或 Agent 承载上。

## 职责

- **负责**：在声明的约束下真正执行能力；报告效应与证据线索。
- **不负责**：定义全局目标；改写 Completion Contract；独占 Capability 语义名称。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Identity | 实现方标识（设备、服务、Agent、插件等） |
| Capability Bindings | 提供哪些能力语义 |
| Availability | 是否在线 / 可调度 |
| Permission Scope | 可在何种 Context 下被选用 |
| Cost / Performance / Trust | 选择信号 |
| Endpoint / Runtime Affinity | 实际执行位置 |

## 关系

- 多个 Provider 可实现同一 Capability
- [Agent](agent.md) 可以作为 Provider（当它对外只暴露能力执行时），也可以是规划主体
- [Runtime](runtime.md) 负责调度 Provider 执行 [Action](action.md)
- [Trust](trust.md) 影响 Provider 选择

## 不变式

1. Task Owner 按 Capability 选型，默认不绑死某一 Provider。
2. Provider 更换不应迫使 Task / Contract 模型变更。
3. Provider 的成功回执仍须经 [Verification](verification.md) 对照世界状态。

## 演化注记

V1：一个 Capability 对应一个内置 Provider（例如假服务的 health check 实现）即可。接口保留多 Provider 注册与按 Trust/Cost/Availability 选择。
