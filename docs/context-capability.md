# Context Capability — Agent 可调用的 Context 管理能力

本文回答两件事：

1. **现在暴露给 Agent 调用的 context 管理能力有哪些**（契约、边界、前置条件）。
2. **Agent 会怎样来实现这些能力**——这里的「实现」不是写检索代码，而是**任何一只 agent**（planner 或 worker，只是分工）怎么调用、怎么串联、怎么停、不够时怎么 `need_input`。

概念层的 Context（「我在哪个世界」）见 [context.md](context.md)。`context_ref` 的被动解析见 [context-builder.md](context-builder.md)。检索基础设施见 `src/context` 与 [architecture.md](../project_map/architecture.md) 的 Context Service。设计真源是 `/Users/gaolei/agent-policies/context_service.md`（V1）。

---

## 1. 边界：谁暴露、谁调用、谁实现

三层不要混：

| 角色 | 做什么 | 不做什么 |
|------|--------|----------|
| **Context Service**（`src/context`） | 登记、切段、建索引、全文检索、按 id 取 section | reasoning、选文档、写计划、当 Agent Memory |
| **Runtime**（`contextcap` + `RegisterDefaults`） | 把 Service 的读面登记成 capability，出现在 Constructs；谁调用都走同一份 Service | 替 agent 决定查什么、读哪一段 |
| **Agent（planner 或 worker）** | 决定是否查询、查什么、读哪些结果、信息是否已经够用；按 Constructs 调用这三件 | 自己实现 SQL / 解析 / 索引；用全盘文件系统 grep 代替 discovery；直接打 HTTP 运维面 |

所以：**能力是 Runtime 暴露的；检索是 Context Service 做的；discovery 是 Agent 的工作。** Planner 和 worker 只是分工（谁在规划、谁在改代码），**不是两套 context 机制**。谁缺项目知识，谁就走同一套 `context.list` / `search` / `get`，不够再 `need_input`。

Agent「实现」这些能力 = 按 Constructs 的 input/output 调用，而不是再发明一套读文档的机制，也不是把 discovery 独占给 planner。

V1 原则（来自设计 spec）：

- Project 保存的不是「要塞进 Prompt 的全文」，而是一个**可发现、可寻址、可版本化**的 Context Space。
- Task 启动时只给最小 bootstrap（身份 + 发现能力），不灌入整个项目。
- Source of Truth 是 Markdown / Git；索引可以随时重建。
- Project Context ≠ Agent Memory。客观共享知识走 Context Service；主观学习不进这里。

---

## 2. 当前暴露给 Agent 的能力：三件套

注册点：`src/capability/register.go` → `context.search` / `context.get` / `context.list`。  
适配器：`src/capability/contextcap`。Domain = `context`，Provider 归属 `autonomy`（planner 看不到 provider，step 只写 capability + input）。

这三件都是**只读 discovery**。写路径（登记、同步、删除）不是 capability，见 §3。

`Deps.Context` 为空时（sqlite engine、或 store 未实现 `ContextStore`）三件仍会登记进 Constructs，调用时返回类型化错误 `INDEX_FAILED`：`context service is not configured`。Agent 应把这当成「检索基础设施不可用」，不要当成「项目没有文档」或「缺一个新 capability」。

### 2.1 `context.list` — 清单，不带正文

**用途。** 先看这个 project 登记了哪些资源，再决定搜什么。返回库存，不返回内容。

| | |
|---|---|
| 输入 | `project_id`（别名 `project`，必填） |
| 输出 | `resources`：JSON 数组 `{id, name, type, uri, revision, source_type}`；另有 `count`（调用元信息，下游不要当产出绑定） |
| 错误 | `PROJECT_NOT_FOUND`（没给 project_id）/ `INDEX_FAILED` |

`type` 为 `document` / `repository` / `service`。V1 只有 `document` 会切出 section、能被 search/get 读到正文；另外两类只出现在清单里。

