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

代码：`src/autonomy.go:Run` · `src/agent.go:DecideAtStep` · `src/prompt.go:parseDecision` · `src/decision.go` · `src/runtime.go:Execute`。

- 每轮：`DecideAtStep(step)` → `Decision` → `Runtime.Execute(decision)` → `Agent.Observe(result)` → 下一轮（`Autonomy.MaxSteps`，默认 `1`）。
- 模型按 AGENT_V2 §Output Schema 回答；`parseDecision` **保留整份 plan**：`plan.steps[]` 每个 step 一个 action，按序放进 `Decision.Actions`，`evidence` / `need` / `deliverable` / `presentation` 一并带回（不再是"只留第一个 action，其余丢掉"）。
- `Runtime.Execute` **在同一轮里按序执行所有 action**；第一个失败就结束该轮（`Result.Message` 会写第几个/共几个）。
- **失败不结束 Task**：`Autonomy.executeDecision` 把失败记进该轮 `Result`（`Err` + `Message`），循环继续、进入下一轮 decide 重规划；只有当**最后一轮**失败时 task 才收成 `error`（`src/autonomy.go:Run`）。
- 之前的每轮结果都作为 `DecisionContext.History` 传给下一次 decide，prompt 的 Runtime Context 里渲染成
  `previous_actions: [{step, message, status, error, actions:[{capability, input, output, error, expected_effect, evidence_refs}]}]`（正是 policy 里 `previous_action` 证据来源）。
  **每个 action 的原始 input/output 都原样保留、不合并**，planner 自己决定看哪条（例如 `code_edit` 的 `summary`/`workspace`）—— 这就是"观察结果再决定"的那一半。
- **消息是增量的**：推理会话本来就是多轮的（Cline session / Cursor agent 保留上下文），所以 AGENT_V2 的
  **frame**（Agent / Role / Delegation / Output Schema / Goal Type / Completion Principles / Constructs）只在
  **一个会话的第一轮**发送；之后每轮只发 **delta**（当前 Task / Context Entity / World / Runtime Context /
  Constraints，Runtime Context 里已经带着 `step` 与 `previous_actions`），以及"本轮是第几个 cycle、按已知 schema 回答"这一句。
  **谁是它自己（`{{AGENT}}`：role / id / name / backend / model / lifecycle / workspace）属于 frame**：
  这些东西整个会话不变，而"这一轮是第几轮"（`step`）每轮都变、留在 delta —— 见 [agent.md](agent.md)。
  frame 里的 per-cycle 占位符渲染成 `reasoningDeltaMarker`，所以 frame 整段字节不变、能吃到 prompt cache。
  是否发 frame 由 `Agent.needsLLMFrame()` 决定：新会话（Cursor `Create` / Cline 新 handle（含 mode|cwd 变化））
  为真，且只有 run **成功之后**才 `markLLMFrameSent()` —— 首轮失败会重发 frame，不会让会话裸着没有指令；
  `Resume` 回来的会话视为已有 frame。见 `src/prompt.go` / `src/reasoner.go` / `src/llm_frame_test.go`。
- step 的 `capability` 未注册 → 该位置变成 `NothingAction{Reason}`：**只跳过这一步，后面的 action 照常执行**（旧实现会让整份 plan 消失）。
- `type` 不是 `plan`（`done` / `blocked` / `need_input`）时 `Actions` 为空，`Execute` 不执行任何动作，且不是错误。
- 轮数上限 `Autonomy.MaxSteps`（默认 `DefaultMaxSteps = 1`）；`AUTONOMY_MAX_STEPS=N` 可在不重编的情况下调大 —— 只有把它设为 ≥2，"失败后进入下一轮 decide"才真的会发生。

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
