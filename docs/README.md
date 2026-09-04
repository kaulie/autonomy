# Autonomy Concepts

Autonomy 的核心不是 Workflow Engine，而是一套可演化的 ontology：

**Context**（我在哪里）→ **Task**（要完成什么）→ **Agent**（谁负责）→ **Capability**（能做什么）→ **World State** → **Event** → 再决策 → **Completion**。

本目录把每个概念单独成文，只定义边界与不变式，不规定具体实现。

## 阅读顺序

1. [principles.md](principles.md) — 十二条原则
2. [task.md](task.md) / [completion-contract.md](completion-contract.md) — 工作契约与完成锚点
3. [context.md](context.md) / [project.md](project.md) — 世界与命名空间
4. [agent.md](agent.md) / [delegation.md](delegation.md) — 责任主体与委托
5. [capability.md](capability.md) / [provider.md](provider.md) / [action.md](action.md) — 能力空间
6. [asset.md](asset.md) / [domain.md](domain.md) — 世界中的对象与语义空间
7. [event.md](event.md) / [verification.md](verification.md) / [policy.md](policy.md) — 观察、验真、边界
8. [runtime.md](runtime.md) / [execution-loop.md](execution-loop.md) — 执行与主循环
9. [trust.md](trust.md) — 信任与选择（可后置实现）

## 终局四对象

| 对象 | 问题 |
|------|------|
| [Context](context.md) | 我在哪里？ |
| [Task](task.md) | 我要完成什么？ |
| [Agent](agent.md) | 谁负责把它完成？ |
| [Capability](capability.md) | 这个世界里可以做什么？ |

## 当前阶段

从零到一：先定骨架与接口边界。V1 允许「一个 Task + 一个 Owner Agent + 若干固定 Capability」；架构上不得锁死为单 Agent 或固定 Workflow。

## 下一里程碑（代码，不在本目录交付）

可运行的 hello 闭环场景预定为：**对假服务做 health check**（假服务 → Owner 调用固定 `service.health_check` → 按 Completion Contract 验证 → Done / Continue）。
