# project_map — Autonomy 项目地图

本目录按**运行事实**说明本仓库，不是概念词典。

| 文档 | 维度 |
|---|---|
| [positioning.md](positioning.md) | 项目定位：解决什么、不解决什么、和周边系统怎么分工 |
| [architecture.md](architecture.md) | 服务架构：进程、循环、能力、存储、HTTP、外部依赖 |
| [tech-stack.md](tech-stack.md) | 技术栈：语言、库、桥、构建工具 |
| [deployment.md](deployment.md) | 部署细节：谁部署、端口、运行目录、优雅重启、数据落点 |
| [release.md](release.md) | 发布细节：`build.sh` 产物、契约登记、跨平台打包 |
| [development.md](development.md) | 开发流程：工作区、分支、测试、提交边界 |

概念层（Task / Agent / Capability / Verification …）仍在 [`docs/`](../docs/README.md)。`docs/` 回答「这个词是什么」；`project_map/` 回答「这个仓库现在怎么跑、怎么改」。

根目录 [`AGENT.md`](../AGENT.md) 是每个后续 agent 的入口：理解以本目录为准，滞后必须更新，与事实不符必须提出异议。

## 怎么读

1. 先读 [positioning.md](positioning.md) 和 [architecture.md](architecture.md)，再按任务去对应维度。
2. 动手前对照代码：路由表在 `src/http_server.go` 的 `routes()`，能力表在 `src/capability/register.go`，引擎在 `src/db/`。
3. 发现本目录或 `docs/` 与代码不一致时，按 [`AGENT.md`](../AGENT.md) 的「更新」与「异议」规则处理，不要默认文档赢。

## 本轮已核对的异议（2026-09-29，对照 `main` 检出）

这些是写本目录时对照代码发现的滞后，**不是**「文档说了就算」：

1. **多篇 `docs/` 仍写根包下的 `src/sqlite_*.go` / `src/postgres_*.go`。** 引擎实现已迁到 `src/db/`（见 `src/db/doc.go`）。本轮已改 `store.md`、`inbox.md`、`broadcast.md`、`execution-step.md`、`llm-message.md`、`llm-event-stream.md` 里的路径。
2. **`docs/accounts.md` 仍写三个 harness。** `src/llmbackend/all` 已链接 `cursor` / `cline` / `codex` / `claude`。本轮已把账号池文档补上 `claude`。
3. **根 `README.md`「Status」与 `docs/README.md`「当前阶段 / 下一里程碑」偏旧。** 仍把项目写成「骨架 + hello 闭环」。事实是：`cmd/autonomyd` 已是带任务 API、数据 API、账号池、自包含 UI、多 harness、优雅重启和发版包装的 runtime。hello 仍在 `cmd/hello`，但它不是系统的主形态。本目录按后者描述；`docs/README.md` 的里程碑段落本轮改为指向现状，避免把后续 agent 锁在过期阶段叙事里。

后续 agent 若发现上述条目已被新代码推翻，删除或改写它们，不要累积过期异议。
