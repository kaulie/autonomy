# Context

## 定义

Context 回答：**我在哪个世界里工作？**

它是 Agent 完成 Task 所需的环境、资源与知识的命名空间，而不是一份塞进 Task 的配置大包。

## 职责

- **负责**：提供 Where / What World——仓库、服务、环境、运行时、知识、资源、适用政策等可解析引用。
- **不负责**：定义要完成什么（那是 Task）；决定怎么做（那是 Agent）；充当 Workflow 容器。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Identity | 上下文标识与类型（组织 / 项目 / 服务 / 环境 / 任务局部等） |
| Resources | 可引用的世界资源（仓库、服务、端点、密钥句柄等） |
| Knowledge | 领域知识与因果约束的入口 |
| Policies | 本世界适用的政策边界 |
| Parent / Composition | 可与其他 Context 组合为 Effective Context |

Effective Context 可以是多层组合，例如：

Organization + Project + Service + Environment + Task-local

## 关系

- [Task](task.md) 持有 Context Ref，而不是内嵌整份世界
- [Project](project.md) 是 Context 的一种常见实现
- [Agent](agent.md) 通过 Context 解析 [Asset](asset.md) 与 [Capability](capability.md) 的可用范围
- [Domain](domain.md) 描述语义空间；Context 提供该空间在当下的具体实例化

## 不变式

1. Context over Configuration：世界信息以引用与解析为主，避免 Task 膨胀为环境快照仓库。
2. Context 告诉 Agent「你在哪」；Task 告诉「做什么」；Agent 决定「怎么做」。
3. 同一 Capability 语义可在不同 Context 解析到不同 Provider。

## 演化注记

V1 可用单一 Project Context。类型上应允许未来出现 Organization / Service / Environment 等层级，而不把「Context = 一个 JSON blob 塞进 Task」写死。
