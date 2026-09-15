# Capability

## 定义

Capability 是系统对外的**能力语义接口**：描述「我能做什么」，而不是「谁来做」或「固定流程里的第几步」。

## 职责

- **负责**：以稳定语义暴露可组合的动作空间（输入、输出、前后条件、副作用、权限、成本等）。
- **不负责**：自主规划整条任务路径（那是 Agent）；绑定唯一物理设备或实现（那是 Provider）。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Name / ID | 稳定语义名，如 `service.health_check`、`camera.capture`、`code_edit` |
| Domain | 所属语义域，如 `software_development` |
| Provider | 具体实现方标识，如 `cursor`、`autonomy`（见 [Provider](provider.md)） |
| Input / Output | 参数与结果的语义 |
| Preconditions | 调用前世界需满足的条件 |
| Postconditions | 成功后期望的世界效应（声明，仍需 Verification） |
| Side Effects | 副作用说明 |
| Permission | 所需权限 |
| Provider(s) | 可绑定的实现方 |
| Cost / Performance | 选择时的代价信号 |

## 关系

- Capability ≠ [Agent](agent.md)：OCR / Git / Camera「Agent」若无自主决策，实质是 Capability Provider
- Capability → 多个 [Provider](provider.md)：同一 `camera.capture` 可对应 iPhone / GoPro / USB Camera
- [Action](action.md) 是某次 Capability 的具体执行
- [Agent](agent.md) 在 [Capability Space](domain.md) 中发现与组合 Capability
- 能力缺口（Capability Gap）可触发寻找 Provider、安装 Skill、创建能力或请求人介入（见原则 Self-Extensible）

## 代码位置与提示词

- 实现：`src/capability/`（`asset_change.go`、`software_development/code_edit.go`、`deployment/monitor.go`），注册在 `capability.RegisterDefaults`。
- `deployment.monitor`：跟随一次部署、发现并解释问题（见 [deployment-monitor.md](deployment-monitor.md)）。
- **提示词不进代码**：委托给 worker 的提示词放在仓库文件里，运行时按次读取 —— 与 agent policy 同一套路（见 `src/prompt.go`）：

| 文件 | 占位符 | 说明 |
|---|---|---|
| `$PROJECT_ROOT/src/agent_policy/AGENT_V2.md` | `{{TASK}}` / `{{RUNTIME_CONTEXT}}` … | planner 的 policy（见 [agent.md](agent.md)） |
| `$PROJECT_ROOT/src/agent_policy/CODE_EDIT.md` | `{{WORKSPACE}}` / `{{GOAL}}` | `code_edit` 委托给 worker 的提示词（见 [delegation.md](delegation.md)） |

改措辞只要改文件、重跑即生效（不用重新编译）；文件缺失时该次委托直接失败（不会先建 agent 再没法 prompt）。

## 不变式

1. Provider 可变，Capability 语义应尽量稳定。
2. Task Owner 面向 Capability Space，而不是设备列表。
3. Capability 报告执行成功，不自动等于 [Completion Contract](completion-contract.md) 满足。

## 演化注记

V1：注册少量固定 Capability（例如 `service.health_check`）即可。注册模型应允许后续动态发现与异构 Provider 排列组合。