### 2.2 `context.search` — 候选 section，不是整篇文档

**用途。** 按词检索一个 project 的已索引 section，返回**候选**（标题、snippet、分数、revision），让 Agent 判断要不要 `context.get`。

| | |
|---|---|
| 输入 | `project_id`（别名 `project`，必填）；`query`（别名 `q`，必填，至少一个可检索词）；`limit`（默认 10，最大 50）；`resource_types` / `types`（逗号分隔：`document,repository,service`）；`metadata`（JSON 对象，资源 metadata 必须全部命中） |
| 输出 | `results`：JSON 数组 `{resource_id, section_id, title, heading, snippet, score, revision}` |
| 错误 | `PROJECT_NOT_FOUND` / `INVALID_QUERY`（空 query、limit 不是数字、metadata 不是 JSON 对象）/ `INDEX_FAILED` |

检索策略是 **metadata 过滤 + 词法全文**（PostgreSQL GIN / 内存实现同一套打分：词频 + heading 加权）。没有 embedding、没有 LLM 摘要。工程文档里的专有名词（Runtime、CompletionContract、GoalType）靠精确词命中。

Search **默认不返回整篇**。Agent 必须再走 `context.get`。

### 2.3 `context.get` — 取一段的全文与出处

**用途。** 在 search 挑中候选之后，取**一个** section 的完整内容与 provenance。

| | |
|---|---|
| 输入 | `section_id`（必填，来自 search 结果）；`resource_id`（可选交叉校验：section 不属于该资源则 `RESOURCE_NOT_FOUND`） |
| 输出 | `resource_id`、`section_id`、`heading`、`content`、`revision`、`source`（JSON：type / location / repository / path） |
| 错误 | `RESOURCE_NOT_FOUND` / `INVALID_QUERY`（缺 section_id）/ `INDEX_FAILED` |

`revision` + `source` 是出处：这段是从哪、在哪个版本被索引的。V1 还没有把「本次 Task 读过哪些 revision」自动记成 TaskContext；要留下证据，调用方应把 get 的输出写进 `evidence` / `previous_actions` / worker 报告，而不是假设 Runtime 会替它记账。

---

## 3. 相邻、但不是 Agent Capability 的面

把这些当成「世界怎么被准备好」，不要写进 plan 当 capability。

### 3.1 被动注入：`context_builder` → `CONTEXT_ENTITY`

每个决策周期、渲染 prompt **之前**，Runtime 解析 task 的 `context_ref`（`{"project":"…"}` 或 `{"task":"…"}`），把身份写进 delta 的 `context_entity`：project 名、仓库、所属组织、该组织登记的 services。失败只让 prompt 变薄，不让 cycle 失败。

这是 **bootstrap，不是 discovery**：

- 给的是「你在哪个世界」的稳定身份，不是文档正文。
- `project_id` 从这里来，供 `context.list` / `context.search` 当字面量输入。
- 关 builder（`AUTONOMY_CONTEXT_BUILDER=0`）时退回本进程注册过的容器描述。

政策已经写明：软件开发任务在项目还没进 World 时，**完整路径可以从 orientation（`context.search` / `context.list`）开始**；空 World 不是缺 capability，不要因此 `blocked`。

Worker（`code_edit` / `deployment.monitor`）的 frame **同样**带 `CONTEXT_ENTITY` 和 `{{CONSTRUCTS}}`（含 `context.*`）。缺项目知识时 worker 走同一套三件套，不另开一条「在工作区里全局 grep 来发现」的路。分工只是：planner 常在派发 `code_edit` 之前先读约定；worker 在改代码中途发现还缺约定时，自己再 `search` / `get`。

### 3.2 运维写路径：HTTP / CLI / UI

这些是给人（或运维脚本）准备 Context Space 的，**不是 capability**：

