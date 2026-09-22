# Execution Loop

## 定义

Autonomy 的核心计算循环是 **Goal-driven Agent Execution Loop**，不是 Workflow Engine。

## 循环

```
Task
  ↓ Understand Goal
  ↓ Resolve Context
  ↓ Observe Current State
  ↓ Understand Domain
  ↓ Find Capabilities
  ↓ Plan
  ↓ Delegate / Execute
  ↓ Observe Result
  ↓ Verify
  ↓ Re-plan   ←─────┐
  ↓ Completion       │
       └─ Continue ──┘
```

浓缩关系：

Goal → Task → Agent → Capability → World State → Event → Agent → Completion

## 术语：cycle 与 step

- **cycle**：**某个 agent 自己的轮次**，从 **1** 开始，相对于它自己而言 —— 不跨 agent 比较。
  一次 prompt → reply 就是一轮：planner 的 cycle 是它对这条 task 的决策轮；被委托的 worker 的 cycle 是**它自己的**轮次
  （第一个 prompt 就是 1），而且 **worker 完全可能有自己的 decision cycle**（规则不预设它是「只干一件事」的：
  它自己跑多轮就是 1、2、3…）。`mode`（plan / agent）说的是这一轮在做什么，不改变 cycle 的含义 ——
  它和「输入行是谁写的」一样由 agent 的**身份**推出，只有一处推导（见 [session.md](session.md)）。
  被截断的一轮重试**不占**新的 cycle：失败的原始轮与重试都记在同一个 round。
  落库就是 `reason_turns.cycle` / `llm_messages.cycle`（历史名 `step` 已重命名 —— 一个词不能指两件事）。
  要定位一次交互用 `(agent_id, cycle)` 或 turn id，不要拿两个 agent 的 cycle 相互对照；
  委托方的 cycle 出现在 worker 提示词的 `delegated_by.cycle` 里 —— 那是「谁在第几轮把它派出去」，仍是相对委托方的。
  `Autonomy.MaxSteps` / `AUTONOMY_MAX_STEPS` 数的是 planner 自己的 cycle（名字是历史包袱，语义以本文为准）。
  **历史数据**：这次迁移把此前记的 `0`（worker 没有自己的 cycle 那个年代）与「委托方的 cycle」按**各自 agent 的先后**
  重新编号（第一个 prompt 记 1、第二个记 2）；`llm_messages` 跟着它的 run header 走；只有 `agent_id = 0` 的行保持原样 ——
  无从判断它是这个 agent 的第几轮。
- **step**：**一次 plan 生成的具体行动步骤** —— planner 返回的 plan 里，每个 step 就是一次能力调用
  （`plan.steps[]`）。运行时的执行记录用的才是这个词：计划 `execution_step_plan`（执行前一次性写全）与实际执行
  `execution_step`（跑一步写一行）。

一句话：**cycle 是「相对某个 agent 的第几轮」，step 是「某份 plan 里的第几个动作」。**

## 职责边界

| 阶段 | 主要概念 |
|------|----------|
| Understand Goal | [Task](task.md), [Completion Contract](completion-contract.md) |
| Resolve Context | [Context](context.md), [Project](project.md) |
| Observe / Domain | [Asset](asset.md), [Domain](domain.md), [Event](event.md) |
| Find / Plan | [Capability](capability.md), [Provider](provider.md), [Policy](policy.md), [Trust](trust.md) |
| Delegate / Execute | [Delegation](delegation.md), [Action](action.md), [Runtime](runtime.md), [Agent](agent.md) |
| Verify | [Verification](verification.md) |
| Done / Continue | Contract 求值结果 |

## 实现：一轮 = 一个决定 = 整份 plan

代码：`src/autonomy.go:Run` · `src/agent.go:DecideAtCycle` · `src/prompt.go:parseDecision` · `src/decision.go` · `src/runtime.go:Execute`。

- 每轮：`DecideAtCycle(cycle)` → `Decision` → **dispatch** `Runtime.Execute(decision)`（独立 worker goroutine）→ 事件 `cycleDone` → `Agent.Observe(result)` → 下一轮。Planner 主循环在 dispatch 之后停在事件等待上，**不**锁在 `Execute` 里（`src/autonomy.go:dispatchExecute`；见 [runtime.md](runtime.md)、[principles.md](principles.md) §Event Driven）。同 task 仍保持 decide → execute → observe 的顺序，下一轮 Decide 必须先看到本轮 `previous_actions`。`Execute` 先把这一轮的计划写进 `execution_plan` / `execution_step_plan`，再逐步执行并写 `execution_step` —— 见 [execution-step.md](execution-step.md)（`Autonomy.MaxSteps`，默认 `1`）。
- **决策先过闸，再执行**：`Runtime.Execute` 的第一件事是校验这份 Decision ——
  ①**类型契约**（AGENT_V2 §Type-specific Requirements：`plan` 非空、每个 step 说清 `expected_effect`、引用的 evidence 存在；`done` 不带 plan/need 且必须带 evidence；`blocked` / `need_input` 不带 plan 且必须写清 `need.description`；给了 `need.options` 就必须是 2–9 个互不重复、非空的选项——要么留空：开放问题由人用自己的话回答、只有一个选项那不叫选择），
  ②**计划的数据血缘**（每个入参来源可读、必填入参齐备，见 [execution-step.md](execution-step.md)）。
  任一条不成立 → 这一轮的计划**不写、不执行**，错误里带规则名与出错的那一步；循环继续，下一轮 planner 在 `previous_actions` 里看到这条原因再规划（`src/decision_rules.go` / `src/plan_lineage.go`）。
  **契约不靠 prompt 兜**：prompt 只是请求，运行时才是闸门 —— 一个"没有任何证据的 done"不能凭模型一句话就把 Task 收成完成。
