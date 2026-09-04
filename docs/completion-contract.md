# Completion Contract

## 定义

Completion Contract 是执行系统的**完成锚点**：在 Agent 可自由选择路径的前提下，规定「什么状态才算真正完成」。

## 职责

- **负责**：定义期望世界状态、成功判据、验证方法与所需证据。
- **不负责**：规定到达该状态的步骤；被 Capability 的「执行成功」声明所替代。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Expected State | 目标世界状态（例如服务 Healthy） |
| Success Criteria | 可判定的条件集合 |
| Verification Method | 如何验真（规则、探针、测试、传感器、人） |
| Evidence | 完成时需要留下的证据形态 |

示例（逻辑，非实现）：

- Goal：让服务恢复正常
- Contract：`/health` 返回 200；核心 API 测试通过；错误率低于阈值；新版本已部署

## 关系

- 挂在 [Task](task.md) 上，约束 [Agent](agent.md)
- 由 [Verification](verification.md) 对照世界状态求值
- 与 [Capability](capability.md) 的自报成功分离：Capability 成功 ≠ Contract 满足
- 受 [Policy](policy.md) 约束（例如禁止降低验证严格度）

## 不变式

1. Agent 可以选择 Diagnose → Fix → Deploy → Health Check，或其他完全不同的路径；**不能**自行宣布「这样也算完成」。
2. 验证是外置的：相对执行方独立，或至少相对「自述成功」独立。
3. 未满足 Contract 时，循环应进入 Continue / Re-plan，而不是静默 Done。

## 演化注记

V1 可用极简 Contract（例如假服务 health 返回成功即 Done）。字段集合应预留 Expected State / Criteria / Method / Evidence，避免日后只能靠硬编码 if 判断完成。
