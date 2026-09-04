# Domain

## 定义

Domain 是相关概念、状态与能力的**语义空间**：描述「这个世界里事情如何成立」，而不是「先写死一条 Workflow」。

真正应建模的是 Domain Model + State Model + Capability Model。

## 职责

- **负责**：定义 Asset 类型、合法状态、状态间因果、相关 Capability 的语义边界。
- **不负责**：替 Agent 锁定唯一执行轨道；替代具体 Context 实例。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Name | 域标识（如 software-service、home-device） |
| Asset Types | 域内对象种类 |
| State Model | 状态与迁移/因果关系 |
| Capability Vocabulary | 域内有意义的能力名与约束 |
| Invariants | 域级不变式 |

示例（软件服务域）：

Service ⊇ Source Code, Artifact, Runtime, Endpoint, Health

因果知识：Running 才能有意义的 Health Check；Artifact 是 Deploy 的输入。

## 关系

- Domain 提供抽象语义；[Context](context.md) 提供当下实例
- [Agent](agent.md) 结合 Domain 与当前状态做反向推理（Desired ← Current）
- Domain 知识可表现为 Workflow Knowledge，但仍不是强制轨道
- [Capability](capability.md) 名称通常属于某个 Domain 词汇表

## 不变式

1. 有 Domain 才能自主规划；「什么都不告诉 Agent」不是自治，是失明。
2. Domain 描述可能性与因果，不等于预先展开所有 Workflow。
3. 动态 Execution Graph 是 Domain + State + Capability 上的推理结果。

## 演化注记

V1 可不实现完整 Domain 引擎；哪怕只有「Service 有 Healthy/Unhealthy」的微域，也应作为显式概念保留，而不是把状态判断散落在脚本里。
