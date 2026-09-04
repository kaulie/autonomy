# Verification

## 定义

Verification 判定：**当前世界状态是否满足 Completion Contract**。

它把「执行成功」与「目标达成」分开。

## 职责

- **负责**：按 Contract 的 Success Criteria 与 Verification Method 收集/评估证据，产出通过、未通过或不确定。
- **不负责**：选择下一步 Capability（那是 Agent）；被 Capability 的自报成功直接短路。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Contract Ref | 对照哪份完成契约 |
| Method | 规则、测试、探针、传感器、模型、其他 Agent、人类 |
| Evidence | 使用的证据 |
| Result | Pass / Fail / Inconclusive |
| Observed State | 验证时看到的世界状态摘要 |

经典分离：

Capability → Effect + Evidence → External Verification → Completion Contract

例如 `printer.print(image)` API 成功，不等于纸上图像正确。

## 关系

- 锚定 [Completion Contract](completion-contract.md)
- 消费 [Action](action.md) 证据与 [Asset](asset.md) 观察，常由 [Event](event.md) 触发
- 结果回到 [Agent](agent.md) / [execution-loop](execution-loop.md)：Done 或 Continue
- 长期方向：把人类判断持续沉淀为可机器验证的标准

## 不变式

1. External verification over self-declared success。
2. Fail / Inconclusive 必须可驱动再规划，而不是被吞掉。
3. Verification 可以更换 Method，但不得偷偷改写 Contract 本身。

## 演化注记

V1 hello：对假服务做 health check，把探针结果当作 Verification 即可。仍应作为独立概念存在，而不是 `if action.ok { done }`。
