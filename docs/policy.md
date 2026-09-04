# Policy

## 定义

Policy 是人设定的**边界与规则**：规定 Agent 在追求目标时可以做什么、不能做什么、以及遇到缺口时的处理策略。

## 职责

- **负责**：权限、安全、预算、隐私、是否允许自我扩展能力、是否必须人工批准等高阶约束。
- **不负责**：描述具体业务 Goal；替代 Completion Contract；编排 Workflow 步骤。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Scope | 适用于哪些 Context / Task / Agent |
| Permissions | 允许的 Capability / Provider 范围 |
| Limits | 时间、成本、重试、并发等上限 |
| Escalation | 能力缺口或风险动作时的升级路径 |
| Override Rules | 谁可以收紧/放宽政策（通常不是执行 Agent） |

Capability Gap 时的政策分支示例：寻找现有 Provider / 安装 Skill / 创建 Capability / 请求人类介入。

## 关系

- 人通过 Policy 定义边界（原则 12）
- [Task](task.md) 引用适用 Policy
- [Context](context.md) / [Project](project.md) 可携带默认 Policy
- 约束 [Agent](agent.md) 的规划与 [Delegation](delegation.md) 选择
- 与 [Completion Contract](completion-contract.md) 分工：Contract 定义「做成什么样」；Policy 定义「做的时候不能越什么线」

## 不变式

1. Agent 不可单方面废止约束自身的 Policy。
2. Policy 收紧可以阻止动作；不能用来把未满足的 Contract 标成 Done。
3. 无 Policy 时也应有安全默认（拒绝高风险未授权能力）。

## 演化注记

V1：可用硬编码白名单（只允许 `service.health_check`）作为 Policy 的退化实现。概念上保留独立 Policy，避免权限逻辑散落在各 Capability 内部。
