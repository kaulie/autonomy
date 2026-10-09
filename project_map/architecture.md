# 服务架构

## 进程边界

```
调用方（控制面 / CLI / UI / 评测）
        │  HTTP
        ▼
   cmd/autonomyd          ← 唯一 runtime
        │
        ├─ HTTPServer     src/http_server.go（路由表即契约面）
        ├─ Autonomy       主循环、inbox、恢复、优雅重启
        ├─ Store 端口     src/store.go（七端口并集）
        │     └─ engine   src/db（sqlite / postgres）
        ├─ Capability     src/capability（RegisterDefaults）
        ├─ Context        src/context（Context Service）+ src/capability/contextcap（context.*）
        ├─ LLMSession     src/llm_session.go
        │     └─ harness  src/llmbackend/{cursor,cline,codex,claude}
        ├─ ContextBuilder src/context_builder + src/context_resolver.go
        └─ EventGateway   src/eventgateway + src/event_gateway.go（外部世界事件）
```

`cmd/autonomy` 不链接上述任何一块，只发 HTTP。

## 决策循环（runtime 内）

一次 cycle：

`BuildDecisionContext` → `Decide` → `Execute` → `Record` → `UpdateWorld` → `Verify` / `ShouldTerminate`

- **cycle**：相对某只 agent 的第几轮 prompt→reply（从 1 计）。被截断后的重试不占新 cycle。
- **step**：一份 plan 里的一个能力调用。落库是 `execution_step_plan` / `execution_step`。
- `AUTONOMY_MAX_STEPS` 数的是 planner 自己的 cycle，名字是历史包袱。

Execute 在独立 worker goroutine（`dispatchExecute`）；同 task 仍串行等本轮结果。详见 `docs/execution-loop.md`。

## Agent 与消息

- 每只 agent 有 inbox（`src/inbox.go`）：用户指令、委托 prompt、runtime stop，按到达顺序一条条处理。
- Agent **默认常驻**：一轮结束标 idle，provider 会话留下给下一条消息。进程退出才 `Autonomy.Close()`。
- 接收 `POST /api/tasks` **不**打开 provider 会话——只入队并应答。需要推理的那一轮才开会话。
- 重启后按 `tasks.agent_id` 恢复（`resumeAgentForTask`），不给同一 task 再建一只。上一进程留下 `running` 的消息由该 agent 自己在开机时捡起（`docs/graceful-restart.md`）。

角色：`planner` 做决策轮；`worker` 被 `code_edit` / `deployment.monitor` 等经 `Runtime.AcquireAgent` 取得。会话实现是同一套 `LLMSession`。

## 提示词是文件，不是 Go 字符串

`$PROJECT_ROOT/src/agent_policy/`（部署后 `PROJECT_ROOT` = runtime 目录）：

| 文件 | 用途 |
|---|---|
| `REASONING_FRAME.md` | 初始化：system + policy + 角色 ack（只在 session 的第一次 prompt） |
| `FRAME_REPLY.md` | 初始化回复：`type: ready` + 角色定位，不是决策 |
| `REASONING_DELTA.md` | 每轮：当前 Task / Context / World / Runtime Context |
| `AGENT_V2.md` | policy 正文（frame 携带）。Decision Output Schema 只用于后续 Decision Cycle |
| `CODE_EDIT.md` | `code_edit` worker。同样要求优先读工作区 / clone 根上的 `AGENT.md` |
| `DEPLOYMENT_MONITOR.md` | `deployment.monitor` worker |
| `CONSTRAINTS.json` | 运行时约束（`src/policy.go`），不是写死在 renderer 里的句子 |

改措辞改文件即可；文件缺失则该次委托失败。占位符由 `src/prompt.go` 与 `src/capability/broker` 渲染。`{{CONSTRUCTS}}` 来自已注册能力的 `spec.Declared`（含 input/output），**不含** provider——谁执行是 runtime 的决定。

一只 agent 的生命里只有两次注入顺序：先 frame（`GiveFirstPrompt`），再 task 到达后的 delta。新 session（换 cwd、桥重启、Cursor Create）会再给一次 frame，frame 从不拼进 task prompt。

