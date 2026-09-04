# Trust

## 定义

Trust 是系统对 Agent / Provider 的**可委托性评价**：由历史结果沉淀，用于未来的能力选择与委托决策。

## 职责

- **负责**：汇总成功率、失败、超时、耗时、输出质量等信号，形成可比较的声誉/信任度。
- **不负责**：替代 Completion Contract；在单次任务内单方面宣布成功；成为静态组织职级。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Subject | Agent 或 Provider |
| Success / Failure / Timeout Rates | 结果分布 |
| Average Duration | 耗时 |
| Output Quality | 质量信号（可来自 Verification / 人类反馈） |
| Reputation / Trust Score | 综合可委托性 |
| Sample Window | 统计范围 |

闭环：

Task → Delegate → Execute → Verify → Evaluate → Reputation Update

之后 Task Owner 可按 Capability + Trust + Cost + Availability + Performance 选择 Provider。

## 关系

- 影响 [Delegation](delegation.md) 与 [Provider](provider.md) 选择
- 输入来自 [Verification](verification.md) 与 [Event](event.md)
- 从属于 [Agent](agent.md) Network 的协作质量，而不是 Workflow 配置项

## 不变式

1. Trust 指导选择，不改写完成标准。
2. 无历史时使用中性先验 + Policy，而不是不可用。
3. 评价应尽可能基于验证后的世界结果，而不是仅基于自报成功。

## 演化注记

V1 **可以不实现** Trust System；本概念仍单独成文，避免日后把「选哪个工具」写成不可扩展的硬编码表而无评价钩子。
