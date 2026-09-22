# Autonomy Concepts

Autonomy 的核心不是 Workflow Engine，而是一套可演化的 ontology：

**Context**（我在哪里）→ **Task**（要完成什么）→ **Agent**（谁负责）→ **Capability**（能做什么）→ **World State** → **Event** → 再决策 → **Completion**。

本目录把每个概念单独成文，只定义边界与不变式，不规定具体实现。

## 阅读顺序

1. [principles.md](principles.md) — 十二条原则
2. [task.md](task.md) / [completion-contract.md](completion-contract.md) — 工作契约与完成锚点
3. [context.md](context.md) / [project.md](project.md) — 世界与命名空间
4. [agent.md](agent.md) / [delegation.md](delegation.md) — 责任主体与委托
5. [session.md](session.md) — 一只 agent 与 LLM 的会话（planner 与 worker 跑同一套）
6. [capability.md](capability.md) / [provider.md](provider.md) / [action.md](action.md) — 能力空间
7. [asset.md](asset.md) / [domain.md](domain.md) — 世界中的对象与语义空间
8. [event.md](event.md) / [verification.md](verification.md) / [policy.md](policy.md) — 观察、验真、边界
9. [runtime.md](runtime.md) / [execution-loop.md](execution-loop.md) — 执行与主循环
10. [trust.md](trust.md) — 信任与选择（可后置实现）
11. [llm-event-stream.md](llm-event-stream.md) — LLM 事件流持久化（reason_turns run header + llm_events 原始流，原始流默认不写）与多 provider 扩展
12. [llm-message.md](llm-message.md) — 输入与返回拆成两条独立记录（llm_messages），返回溯源到具体输入
13. [store.md](store.md) — 存储统一接口与可插拔 engine（sqlite 为默认实现）
14. [http-api.md](http-api.md) — 对外 HTTP：接受任务、查进展、查 agent 工作状态、轮询增量对话流；以及**数据 API**（把日志当数据读，评测侧不再直接读库）
15. [deployment-monitor.md](deployment-monitor.md) — `deployment.monitor` 能力参考（跟随部署、定位问题；概念边界见 [capability.md](capability.md)）
16. [inbox.md](inbox.md) — 每个 agent 的消息队列：用户 / 别的 agent / runtime 发的消息，按到达顺序一条条处理
17. [execution-step.md](execution-step.md) — 计划与执行两层记录（`execution_plan` / `execution_step_plan` / `execution_step` / `execution_step_interaction`）：计划先写、不可改、结果派生，且按 message id 追溯到是哪条回复、哪条输入
18. [context-builder.md](context-builder.md) — `context_ref` 的解析器（独立模块）：在 prompt 之前把引用解析成世界（本进程的容器 + 平台的 project / organization / service / task 注册表），失败不致命
19. [broadcast.md](broadcast.md) — 广播：一句话投递给某个 project 下的、或所有 project 下的所有 agent（`AcceptTask` 的复数，消息本身还是一条普通指令）
20. [llm-backend.md](llm-backend.md) — LLM 后端（cursor / cline）与怎么切换：后端是 runtime 进程的设置（`AUTONOMY_LLM_BACKEND`），`GET /health` 报当前后端，本地 / 部署两条切换路径
21. [local-replica.md](local-replica.md) — 本地副本（写远端、读本地）：把远端主库的一份 streaming replica 立在本机，观察类的读走本地、写与「读完就写」的读走远端
22. [graceful-restart.md](graceful-restart.md) — 优雅重启：部署平台重启前先通知（`POST /api/ops/restart-notify`）、再轮询（`GET /api/ops/restart-status`）；不再启动新 run、在途 run 跑完、被 hold 的指令由新进程接着跑，进程收到 `SIGTERM` 也走同一条路
23. [agent-monitor.md](agent-monitor.md) — agent 实时状态监控面板：只读聚合 `GET /api/agents`、SSE 推送 `GET /api/agents/stream`、单页面板 `GET /monitor`；把 agent 行 / factory / task / inbox / 在途 run 投影成 running \| idle \| blocked \| done

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
