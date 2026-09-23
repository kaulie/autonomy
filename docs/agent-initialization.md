# Agent Initialization（与 task 解耦的初始化）

一只 agent 的生命从**初始化**开始，而不是从一条 task 开始。初始化只回答「这是谁、在哪里、以什么身份运行」，
不回答「要做什么」——后者是 task 的事（见 [task.md](task.md)）。

## 流程

```
1. 初始化 agent                AgentInitializer.Initialize / Autonomy.InitializeAgent
2. 给出 system prompt          AgentInitOptions.SystemPrompt → Agent.GiveSystemPrompt
3. 接受 task                   Autonomy.AcceptTask / POST /api/tasks（accept 把 agent 与本 task 配对）
4. 先出方案并等待确认           Agent.RequirePlanApproval：run 停在第一个 plan 上（status awaiting_approval）
5. 确认后才进入实现             AcceptTaskRequest.Approve（inbox kind = approval）释放该 plan 并执行
```

## 边界与不变式

- **初始化不认识 task。** `AgentInitOptions` 里没有任何 task 字段；`Initialize` 不设置 `CurrentTask`，
  也不需要 task 存在。`AgentFactory.NewAgent` 本来就与 task 无关，本模块是它的入口。
- **初始化先于 task，task 配对的是这只被初始化的 agent。** `resumeAgentForTask` 在需要新建 agent 时，
  先看有没有一只已初始化、尚未绑定 task 的 planner（`AgentFactory.forInitialization`），有就用它，
  没有才 `Create` 一只新的。
- **system prompt 属于 frame。** 它是稳定的、每个 session 只发一次的那一半
  （`buildReasoningFrame`），排在 agent policy 之前，所以它先于 task 进入会话。为空则不写这一段。
- **plan 先确认、再实现。** `RequirePlanApproval` 的 agent 的第一条 `plan` 不会被执行：
  它经 `Runtime.RecordPlanOnly` 写成计划记录后 run 暂停（`awaiting_approval`），
  计划在、`execution_step` 不在——正是 [execution-loop.md](execution-loop.md) 里
  「计划先写、执行后写」所允许的「计划了但没执行」。确认到达后 `Runtime.ExecuteApproved`
  执行**那一份**计划（不重写、不重规划），随后 loop 照常继续。
- **确认是一条普通消息。** 它是 inbox 里的一条 `approval` 消息（`AcceptTaskRequest.Approve` 或 `Mode:"approve"`），
  复用同一条队列与同一套 run 语义：不必为「确认」开一条旁路。

## 现状（本模块的边界）

暂停中的计划保存在 agent 的内存里（`Agent.pendingPlan`）：**同一进程内**的确认会执行它；
跨进程重启后确认会被当作普通指令处理（重新规划）。按 id 记住「等待确认的那一份计划」是后续可以加深的地方，
不在本模块的范围内。
