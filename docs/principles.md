# Principles

Autonomy 的设计原则。实现可以逐步填充，原则不应被短期简化推翻。

## 定义

一组约束系统演化方向的不变判断：人定义边界，Agent 对目标负责，能力可组合，完成标准外置可验证。

## 十二条原则

1. **Task First** — Task 是最基本的工作单位。
2. **Goal Driven** — Agent 围绕目标工作，而不是围绕 Workflow。
3. **Completion Contract** — Agent 可自由选路径，不可擅自改写完成标准。
4. **Context over Configuration** — Task 通过 Context 引用世界，而不是把环境整份复制进 Task。
5. **Capability Based** — 系统提供能力空间，而不是提供固定流程。
6. **Dynamic Planning** — 执行计划由 Agent 根据当前状态动态产生。
7. **Delegation** — Agent 可以把工作委托给其他 Agent。
8. **Dynamic Hierarchy** — Agent 层级由任务责任链动态产生，不是预先设计的组织架构。
9. **Event Driven** — 状态变化与事件驱动下一轮决策，不是固定 Step 链。
10. **Trust Based** — 能力、结果与历史表现影响未来委托选择。
11. **Self-Extensible** — 能力空间可扩展，核心 Goal → Execution 机制保持稳定。
12. **Human Defines the Boundary** — 人负责目标、完成标准、政策与边界；Agent 负责执行策略。

## 职责

- **负责**：裁定概念冲突时的优先级（例如 Contract 高于 Agent 自述成功）。
- **不负责**：具体 API、存储格式、调度算法。

## 与 Workflow 的关系

Workflow 不消失，但不是系统核心。它可以是：

- 确定性特例（备份 → 升级 → 重启 → 验证）
- 领域因果知识（Build 通常先于 Deploy）
- 动态 Execution Graph 的产物

固定 Workflow 只是动态规划的一种退化形式。

## 演化注记

V1 可以没有多 Agent、Trust、能力自创建；必须保留 Task / Context / Agent / Capability / Delegation / Event / Completion Contract / Policy / Verification 的边界，以便后续自然扩展。