- **完成契约先钉住，`done` 才收束**：答复里的 `completion_contracts` 在 **cycle 1** 被钉住（`completion_contract`，只写一次，之后重述无效；首轮那轮还会先校验它），之后每轮的 Runtime Context 都把这份钉住的合同带回去。`done` 是**唯一**被验证的答复：运行时逐条判据解析**证据槽**（planner 预先绑定的 `step:<name>.output.<key>` / `world_model:asset.<id>.<field>`，解析范围是这个 task 的步历史 —— 那个 id 要到执行完才有），再到**权威来源**查真（World Model，或证据产出者对应的只读能力 / 判据自己写的 `check`），全 `pass` 才收束成 `completed`；`fail` / `inconclusive`，以及「任务根本没有钉住合同」，都让这一轮失败，verdict 进 `previous_actions` 交给下一轮 planner —— 见 [verification.md](verification.md) 的事实链与三态。
- 模型按 AGENT_V2 §Output Schema 回答；`parseDecision` **保留整份 plan**：`plan.steps[]` 每个 step 一个 action，按序放进 `Decision.Actions`，`evidence` / `need` / `deliverable` / `presentation` 一并带回（不再是"只留第一个 action，其余丢掉"）。
- `Runtime.Execute` **在同一轮里按序执行所有 action**；第一个失败就结束该轮（`Result.Message` 会写第几个/共几个）。
- **失败不结束 Task**：worker 把失败记进该轮 `Result`（`Err` + `Message`），planner 在 `cycleDone` 上 Observe 后继续下一轮 decide 重规划；只有当**最后一轮**失败时 task 才收成 `error`（`src/autonomy.go:Run`）。
- 之前的每轮结果都作为 `DecisionContext.History` 传给下一次 decide，prompt 的 Runtime Context 里渲染成
  `previous_actions: [{cycle, message, status, error, actions:[{capability, input, output, error, expected_effect, evidence_refs}]}]`（正是 policy 里 `previous_action` 证据来源）。
  **每个 action 的原始 input/output 都原样保留、不合并**，planner 自己决定看哪条（例如 `code_edit` 的 `summary`/`workspace`）—— 这就是"观察结果再决定"的那一半。
- **重启之后仍然是同一条 task**：`previous_actions` 只装**本次 run** 的 cycle；本次 run 之前那条 task 做过的事（包括上一个进程在被重启前跑过的轮次）由 run 开始时从 **runtime 自己的记录**里读回来（`execution_plan` / `execution_step_plan` / `execution_step`），作为 Runtime Context 的 `briefing` 交给每一轮（`src/task_record.go`）：
  `briefing: {note, state: {last_round, verdicts, open_criteria, interrupted}, earlier_rounds: [{plan_id, cycle, decision, reason, status, error, need, steps: [{idx, name, capability, status, input, output, error, expected_effect, duration_ms}]}]}`。
  为什么必须有它：**provider 会话会随进程走**（Cline bridge 的会话活在 bridge 进程里），会话没了以后，`previous_actions` 是空的、`task.status` 只写着本次受理的 `pending` —— 没有 briefing，一次续做就是「从头开始」：重开 PR、重部署。`note` 明说这些轮次是**本次 run 之前**的记录，`state.open_criteria` 是契约里还没有 `pass` 的判据（还差什么）。
  一个 step 计划了却没跑（`execution_step_plan` 有行、`execution_step` 没有）在这里是 `status: pending` + 计划原文的 input —— 「上一轮停在哪一步」因此是看得见的。
  当上一个进程是**被重启切断**的，`state.interrupted` 会明说这件事（`{reason, stopped_at_step, next_step}`，来自 runtime 自己写的停止原因），`note` 也多一句「这一轮是被重启切断的，没跑的 step 才是要接着做的」——开机自愈续起来的 run 因此知道自己在接着谁做（[graceful-restart.md](graceful-restart.md)）。worker 的 Runtime Context **不带** briefing：委托方那一轮已经说清，每次委托不该背上 task 全史。