**第一条 system 定位角色，与 harness 无本质区别。** 各后端只是注入时机不同（`llmbackend.SystemInject`）：Cline 在建会话时写入同一份 frame；Cursor / Claude / Codex 把它当作第一条 turn。不要按 harness 再写一套政策。初始化那条的回复是 `FRAME_REPLY.md` 的角色 ack（`type: ready`），不是 Decision Output Schema，不钉完成契约、不阻塞后续 Decision Cycle。

## 内置能力（`src/capability/register.go`）

| 名称 | 实现 | 形态 |
|---|---|---|
| 资产变更 | `asset_change.go` | 改本进程 world |
| `code_edit` | `software_development/code_edit.go` | 委托 worker（Cursor/Cline/Codex/Claude） |
| `service.deploy` | `software_development/deploy.go` | HTTP 触发部署平台，不等待流水线结束 |
| `pull_request.review` | `software_development/pull_request_review.go` | GitHub REST，**不 merge** |
| `pr.check` | `software_development/pr_check.go` | 系统验真工具：PR 是否存在、状态如何 |
| `pr.watch` | `software_development/pr_watch.go` | 观察一个 PR；合并后经 event gateway 唤醒 agent |
| `deployment.monitor` | `deployment/monitor.go` | 跟随一次部署；可再委托监控 agent |
| `context.search` | `contextcap/search.go` | Context Service：全文 + metadata 过滤检索，返回候选 section |
| `context.get` | `contextcap/get.go` | Context Service：取一个 section 的完整内容与来源 |
| `context.list` | `contextcap/list.go` | Context Service：列出一个 project 已注册的 context 资源 |

## Context Service（`src/context`）

设计见 `/Users/gaolei/agent-policies/context_service.md` 的 V1 范围。`src/context` 是检索基础设施，不做 reasoning：

- **接口**：`ContextService`（`RegisterResource` / `SyncResource` / `Search` / `GetResource` / `GetSection` / `ListResources`）。Planner / Runtime / Agent 只依赖它，不碰 SQL。
- **领域**：`Resource`（`ResourceType` = document / repository / service）、`ResourceSource`、`Section`。Markdown 解析按 heading 切分，带 heading path、order、行号区间。
- **持久化**：`Repository` 端口 + `MemoryRepository`（测试/内嵌）。生产实现在引擎层 `src/db`（`postgres_*.go`）：`PostgresStore.ContextRepository()` 实现根包的 `ContextStore` 端口（`src/context_service.go`），复用同一个 `*sql.DB`，schema 为 `context_resources` / `context_sections` + GIN 全文索引。引擎层之外不 import 驱动（`store_ports_test.go`）；sqlite 未实现该端口，Context Service 保持未配置。
- **同步**：revision（git SHA）/ checksum（SHA256）变更检测，source 未变不重建索引。
- **错误**：`RESOURCE_NOT_FOUND` / `PROJECT_NOT_FOUND` / `SOURCE_UNAVAILABLE` / `PARSE_FAILED` / `INDEX_FAILED` / `INVALID_QUERY`，不转成裸 500。

V1 不含 vector DB / embedding / LLM 摘要 / 知识图谱 / agent memory / planner 集成 / 自动爬取。

`service.deploy` 打 `DEPLOYMENT_API_URL`（默认 `http://127.0.0.1:4220`）的 `POST /api/deploy-notify`，并带 `identity_role` / `identity_id`（默认 `agent` / `autonomy`）。返回 `pipeline_id`，观察走 `GET /api/pipelines/<id>` 或 `deployment.monitor`。

## 存储

