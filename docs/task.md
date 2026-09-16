# Task

## 定义

Task 是系统的**工作契约（Work Contract）**：对某个目标负责到底的最小工作单位。

它不是传统项目管理里的「待办事项」。

## 职责

- **负责**：承载人定义的 What / Why / Success Criteria / Policy；指向工作所在的世界（Context）；指定最终结果责任人（Owner）。
- **不负责**：规定每一步怎么做；充当 Project 的子节点生命周期容器；替 Agent 决定执行路径。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Source | 任务从何而来（人、系统、上游委托） |
| Context Ref | 指向工作世界，不内嵌整份环境 |
| Current Situation | 创建或接受时已知的局面摘要 |
| Goal / Objective | 要什么 |
| Constraints | 不可逾越的边界 |
| Completion Contract | 什么状态才算真正完成 |
| Owner | 对最终结果负责的 Agent |
| Policy | 适用的策略引用（权限、预算、是否允许扩展能力等） |

其中 **Goal** 与 **Completion Contract** 构成 Task 的核心。

**运行结果也是数据**（`tasks` 表，见 [store.md](store.md)）：`status` 是落点（`running` / `completed` /
`error`），`error` 是**为什么**——这一轮 decide 失败（`decide: ...`），或最后一个 cycle 里失败的 action 及其
原因。两者都只在「记下结果」时写，不在起跑时写：一行只在真的有结果时才改口。这样一次失败不会随进程退出而
消失（此前只有终端上那行，库里只留一个 `status=error`）。

## 关系

- Task → [Context](context.md)：获得世界信息
- Task → [Completion Contract](completion-contract.md)：完成锚点
- Task → [Agent](agent.md)：One Task, One Owner
- Task ← [Delegation](delegation.md)：可被拆出子 Task / 子委托，但用户视角仍只有一个最终 Owner
- Task ↛ Project 父子生命周期：Project 是 Context，不是 Task 的父对象

## 不变式

1. 人定义目标与成功标准；Agent 不对业务使命「自创」完成定义。
2. 同一用户可见 Task 只有一个最终 Owner。
3. Completion Contract 不可被执行路径偷偷改写。
4. Task 通过 Context Ref 工作，而不是复制全量配置。

## 演化注记

V1：单个 Task + 单个 Owner + 固定 Capability 即可跑通闭环。接口上必须允许后续出现 Specialist 委托与动态 Agent Network，而不把 Task schema 绑死在单步 Workflow 上。
