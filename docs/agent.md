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
| Identity | Agent 自有标识（与 Task ID 独立；`current_task_id` 关联当前负责的任务）——**被委托的 agent（capability acquire 出来的 worker）记的是同一个 task id**：它和它的 run 都挂在委托方那条 Task 下 |
| Lifecycle | `ephemeral`（默认）：任务结束时先 Cancel 未结束 run，再调用 Cursor SDK `DeleteAgent`；`persistent`：仅 `CloseAgent`，保留可 Resume |
| Workspace | `AGENT_WORKSPACE=/Users/gaolei/agent-workspace-sandbox/{agent_name}/`，创建 Agent 时分配，供 Cursor / `code_edit` 使用。**委托出去的 worker 用自己这一份**：委托方（planner）不能把自己的 workspace 强加给它 —— 委托时 `AcquireAgent` 不带 workspace，worker 在自己的沙箱里干活 |
| Backend | `local`（默认）或 `cursor`：Cursor SDK 只是后端实现；上层统一走 `AgentFactory` + `AttachCursor` / `PromptCursor` |
| LLM Provider | `cursor` / `cline` / `deepseek_harness`：记录当前 LLM 提供方（DB 列 `llm_provider`） |
| Model | 记录当前使用的模型（如 `composer-2`；DB 列 `model`） |
| Role (dynamic) | 当前是 Task Owner、Specialist，还是 Capability Provider 的承载者。**V1 只分两种**：`planner`（runtime 为一条 Task 创建的那个 agent，它决定每一轮）与 `worker`（capability 通过 `AcquireAgent` 要来的那个 agent，干一件事）；worker 的 **Purpose** 就是它被要来的原因（`code_edit` / `deployment.monitor`）。两者都是 runtime state，不落库，只用来渲染 agent 自己的提示词。**这条区分属于 runtime，不属于 planner 的计划**：planner 的 policy 只按 capability 派发（step = capability + input），哪个 capability 背后有 worker 由 runtime 决定，见 [capability.md](capability.md) |
| Session | 这只 agent 与 LLM 的会话（`src/llm_session.go`）：它自己的每一轮都在这里出去、也记在这里。**planner 与 worker 是同一种会话** —— 身份（上一行的 Role）只决定这一轮的形态（plan/agent、输入行是谁写的、round 从哪来），不是另一种 session，见 [session.md](session.md) |
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

## 提示词里怎么出现

每个 agent 自己的身份是提示词里**单独一节**（`{{AGENT}}`）：`## Agent` 先把"你是谁"说清楚，再附上结构化的一条记录 ——
`role`（`planner` / `worker`）、worker 的 `purpose`、`id`、`name`、`backend`、`llm_provider`、`model`、`lifecycle`、`workspace`。
四个 policy 文件（`AGENT_V2.md` / `CODE_EDIT.md` / `DEPLOYMENT_MONITOR.md` / `TURN_TRUNCATED.md`）都用这一节，取的值永远是**读到它的那个 agent 自己**的：
planner 拿到 planner 的，被委托的 worker 拿到 worker 的，委托方只以 `delegated_by` 出现在 Runtime Context 里
（见 [delegation.md](delegation.md)）。

- **身份 ≠ 处境**：身份（role / id / …）整个会话不变，所以在 AGENT_V2 的 **frame** 里；它正在做的 Task、第几轮
  （`cycle`）、谁委托的、`previous_actions` 是 Runtime Context，属于每轮的 **delta**（见 [execution-loop.md](execution-loop.md)）。
  注意 `cycle` 是**相对某个 agent 自己**的轮次（从 1 起，不跨 agent 比较）：worker 的 Runtime Context 里不渲染自己的
  cycle（那是它拿到的处境），它自己的轮次记在它自己的 run header 上，委托方的轮次以 `delegated_by.cycle` 出现。
- **不编造**：runtime 没记录 role 的 agent（比如手搓的测试替身）不会被硬塞一个身份 —— 该字段就不出现。

## 不变式

1. 身份动态：今天可以是 Task Owner，明天可以是 Specialist。
2. Hierarchy 是责任链，不是静态组织架构。
3. 不得因当前只有一个 Agent，就把模型设计成永远只能有一个 Agent。
4. Agent 自报「做完了」不能绕过 Completion Contract。

## 演化注记

V1：单个 Owner Agent + 固定能力即可。接口保留注册、委托、多 Agent 责任链，以便演进到动态 Agent Network。
