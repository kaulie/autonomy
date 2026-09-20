# Project

## 定义

Project 是人类理解和组织世界的一种 **Context**：Context Container / Context Namespace。

它**不是** Task 的生命周期父对象。

## 职责

- **负责**：聚合某个产品或系统相关的 Repository、Service、Environment、Deployment、Runtime、Knowledge、Resources、Policies。
- **不负责**：规定 Agent 的执行步骤；拥有 Task 的创建/关闭生命周期主权；替代 Completion Contract。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Identity | 项目标识 |
| Repository | 代码与制品来源 |
| Services | 相关服务与端点 |
| Environments | 部署与运行环境 |
| Runtime / Deployment | 运行与发布相关引用 |
| Knowledge | 项目域知识 |
| Policies | 项目级边界 |

当人说「把这个项目部署一下」，自然语言已隐含上述世界信息；Project 就是把这些隐含变成可解析 Context。

## 关系

- Project **是一种** [Context](context.md)
- [Task](task.md) 通过 `context_id`（或等价引用）关联 Project，而不是「挂在 Project 子树下才能存在」
- Project 为 [Agent](agent.md) 提供解析 [Asset](asset.md) / [Capability](capability.md) 的范围

## 运行时读到什么

一条 task 用 `context_ref`（`{"project": "<id>"}`）说自己在哪个 project 里。这个 id 的名字、仓库与**所属组织（部门）**
不重新在 autonomy 里登记一份：它们是平台的注册表事实 —— 控制面 `GET /api/projects` 答 `{projectId, name, gitRepoUrl, department{departmentId, departmentName}}`
（组织目录本体在 organization 服务，控制面存的是「这个 project 属于哪个部门」）。本进程自己注册过的 context container 只补充
它知道的部分（描述、领域）。

解析这件事只有一个实现：**context builder**（[context-builder.md](context-builder.md)）—— 每个决策周期在 prompt 之前解析一次，
结果进 prompt 的 `context_entity`；`GET /api/tasks/{id}` 的 `project` / `project.organization` 是同一个解析的另一种读法
（见 [http-api.md](http-api.md)）。注册表不在时字段缺失而不是报错。

## 不变式

1. 禁止把「Project 管理 Task」的人类项目管理习惯，直接建模成 Runtime 的父子生命周期。
2. Project 回答 Where/What World；不回答 How。
3. 没有 Project 也可以有 Task（只要有其他 Context）；有 Project 也不必然拥有固定 Workflow。

## 演化注记

今天可以为了人的组织需要保留 Project UI；架构上必须把 Project 落在 Context 一侧，以便未来出现非 Project 的 Context，而不重构 Task 模型。
