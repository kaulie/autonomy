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
- **落库表现**：用户只写顶层 Task 的输入；被委托的那次 run 的输入行在 `llm_messages` 里记成
  `role=agent`（谁委托的），不再是 `user`（见 [llm-message.md](llm-message.md)）。
  worker 的 **`agents.current_task_id` 与它的 `reason_turns.task_id` 是同一个**（委托方那条 Task 的 id，
  由 `AcquireAgentOpts.TaskID` 带过去），因为它干的活属于同一条 Task
- **执行位置**：被委托的 agent 在自己的 `AGENT_WORKSPACE` 里执行。委托方不能把自己的 workspace
  交给它 —— 委托走 `AcquireAgent` 且**不带 workspace**（`code_edit` 不再接收 `workspace`/`cwd` 输入），
  worker 的 workspace 由 `AgentSession.Workspace()` 回读并渲染进提示词（见 [agent.md](agent.md)）
- **会话是同一个**：worker 拿到的 session 与 runtime 给 planner 的**是同一种**（`src/llm_session.go`）——
  capability 看到的只是它的窄视图（`ID` / `Workspace` / `Prompt` / `Release`）。它这一轮的 `mode=agent`、
  输入行记成 `role=agent`、round 从 1 开始，都不是"另一种 session"，而是**身份**（`Role=worker`）推出来的，
  见 [session.md](session.md)

- **提示词在文件里，不在代码里**：worker 拿到的提示词是仓库文件，每次委托现读现渲染；
  改措辞不用重新编译，文件缺失则该次委托直接失败：
  - **凡是「需要 agent」的能力，交给这个 agent 的提示词都带委托方 runtime 的 frame，并且身份那一节是它自己**：
    `{{AGENT}}`（它自己的 role / id / name / backend / model / lifecycle / workspace —— 委托方的身份只以
    `delegated_by` 出现在 Runtime Context 里）/ `{{WORLD}}` / `{{RUNTIME_CONTEXT}}` /
    `{{COMPLETION_PRINCIPLES}}` / `{{CONSTRAINTS}}` / `{{CONSTRUCTS}}`（以及 `{{TASK}}` / `{{CONTEXT_ENTITY}}` /
    `{{GOAL_TYPE}}`），词表与渲染规则只有一份
    （`src/capability/broker`：`WorkerFramePlaceholders` / `WorkerFrame` / `RenderWorkerPrompt`），
    按 worker 自己的身份/沙箱渲染，委托方记成 `delegated_by`；取不到的 host（测试替身、没有 World 的宿主）
    该节渲染成 `(not provided by this runtime)`，而不是把 `{{NAME}}` 原样丢给模型。
    提示词文件用哪几节由模板决定。
  - `code_edit` → `$PROJECT_ROOT/src/agent_policy/CODE_EDIT.md`（`{{WORKSPACE}}` / `{{GOAL}}` + 上面整套 frame）
  - `deployment.monitor` → `$PROJECT_ROOT/src/agent_policy/DEPLOYMENT_MONITOR.md`
    （`{{DEPLOYMENT}}` / `{{STATUS_URL}}` / `{{OBSERVATION}}` … + Agent / World / Runtime Context /
    Completion / Completion Principles / Constraints，见 [deployment-monitor.md](deployment-monitor.md)）
  - 不拿 agent 的能力（如 `pull_request.review`、`service.deploy`）没有 frame 可言：它们是确定性调用，
    值走输入和环境，不需要向谁交代世界状态

## 不变式

1. One Task, One Owner, Many Specialists：对外单一最终责任人。
2. 子契约服务于父目标，不能把父 Completion Contract「降级」成更易满足的标准。
3. Hierarchy 是责任链，不是静态 CEO→VP 组织图。
4. 今天的 Owner 可以成为明天的 Specialist。

## 演化注记

V1：可以没有真实多 Agent，只保留 Delegation 接口（甚至 Owner 自委托到内置能力执行器）。不得把代码写成无法插入第二 Agent。
