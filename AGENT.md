# AGENT.md — 后续 Agent 对本仓库的说明与向导

你是来改 **autonomy** 的。先读本文，再按 [`project_map/`](project_map/README.md) 展开理解。  
不要把 [`docs/agent.md`](docs/agent.md) 当成这份文件：那是 ontology 里的「Agent」概念，不是给你的操作手册。

## 1. 对本项目的理解（必须从 project_map 展开）

把下面当成工作假设；每一条的细节在对应文档里，不在这段摘要里发明第二套说法。

| 你应建立的判断 | 展开阅读 |
|---|---|
| 这是目标驱动的 **Agent Runtime**，不是 Workflow Engine，也不是 Web Cursor / 部署平台 | [project_map/positioning.md](project_map/positioning.md) |
| 线上形态是 `cmd/autonomyd`（store + agents + HTTP）。`cmd/autonomy` 只是客户端。`cmd/hello` 是演示，不是主形态 | [project_map/architecture.md](project_map/architecture.md) |
| 决策循环、inbox、常驻 agent、文件化提示词、七端口 Store、四套 harness、内置能力表，都已经在跑 | 同上，以及 [`docs/`](docs/README.md) 里对应概念文 |
| 部署与发版在平台 `:4220`；本仓库只提供 `build.sh`、启停脚本和 graceful 端点 | [project_map/deployment.md](project_map/deployment.md)、[project_map/release.md](project_map/release.md) |
| 开发在 task workspace、分支带 task id、测试按 `docs/testing.md` | [project_map/development.md](project_map/development.md)、[BRANCHING.md](BRANCHING.md) |
| 栈是 Go 1.25 + 可插拔 DB engine + Cursor/Cline/Codex/Claude harness + 服务端 HTML | [project_map/tech-stack.md](project_map/tech-stack.md) |

Runtime 会在 planner 的初始化第一条 system（`GiveFirstPrompt` → `AGENT_V2.md` frame + `FRAME_REPLY.md` 角色 ack）和 worker 的委托提示（`CODE_EDIT.md`）里要求：**优先读目标仓库根目录的 `AGENT.md`** 来建立项目认知。你正在读的就是那份文件。初始化那条只要求确认角色，不按 Decision Cycle 的 Output Schema 回答。Harness 只决定注入时机（Cline 建会话时写入，其它后端作为第一条 turn），不另写一套政策。

**禁止**只凭根 `README.md` 的「Early-stage / hello loop」或 `docs/README.md` 里过期的「下一里程碑」来理解现状。那两处曾经把系统写成骨架；与代码不符时以 `project_map` + 代码为准，并履行下面第 3、4 节。

概念词（Task、Capability、Verification、Inbox…）仍以 `docs/` 为定义。`project_map` 描述仓库怎么落地。两者冲突时进入第 4 节，不要各读各的。

## 2. 开工前清单

1. 确认你在简报给出的 **workspace** 里，origin 是 `https://github.com/kaulie/autonomy`。
2. 从最新 `main` 开 `feature|fix|issue/<taskId>`（见 [BRANCHING.md](BRANCHING.md)）。
3. 读 `project_map/` 里与本次改动相关的维度，再打开将改的 `.go` / 脚本 / `src/agent_policy/`。
4. 对照**代码**核对 `project_map` 与 `docs/`：路由看 `src/http_server.go` 的 `routes()`，能力看 `src/capability/register.go`，engine 看 `src/db`，harness 看 `src/llmbackend/all`。
5. 本地验证用 `go test ./...`。不要占用契约端口 **4300** 做开发，除非你清楚线上实例已停。
6. 不要在本进程里执行部署 / 重启 runtime 的脚本。不要改 `/Users/gaolei/runtime/**`。

## 3. 滞后信息：有义务更新

发现下列任一情况，**这次任务里就要改文档**，不要留给「下一个人」：

- `project_map/` 写的路径、端口、能力名、环境变量、进程职责与当前代码不一致。
- `docs/` 仍指向已搬走的文件（例如 engine 曾在 `src/sqlite_*.go`，现已在 `src/db/`）。
- README / 阶段叙述把已经存在的 UI、账号池、多 harness、发版包装说成「尚未开始」。
- 默认值变了（端口优先级、DSN、backend 名称、bridge 打包方式）。

