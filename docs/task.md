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
| Owner | 对最终结果负责的 Agent。落库在 `tasks.agent_id`：一条 Task 与一只 agent 的配对写在行里，所以重启之后一条新指令能按它找回同一只 agent 并 Resume（见 [agent.md](agent.md)），一次不携带 agent id 的 task 写入也不会把它抹掉 |
| Policy | 适用的策略引用（权限、预算、是否允许扩展能力等） |

其中 **Goal** 与 **Completion Contract** 构成 Task 的核心。

一条 Task 的**指令**是发给它那只 agent 的消息（`instruction`，见 [inbox.md](inbox.md)）：第一次就是它被接受时的
描述（`tasks.description`），之后的每一条是新的消息；任务行本身不会被后来的指令改写。

**世界也写在行里**：`tasks.goal_type` / `tasks.context_ref`（`{"容器类型": "容器 id"}` 的 JSON）是这条 Task 被受理时的
目标类型与它所在的世界。跟 `tasks.agent_id` 同理，后来的指令不携带它们时不会把它们抹掉 —— 受理时从行里读回，所以一条
只说得出 task id 的指令仍然在同一个世界里跑（见 [store.md](store.md)、[http-api.md](http-api.md)）。

**世界也可以指向另一条 task**：`context_ref: {"task": "<id>"}` 说的是"这条 task 的世界，跟着那条 task 走"。那条 task 的 world
同样写在**它的**行里，所以解析从它出发（本进程先答，本进程没有的由平台按 task id 答，见 [context-builder.md](context-builder.md)）：
拿到它的 project，再照常往下走 —— project → organization → 那个组织登记的服务。于是"接着某条 task 干"不需要重述仓库、组织与服务。

**运行结果也是数据**（`tasks` 表，见 [store.md](store.md)）：`status` 是落点，取值就是**收束这次运行的
那个决策**：`running`（还在跑）→ `completed`（`done` **且验证通过**：完成契约的每条判据都被权威来源证实，
见 [verification.md](verification.md)）／`unverified`（运行结束时最后一个答复是 `done`，而它始终没通过验证 ——
被类型规则拒、被验证拒、或世界不满足）／`blocked`／`need_input`（任务在等东西，不是跑完了）／`error`（失败）。
`error` 是**为什么**——这一轮 decide 失败（`decide: ...`），或最后一个 cycle 里失败的 action 及其原因；`unverified`
这一行的 `error` 写的是**为什么没验过**（哪条判据、期望什么、权威来源答了什么）；`blocked` / `need_input` 的"为什么"
是那次决策自己的 `need`（在 `execution_plan.need` 与那条回复里），不写进 `error`。两者都只在「记下结果」时写，不在
起跑时写：一行只在真的有结果时才改口。这样一次失败不会随进程退出而消失（此前只有终端上那行，库里只留一个
`status=error`）。

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