| 入口 | 作用 |
|------|------|
| `POST /api/context/resources` | 登记资源（不建索引） |
| `POST /api/context/resources/{id}/sync` | 读源 → 按 Markdown 标题切段 → 写全文索引 |
| `GET /api/context/resources?project_id=` | 清单（运维 UI 用，语义同 `context.list`） |
| `GET /api/context/resources/{id}` | 取一个资源的登记信息（**没有**对应 capability） |
| `DELETE /api/context/resources/{id}` | 删登记与索引（不碰源文件；**没有**对应 capability） |
| `GET /api/context/search` | 运维 UI 的检索 |
| `GET /api/context/sections/{sectionID}` | 运维 UI 的读段 |
| `GET /context` | 自包含运维页 |
| `autonomy context register \| sync` | 与 POST 同一条路 |

Sync 的 `status`：`indexed` / `unchanged`（checksum + revision 未变不重建）/ `skipped`（`repository` / `service` 在 V1 只登记）。源路径是 **runtime 所在主机**上的文件或目录。

Agent **不要**计划 `context.register` / `context.sync` / `context.delete`：Constructs 里没有它们。空间是空的，应报告「没有可检索的资源」或 `need_input`（请人登记），而不是发明写能力。

### 3.3 Service 有、Capability 没暴露的

`ContextService` 上还有：`Register` / `Sync` / `GetResource` / `DeleteResource`。只有 Search / GetSection / ListResources 接到了 `context.*`。

因此 Agent **不能**：

- 按 `resource_id` 一次取回整篇文档（只能按 section 取）；
- 登记、同步、删除资源；
- 绕过 capability 去打 SQL 或假设存在 `context.get_resource`。

### 3.4 另一套「context」：会话预算

控制面（agent-control-plane）有任务简报裁剪、历史携带、digest / compaction、ContextMeter——那是 **LLM 会话窗口** 的管理，由平台在注入 prompt 时处理，**不是** Autonomy 的 capability，任何一只 agent 都不能当 Constructs 来调。两套词都叫 context，职责相反：一个是「项目知识怎么被发现」，一个是「这轮 prompt 塞得下多少」。

---

## 4. Agent 怎样实现这些能力（planner 与 worker 同一套）

### 4.1 总原则

1. **只从 Constructs 调用。** 调用 = capability + 它声明过的 input。不要发明 `context.read_all`、`docs.grep`、`rag.query`。
2. **Planner 和 worker 用同一套机制。** 角色只是分工：planner 把读到的约定写进后续 step；worker 在实现中途同样可以 `list` / `search` / `get`。禁止把 discovery 写成「只有 planner 能做」。
3. **文件系统默认只搜自己的 workspace。** 每只 agent 的 workspace 是 `{account_root}/{agent-name}/`（见 `{{CONSTRAINTS}}` 的 `workspace`）。Grep / Glob / 读文件默认不得越出这个目录：不搜其它 agent、不搜 runtime、不搜宿主工程树。项目知识仍走 `context.*`；workspace 里没有、索引里也没有，就 `need_input`，不要把搜索范围扩大到 workspace 外。
4. **输入必须有来源。** planner 的 plan step 只有三种：字面量、`{"source":"step:<name>.output.<key>"}`、`{"source":"world_model:asset.…"}`。worker 从同一份 `CONTEXT_ENTITY` 抄 `project_id`。Runtime 不会猜。
5. **一次正经的零命中就问人。** `INVALID_QUERY`（空 query、坏 metadata）是调用方写错了，改请求再搜一次，不要把修 query 推给人。`list` 没有 document、或一次用任务术语发出的 `search` 返回空结果：索引里没有这份知识，立刻 `need_input`。不要仪式性地换词再搜两轮——那只会推迟问人，也掩盖「资源根本没登记」。
6. **够用就停。** 已经能做下一步该做的事（planner 能写 `code_edit.instruction`，worker 能改对文件）就停。不要把项目文档读完。