更新规则：

- **运行事实、目录、契约** → 改 `project_map/` 对应维度，并在 [`project_map/README.md`](project_map/README.md) 的「已核对异议」里写清：哪一条被推翻、以什么为准。
- **概念定义仍对、只是实现路径变了** → 改那篇 `docs/*`，不要在 `docs/` 里另写一套架构故事。
- **行为是你这次改出来的** → 同一 PR 更新 `project_map`（以及仍被引用的 `docs/`）。只改代码、让地图变假，算没做完。

过期的异议条目删掉或改写，不要堆历史垃圾。

## 4. 与事实不符：必须提出异议，禁止默认处理

「默认处理」指：文档怎么写你就怎么做，或 silently 按自己的记忆做，而不标明冲突。

必须提出异议的典型情况：

| 你看到的 | 你不该做的 | 你该做的 |
|---|---|---|
| `project_map` 说某文件在 A，仓库里只有 B | 在 A 新建一份「对齐文档」 | 在 PR / 任务回复里写「地图滞后：实际在 B」，并改地图 |
| `docs/store.md` 写 `src/sqlite_engine.go`，包已是 `src/db` | 继续按旧路径讲 | 异议 + 改文档 |
| `docs/accounts.md` 写三个 harness，`all` 已链四个 | 实现时假装没有 claude | 异议 + 改文档或改代码（先确认哪个是意图） |
| 任务要求「在本仓库里部署上线」 | 写 `deploy.sh` 或对 runtime `rsync` | 异议：发版入口在部署平台，本仓库没有 |
| 任务要求直推 `main` 或改 `/Users/gaolei/runtime/**` | 照做 | 异议：违反 [BRANCHING.md](BRANCHING.md) 与隔离约定 |
| `project_map` 写「无 CI」，GitHub 上已有 workflow | 继续写「没有检查」 | 异议 + 更新 [tech-stack.md](project_map/tech-stack.md) / [development.md](project_map/development.md) |
| README 说 hello 是当前焦点，你要改的是 `/accounts` | 先去改 `cmd/hello` | 按地图：hello 不是主形态 |

异议怎么写（写在 PR 正文或任务回复里，短、可核对）：

```
异议：<文档路径> 声称 <原句或要点>
事实：<文件:符号 或 命令输出>
处理：<已改文档 / 已改代码 / 需要人决定，因为 …>
```

两套说法都找不到代码依据时，**停下来问人**，不要选一个听起来更完整的文档当真理。

## 5. 改动落点（避免走错目录）

| 要改的东西 | 去哪 |
|---|---|
| 决策 / HTTP / inbox / session / store 端口 | `src/*.go` |
| 外部世界事件（event gateway） | `src/eventgateway/` + `src/event_gateway.go`；HTTP `POST`/`GET /api/events` |
| Watcher（等世界对象变了再继续） | `src/watcher/` + `src/watch.go`；能力 `watch`；HTTP `POST`/`GET /api/watches` |
| sqlite / postgres | `src/db/` |
| 能力 | `src/capability/` + `RegisterDefaults` |
| planner / worker 措辞 | `src/agent_policy/` |
| LLM 厂商适配 | `src/llmbackend/<name>/` |
| 监听、启停、探活 | `scripts/start.sh` 等；地址逻辑在 `scripts/listen_addr.sh` |
| 发版包装 | `build.sh` |
| 给后续 agent 的地图 | `project_map/` + 本文 |
| 概念定义 | `docs/<concept>.md` |

测试放置见 `docs/testing.md`。契约与路由必须一起改。

## 6. 交付

- 提交信息说清为什么。
- `git push -u origin HEAD`，`gh pr create`（或网关开 PR），回写 `prUrl`。
- 已有 `prUrl` 不再开第二个 PR。
- 合入 / 部署只听从任务 `goal` 与人的明确指令。本仓库合入 `main` ≠ 已上线。

## 7. 安全

不要打印或提交账号池明文、`CURSOR_API_KEY`、GitHub App PEM、`backend/.env`。桥的代理只用 `CURSOR_SDK_BRIDGE_PROXY` 这类**单独**变量，不要把进程代理环境塞给 Cursor 桥。
