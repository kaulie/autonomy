# Asset

## 定义

Asset 是世界中可被**观察、引用或改变**的对象：代码、制品、运行中的服务、设备、文件、端点等。

## 职责

- **负责**：作为目标状态与能力作用的对象载体（Target Asset + Desired State）。
- **不负责**：自己完成 Task；定义能力语义；充当 Workflow 节点。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Identity | 资产标识 |
| Type | 服务、仓库、文件、设备、端点等 |
| Observable State | 可观察状态投影 |
| Desired State (when targeted) | 任务期望其达到的状态 |
| Context Affinity | 所属或可见的 Context |
| Handles | 访问句柄（地址、路径、ID） |

软件世界的常见状态链（领域知识，非固定 Workflow）：

Code → Build → Artifact → Deploy → Runtime → Start → Running → Health Check → Healthy

## 关系

- [Task](task.md) 常指向 Target Asset + Desired State
- [Capability](capability.md) 作用于 Asset 或以其为输入/输出
- [Context](context.md) 界定哪些 Asset 可见
- [Domain](domain.md) 描述 Asset 类型与合法状态迁移的语义
- [Verification](verification.md) 对照 Asset 的实际状态与 Contract

## 不变式

1. 目标以世界状态表达，优先于「步骤做完了」。
2. Asset 状态应以可观察事实为准，不以执行方口头声明为准。
3. 同一 Asset 可在不同 Task 中成为不同目标。

## 演化注记

V1 hello 可用「假服务」作为一个 Asset，Desired State = Healthy。Asset 模型应足够通用，以覆盖代码、设备、文件等后续类型。