### 4.2 标准闭环

```text
CONTEXT_ENTITY.project.id
        │
        ▼
  context.list          看库存：有没有文档？哪些只是 repo/service 占位？
        │
        ▼
  context.search        用 Task 里的术语做 query，拿候选 section
        │
        ▼
  判断 snippet          挑 1～N 个真正相关的 section_id
        │
        ▼
  context.get           绑定上一步的 section_id（同计划内）或上一轮的字面量
        │
        ▼
  工作上下文够了？
     ├─ INVALID_QUERY → 改请求再 search 一次
     ├─ list 无 document，或一次正经 search 零命中 → need_input
     ├─ 缺口只是本 workspace 里已有代码的实现细节 → 只在自己的 workspace 里搜
     └─ 是 → 停止 discovery，做本职（planner 派发下一步 / worker 改代码）
```

同一份 plan 里可以把 list → search → 一次 get 串起来。**一次 get 只取一段**；要读多段，就写多个 get step，或先 search、下一 cycle 再按 `previous_actions` 里的 `section_id` 逐个 get。不要指望一个 step 返回「所有相关文档」。

Search 的 `results` 是 JSON 数组。Runtime **不会**从数组里替你挑 `section_id`（「不做隐式聚合」）。因此：

- **同 cycle 里** search 之后立刻 get：planner 还没有具体 `section_id`，不能合法绑定 `step:search.output.results` 当 `section_id`。正确做法是 **这一轮只跑 list/search**，下一轮把选中的 id 当字面量传给 get。
- 或者，若清单已经够小、Task 点名了某一节，planner 可以在 list 之后直接 search，再在再下一轮 get。

这是当前契约下最重要的实现约束：**search → get 的「挑选」发生在两次调用之间，而不是 Runtime 内部。** Planner 表现为两个 Decision Cycle；worker 表现为先看 `results`，再带着选出的 `section_id` 调 `context.get`。

### 4.3 输入从哪里来

| 入参 | 来源 |
|------|------|
| `project_id` | `CONTEXT_ENTITY` 里这条 task 的 project id（字面量）。`context_ref` 指向另一条 task 时，用展开出来的那个 project，不是 task id。 |
| `query` | Task 描述与 Goal 里的术语（字面量）。优先专有名词、接口名、标题用词，而不是整段需求复述。 |
| `limit` | 字面量；默认即可。需要更宽再加大，不超过 50。 |
| `resource_types` | 只要文档时写 `document`，避免把未索引的 repo/service 期望成正文。 |
| `section_id` | 上一 cycle 的 search 结果（字面量），或（若将来有单值输出）同计划内更早 step 的 output。 |
| `resource_id`（get 的交叉校验） | search 同行的 `resource_id`，防止拿错段。 |

`task_id` 不是这三件的入参。Runtime 不会因为「当前 task 在这个 project」就补上 `project_id`。

### 4.4 错误怎么分支

按类型化错误决定下一步，不要按散文：

| 错误 | Agent 应做什么 |
|------|----------------|
| `INVALID_QUERY` | 改 query / 补 project_id / 修正 metadata JSON；不要重试同一个坏请求 |
| `PROJECT_NOT_FOUND` | 检查 `CONTEXT_ENTITY` 是否真有 project；没有则 `need_input`，不要编一个 id |
| `RESOURCE_NOT_FOUND` | section 已不在索引，或 `resource_id` 交叉校验失败 → 重新 search，不要死磕旧 id |
| `INDEX_FAILED` / not configured | 基础设施不可用（常见：sqlite）。报告事实；不要假装搜过、也不要改去打运维 HTTP |
| `SOURCE_UNAVAILABLE` / `PARSE_FAILED` | 只出现在运维 sync，不会从这三件 capability 冒出来 |