- **消息是增量的**：推理会话本来就是多轮的（Cline session / Cursor agent 保留上下文），所以 AGENT_V2 的
  **frame**（Agent / Role / Delegation / Output Schema / Goal Type / Completion Principles / Constructs）只在
  **一个会话的第一轮**发送；之后每轮只发 **delta**（当前 Task / Context Entity / World / Runtime Context /
  Constraints，Runtime Context 里已经带着 `cycle` 与 `previous_actions`），以及"本轮是第几个 cycle、按已知 schema 回答"这一句。
  **谁是它自己（`{{AGENT}}`：role / id / name / backend / model / lifecycle / workspace）属于 frame**：
  这些东西整个会话不变，而"这一轮是第几轮"（`cycle`）每轮都变、留在 delta —— 见 [agent.md](agent.md)。
  frame 里的 per-cycle 占位符渲染成 `reasoningDeltaMarker`，所以 frame 整段字节不变、能吃到 prompt cache。
  delta 里的 **Context Entity 不是引用字符串**：每个 cycle 在渲染 prompt 之前，runtime 先用 task 的 `context_ref` 走一遍 context builder
  （[context-builder.md](context-builder.md)），把 id 解析成世界（本进程的容器 + 平台的 project / organization / service 注册表），解析结果随
  `DecisionContext.ContextSections` 进这一轮的 delta；解析不到就只报引用本身，不影响这次决策（`Agent.decide` → `fillContextSections`）。
  是否发 frame 由 `Agent.needsLLMFrame()` 决定：新会话（Cursor `Create` / Cline 新 handle（含 mode|cwd 变化））
  为真，且只有 run **成功之后**才 `markLLMFrameSent()` —— 首轮失败会重发 frame，不会让会话裸着没有指令；
  `Resume` 回来的会话视为已有 frame。见 `src/prompt.go` / `src/reasoner.go` / `src/llm_frame_test.go`。
- step 的 `capability` 未注册 → 该位置变成 `NothingAction{Reason}`：**只跳过这一步，后面的 action 照常执行**（旧实现会让整份 plan 消失）。
- `type` 不是 `plan`（`done` / `blocked` / `need_input`）时 `Actions` 为空，`Execute` 不执行任何动作，且不是错误。
- **结论了任务的一轮就收束**：`done` / `blocked` / `need_input` 是**答案**而不是计划（`Decision.Concludes()`），循环在这一轮结束 —— `done` 是完成契约被满足（且按 §Type-specific Requirements 必须带证据），另两个是任务在等外部的东西。再问一次 planner 只会用一个 cycle 换回同一句话（task-26 就是计划四步全成功后连答了三次 `done`）：这正是不变式 2「每轮以观察与验证收束，不是以轮数用尽收束」。**失败**的轮才是继续重规划的那一类，`MaxSteps` 兜住它。
  这一轮**照样过闸、照样落库**（`Runtime.Execute`：校验类型契约 + 写出 `execution_plan` 那一行 —— 答案也是记录，`done` 的证据、`blocked` / `need_input` 的 `need` 都在那一行上），所以「是不是答案」由**决策本身**（它的 `type`）决定，读它的时机在执行之前 —— 执行结果不会、也不能改变它。
  但**只有站得住的答案**才收束：被拒绝的答案（没有证据的 `done`、没写清 `need.description` 的 `blocked` / `need_input`）是一个**失败的轮**，和别的失败完全一样 —— 不写、不执行、下一轮 planner 拿着规则名重规划。这正是上一层「决策先过闸」那句承诺的兑现：闸门把原因写进了错误里，就得有人有机会看它 —— 只看 `type` 就收束的话，一个「补上证据即可」的 `done` 会被直接判成任务失败。
  任务行跟着落同一个结论：`done` **验证通过** → `completed`；一个始终没验过的 `done`（被拒、或世界不满足、或判不了）→ `unverified`，`tasks.error` 写它为什么没通过；`blocked` / `need_input` → 同名状态（`TaskStatusFor`，`src/decision.go`）—— 任务在等东西不是跑完了；为什么在等是那次决策的 `need`，`tasks.error` 仍然只写失败的原因。
- 轮数上限 `Autonomy.MaxSteps`（默认 `DefaultMaxSteps = 1`；它数的是 **decision cycle**，`step` 这个词现在专指「执行步」）；`AUTONOMY_MAX_STEPS=N` 可在不重编的情况下调大 —— 只有把它设为 ≥2，"失败后进入下一轮 decide"才真的会发生。

## 不变式

1. 计划是动态产物；固定 Workflow 只是特例。
2. 每轮以观察与验证收束，而不是以「步骤计数用尽」收束。
3. Re-plan 是一等路径，不是错误处理的边角。

## 下一里程碑（实现预告）

第一轮可运行 hello 预定场景：

1. 存在假服务 Asset（可 Healthy / Unhealthy）
2. Task Goal = 服务 Healthy；Completion Contract = health check 通过
3. 单一 Owner Agent 选择固定 Capability `service.health_check`
4. Runtime 执行 → Event → Verification → Done 或 Continue

本文件只定义循环；具体代码在后续变更中落地。

## 演化注记

即使 V1 把 Plan 写成「永远先 health_check」，循环阶段仍应按上表留清晰缝隙，以便插入真正的动态规划与多 Agent 委托。
