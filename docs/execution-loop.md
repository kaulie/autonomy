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