零命中（成功但 `results` 为空）不是基础设施错误，而是「这份知识不在 Context Space 里」。进入 §4.5 问人登记或粘贴。不要再换词搜一轮，也不要把文件系统搜索扩到自己的 workspace 之外。

### 4.5 `need_input`：search 仍不够时请人补，然后重试

`need_input` 收束本轮；用户下一条 `POST /api/tasks` 进同一只 agent 的 inbox，带着 `runtime_context.additional_input` 再 decide。不必新造状态机。

不要用 `need.type: capability` 要一个「全盘 grep」或新的读文档能力。用人已经有的类型：

- `resource`：索引里没有这份知识。请人在运维面登记并 sync 一篇文档，或指出应登记的路径。
- `information`：请人把原文贴进下一条指令。
- 不要用 `need_input` 申请「到 workspace 外去 grep」。默认搜索范围就是自己的 workspace；缺的知识走 Context Service 或人贴进来。

`need.description` 必须写清：缺什么、已经试过的 list/query 与结果、人可以怎么补、补完后重试哪一步。缺任何一项，下一轮还会猜。

Worker 不走 Decision Output Schema 时：把同样四件事写进本次委托的报告并失败/停住，让本 task 的 planner 发出 `need_input`。机制仍是同一套，只是由谁落 `type: need_input` 这一笔。

`INDEX_FAILED` / Context Service 未配置，也走 `need_input`（`type: resource`），说明是基础设施不可用。

### 4.6 与 World、`code_edit`、完成契约

- Orientation **不改变 World**。`expected_effect` 可以为空；不要因为 World 仍是 `assets: []` 就认为 list/search 失败。
- 软件开发任务的完成锚点仍是 `code_edit.pr_url`（kind `pull_request`）+ `pr.check`。`context.get` 的 `content` **没有 kind**，不能当 Completion Contract 的 evidence 槽。
- 读到的正文用来**写好**后续工作（planner 写 `code_edit.instruction`，worker 按约定改文件），而不是把整段 content 再塞进 World。
- 文件系统阅读只发生在**自己的 workspace**（通常是 clone 进来的那份代码和它的 `AGENT.md` / `project_map/`）。跨仓库 / 服务 / ADR 的项目知识走 Context Service。两者叠用，不是互相替代，也不是把搜索范围扩到别人的目录。

### 4.7 Worker 怎么真正调到这三件

Worker 的提示词已经带 `{{CONSTRUCTS}}`（含 `context.list` / `search` / `get`）和 `CONTEXT_ENTITY`。政策要求它按 Constructs 调用，不要另做一套。

今天 planner 经 Decision Cycle 的 `Execute` 跑这些能力；worker 的 harness 默认是编辑器工具。要让「都可以用」在运行时成立，worker 会话必须能 **Run 同一份 `contextcap`**（同一 Context Service、同一个 `project_id`），而不是改去打运维 HTTP，也不是用 `rg` 冒充 search。接线可以后做；在接到之前，worker **仍然禁止**搜自己 workspace 以外的目录——报缺口，升到 `need_input`。

### 4.8 示例：两轮 orientation 再执行

第一轮 Decision Cycle（只发现，不改世界）：

```json
{
  "type": "plan",
  "plan": [
    {
      "name": "inventory",
      "capability": "context.list",
      "inputs": { "project_id": "project-749a0238" },
      "expected_effect": {},
      "evidence_refs": ["E1"]
    },
    {
      "name": "find",
      "capability": "context.search",
      "inputs": {
        "project_id": "project-749a0238",
        "query": "heartbeat protocol",
        "limit": "10",
        "resource_types": "document"
      },
      "expected_effect": {},
      "evidence_refs": ["E1"]
    }
  ]
}
```

第二轮（`previous_actions` 里已有 `section_id` / snippet，planner 选出一段）：