- 契约：`src/store.go` 七个端口 —— Task / Agent / Inbox / Conversation / Execution / Verification / TurnQuery。
- SPI：`src/store_engine.go`。上层禁止 import 驱动（`src/store_ports_test.go` 钉死）。
- 实现：`src/db` 包。`cmd/autonomyd` 空白 import `_ "github.com/kaulie/autonomy/src/db"` 完成注册。
- 默认 `AUTONOMY_STORE_ENGINE=sqlite`，DSN：`AUTONOMY_STORE_DSN` > `$AUTONOMY_DATA_DIR/autonomy.db` > `~/database/autonomy/autonomy.db`。全机一份 sqlite 文件：部署 runtime、本机 `go run`、评测工具读同一份。
- `postgres` 是另一份库，**不迁移** sqlite 数据。
- `AUTONOMY_STORE_READ_DSN` 可做读侧副本（`docs/local-replica.md`）；读侧挂了退回主库，不失败。
- `llm_events` 默认不写（叶子表）；`AUTONOMY_LLM_EVENTS=1` 打开。对话在 `reason_turns` + `llm_messages`。

## HTTP 面（`src/http_server.go` `routes()`）

任务 / 控制：

- `POST /api/tasks`、`GET /api/tasks`、`GET /api/tasks/{id}`、`POST /api/tasks/{id}/stop`
- `POST /api/broadcast`
- `GET /api/tasks/{id}/agents/{agentID}`、`…/events`

数据 API（评测侧读日志，不直读库）：

- `GET /api/reason-turns`、`/facets`、`/{turnID}`
- `GET /api/tasks/{id}/turns`、`GET /api/meta`

Agent / 账号 / UI：

- `GET /api/agents`、`GET /api/agents/{id}/messages`
- `/api/accounts` CRUD + vendors/models + verify
- 页面：`GET /`、`/tasks`、`/dashboard`、`/accounts`、`/agents/{id}/events`、`/agents/{id}/messages`

运维：

- `GET /health`（`/healthz` 别名）—— 含 `llm_backend` / 默认账号信息
- `POST /api/ops/restart-notify`、`GET /api/ops/restart-status`

世界事件（event gateway，`src/eventgateway`，见 [event-gateway.md](../docs/event-gateway.md)）：

- `POST /api/events` — 受理一条外部世界事件（写入 World；`subject.task_id` 已有 agent 时入队 observation）
- `GET /api/events` — 列出已受理事件（与 prompt 的 `world.events` 同一份 log）
- `POST /api/watches` / `GET /api/watches` — 观察一个 PR；合并后 ingest `github.pull_request.merged`

契约真源是 handler 上的 swag 注解 + `src/contract_test.go`（注解与路由表必须一一对应）。运行期不 import swag。

## 外部系统（context builder 与能力）

每个决策周期、渲染 prompt 之前，`context_builder` 解析 `context_ref`。注册表失败只让 prompt 变薄，不让 cycle 失败。`AUTONOMY_CONTEXT_BUILDER=0` 可关。

| 依赖 | 默认 | 本进程用途 |
|---|---|---|
| 控制面 projects / tasks | `http://127.0.0.1:4211`（`PROJECTS_API_URL` / `TASKS_API_URL`） | project 名、仓库、所属部门；面板侧 task |
| organization | `http://127.0.0.1:4244` | 部门目录补全 |
| 服务中心 | `http://127.0.0.1:4240` | `GET /v1/orgs/{orgId}/services` |
| 部署平台 | `http://127.0.0.1:4220` | `service.deploy` / `deployment.monitor` |
| GitHub | `GITHUB_TOKEN` / `gh` / GitHub App（`docs/github-app.md`） | `pull_request.review`、`pr.check`、worker 推送 |

本进程自己的 world 容器（`RegisterContextContainer`，`cmd/autonomyd/world.go` 播种 demo）只补充它知道的描述与领域。

那些系统上**已经发生的变化**不经 context builder 拉取，而经 event gateway **推入**：`POST /api/events`（`src/eventgateway`）。Gateway 不知道这些系统；调用方把事实收成 canonical envelope。见 [event-gateway.md](../docs/event-gateway.md)。

## 工作区

账号的 `agent_root_workspace` 必填、绝对路径、账号间互斥。每只 agent：`{root}/{agent-name}/`。  
README 里出现的 `/Users/gaolei/agent-workspace-sandbox/{agent_name}/` 是本机实例的一种配置，不是写死在代码里的全局常量。
