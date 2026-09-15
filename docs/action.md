# Action

## 定义

Action 是某次 [Capability](capability.md) 的**实际执行实例**：从意图落到世界改变（或尝试改变）的一次运行。

## 职责

- **负责**：记录一次执行的输入、输出、状态、副作用线索与证据句柄。
- **不负责**：决定是否满足 Task 完成（那是 Verification + Contract）；代替整个规划循环。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Capability Ref | 执行的是哪一能力语义 |
| Provider Ref | 由谁执行 |
| Input / Output | 本次参数与结果 |
| Status | 排队 / 运行 / 成功 / 失败 / 超时等 |
| Effect Claims | 执行方声称的世界效应 |
| Evidence Handles | 可供验证引用的证据 |
| Causing Agent / Task | 发起方 |

## 关系

- Capability 是类型；Action 是实例
- [Runtime](runtime.md) 管理 Action 生命周期
- Action 完成后通常产生 [Event](event.md)
- [Verification](verification.md) 消费 Action 的证据与后续世界观察，而不是只信 Effect Claims

## 实现（Go）

| 形态 | 说明 |
|---|---|
| `Action` 接口 | `Execute(ctx DecisionContext) (ActionResult, error)`：返回**这次执行的记录**——实际拿到的 input 与产出的 output；`Runtime.Execute` 按 `Decision.Actions` 顺序调用并逐条收集 |
| `CapabilityAction{Name, Input, ExpectedEffect, EvidenceRefs}` | 已注册能力的一次执行；后两个字段来自 plan step（`expected_effect` / `evidence_refs`），用于留痕"这一步想改变什么、依据哪条 evidence" |
| `NothingAction{Reason}` | 空动作：step 写了 `noop`/`none`，或写了一个未注册的能力（`Reason` 会打印出来，并以 `output.reason` 留痕，而不是静默什么都不做） |

一个 plan 里的多个 action 属于**同一轮**执行（见 [execution-loop.md](execution-loop.md)）；该轮的 `Result` 逐条保留，再交给下一次 decide：

- `Result.Actions []ActionResult`：**每个 action 一条记录，原始 input / output 都保留**（`Capability` / `Input`（含 runtime 补的 `task_id` 等默认值）/ `Output` / `Error` / `ExpectedEffect` / `EvidenceRefs`）。**不做合并、不做取舍** —— 由消费者（下一轮 decide、UI）自己决定看哪条。
- `Message`：`executed N action(s): <capability 列表>`，或 `action i/N (<capability>) failed: <err>`。
- `Err`：失败的 action 的错误（`Run` 用"最后一轮是否失败"决定 task 状态）；失败的 action 记录里同样保留它已产出的 output。

## 不变式

1. Action 成功 ≠ Task 完成。
2. 每次 Action 应可追溯到 Task / Agent / Capability / Provider。
3. 失败与超时也是事件源，应能触发再规划。

## 演化注记

V1：同步执行几个固定 Action 即可。模型上区分 Capability（语义）与 Action（实例），避免把「工具调用日志」直接当成完成证明。