```json
{
  "type": "plan",
  "plan": [
    {
      "name": "read",
      "capability": "context.get",
      "inputs": {
        "section_id": "section-003",
        "resource_id": "doc-001"
      },
      "expected_effect": {},
      "evidence_refs": ["E2"]
    }
  ]
}
```

第三轮：用读到的约定写 `code_edit`。不要为了「再确认一下文档」把已经满足的 get 重跑一遍。

---

## 5. 运行前置（Agent 需要知道的事实）

- **Store 必须是 postgres。** sqlite 不实现 `ContextStore`，三件 capability 登记但不可用。
- **资源必须先被运维 register + sync。** 未 sync 的 document 出现在 list 里，但 search 没有 section。
- **源文件在 runtime 主机上。** Agent 工作区里的一份 Markdown ≠ 已索引的 Context 资源。
- **修订检测：** git 源用 commit SHA，文件源用内容 SHA256；未变不重建。
- **权限（V1）：** spec 要求 Search/Get 过 Identity → Project Scope → Resource Access；当前实现按 `project_id` 隔离数据，没有把 ACL 散进 search。Agent 不得假设「知道 `section_id` 就等于有权读任意项目」。

---

## 6. V1 明确不做

不要在计划里依赖、也不要当缺口去发明：

- Vector DB / embedding / hybrid search
- 索引期 LLM 摘要
- 知识图谱 / 自动本体
- Agent Memory
- 自动爬 GitHub / 定时同步 / webhook
- `context.register` / `sync` / `delete` / `get_resource` 作为 capability
- Planner 在派发前替 Agent 找齐所有文档
- 把整个 Project Context 一次性注入 prompt

---

## 7. 后续方案（只记方向，不写代码）

按对 Agent 编排的影响排序，而不是按基础设施胃口：

1. **Search 结果的可绑定形状。** 今天 `results` 是数组，planner 无法在同计划内把「第一名的 section_id」绑给 get。若要同 cycle 闭环，需要一个显式、单值的产出（例如独立的 `context.pick` 或 search 增加可选的 `select=top` 且输出标量 `section_id`）。在此之前，两轮 cycle 就是正确实现。
2. **把本次 Task 读过的 `(resource_id, section_id, revision)` 记成 TaskContext。** 设计 spec §9 已要求能回答「当时基于哪个版本」；现在只能靠 planner 自己把 get 输出写进 evidence。
3. **空索引的可观察信号。** list 返回 `count=0` 与「service 未配置」应继续保持不同类型，避免 Agent 把基础设施故障当成「项目没有文档」。
4. **不要把 register/sync 做成 capability。** 写路径碰的是 runtime 主机文件系统与索引生命周期，应留在运维面；Agent 缺资源时 `need_input`。
5. **embedding 只能加在 Service 后面。** Agent 面的三件名字与「search 返回候选、get 取一段」应保持稳定；换检索实现不应迫使任何一只 agent 改调用方式。
6. **Worker 调用入口。** 给 worker 会话一条 Run `context.*` 的路（同一 `contextcap`），这样「planner / worker 同一套」不只写在政策里。

---

## 8. 对照清单

| 问题 | 答案 |
|------|------|
| Agent 现在能调用哪些 context 管理能力？ | 只有 `context.list`、`context.search`、`context.get` |
| 谁真正做检索？ | Context Service；Agent 只派发 |
| `project_id` 谁填？ | 调用方从 `CONTEXT_ENTITY` 抄成字面量 |
| 能不能同一步从 search 绑到 get？ | 不能；挑选发生在两次调用之间 |
| 空 World 要不要 blocked？ | 不要；先 orientation |
| 登记文档是不是 Agent 的事？ | 不是；HTTP / CLI / `GET /context` |
| Worker 会不会调这三件？ | 会；和 planner 同一套，只是分工不同 |
| 文件系统默认搜哪里？ | 只搜自己的 workspace；search 不够就 `need_input` |
| 会话裁剪、digest 算不算？ | 不算；那是控制面的窗口管理 |
