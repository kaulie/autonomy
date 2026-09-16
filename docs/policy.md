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

## 代码位置（V1）

- **Policy 是数据，不是代码**：`$PROJECT_ROOT/src/agent_policy/CONSTRAINTS.json` —— 一个扁平的
  `key → 句子` 对象，runtime 在渲染提示词时读取（`src/policy.go`），合并进 `{{CONSTRAINTS}}`，
  也就是 `AGENT_V2.md` / `CODE_EDIT.md` / `DEPLOYMENT_MONITOR.md` 里 `## Constraints` 那节的内容。
- **为什么在文件里**：和 agent policy 同一个理由（见 [capability.md](capability.md)）—— 规则是一句话，
  是有人拥有的东西，改它不该需要重新编译；而渲染提示词的那层（`src/prompt.go`）不认识 deploy / budget /
  privacy 这些语义，它只把给它的东西渲染出来。谁可以部署是**部署边界**的事，就写在部署边界自己的地方。
- **不散落在 Capability 里**（见「演化注记」）：Capability 只声明自己能做什么（`{{CONSTRUCTS}}` 里的
  input/output），不拥有关于自己的权限规则 —— 那是 Policy。
- **运行时自己的事实优先**：这条 Task、唯一可改文件的沙箱由 runtime 自己带，并覆盖同名规则：
  policy 不能重新定义 Task 或沙箱。
- **读不出来要看得见**：文件不存在 = 这个 runtime 没有额外规则（不是错误）；文件存在但读不出来 =
  在 stderr 报出来且不生效 —— 提示词总得渲染，而读不到的规则必须可见，不能被悄悄执行或悄悄丢掉。
- 示例：`{"deploy": "the Runtime's move, not the agent's"}`。coding worker 那一侧更具体的禁令
  （不许 `bin/deploy.sh`、不许 `service.deploy`、不许 rollout restart）写在 `src/agent_policy/CODE_EDIT.md`
  的 `### Deploy Policy` —— 一句话的边界给所有人，具体动作清单给有手的那个人。

## 不变式

1. Agent 不可单方面废止约束自身的 Policy。
2. Policy 收紧可以阻止动作；不能用来把未满足的 Contract 标成 Done。
3. 无 Policy 时也应有安全默认（拒绝高风险未授权能力）。

## 演化注记

V1：可用硬编码白名单（只允许 `service.health_check`）作为 Policy 的退化实现。概念上保留独立 Policy，避免权限逻辑散落在各 Capability 内部。
