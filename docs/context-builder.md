# Context Builder（把 context_ref 解析成世界）

## 定义

一条 task 用 **引用**说自己在哪个世界：`context_ref: {"project": "project-749a0238"}`（见 [task.md](task.md)、[context.md](context.md)）。
引用只有被解析才有用。`context_builder` 就是这个解析器，而且**独立**：它不知道 task、agent、prompt、runtime，只收一个 `Ref`
（`{"<类型>": "<id>"}`），按顺序问每个 `Resolver`「这个容器你知道什么」。

- 代码：`src/context_builder/`（模块本体 + 平台侧三个 resolver：project / organization / service registry）、`src/context_resolver.go`（runtime 自己的 resolver、接线、挂点）。
- 时机：**每个决策周期、在 prompt 生成之前**（`Agent.decide` 里 `fillContextSections`，见 [execution-loop.md](execution-loop.md)）。
- 结果：注入 prompt 的 `context_entity` 块 —— planner（以及被委托 worker 的 frame）看到的是**解析后的世界**，不是一个 id 字符串。

## 契约

```go
Ref        map[string]string   // {"project": "project-749a0238"}
Resolver   interface { Name() string
                       Resolve(ctx, refType, id string, built map[string]any) (fields map[string]any, ok bool, err error) }
Builder    New(resolvers...).WithTimeout(d).Build(ctx, Ref) Result
Result     { Sections map[string]map[string]any,  // 类型 -> 合并后的字段（含 id/type）
             Errors   []error }                    // 失败被记下，不致命
```

- 每个 `(类型, id)` 按**注册顺序**问所有 resolver，**后者按字段覆盖**；`built` 是前面已合并的结果 —— resolver 可以在别人的答案上继续
  （organization 就是这么拿到 project 的部门 id 的）。
- **`id` / `type` 永远是 task 自己说的那个**：resolver 改不了引用的身份。
- **永不失败调用方**：单次调用有预算（`AUTONOMY_CONTEXT_TIMEOUT`，默认 3s），失败的 resolver 只是「没说」，别人的答案仍然成立，
  失败只进 `Result.Errors`（runtime 打一次 `[autonomy] context: …`，不按周期刷屏）。

## resolver 有哪些

| resolver | 来源 | 贡献 |
|---|---|---|
| `world`（runtime 提供，`src/context_resolver.go`） | 本进程注册的 context container（`RegisterContextContainer`） | `name` / `description` / `domain` —— 只有这个进程知道的那些 |
| `project_registry`（模块提供） | 控制面 `GET /api/projects`（`PROJECTS_API_URL`，默认 `http://127.0.0.1:4211`） | `name` / `git_repo_url` / `organization{id,name}` |
| `organization`（模块提供） | 组织服务 `GET /api/v1/departments/{id}`（`ORGANIZATION_API_URL`，默认 `http://127.0.0.1:4244`） | 补上 `organization{id,name,type}`（部门目录） |
| `service_registry`（模块提供） | 服务中心 `GET /v1/orgs/{orgId}/services`（`SERVICE_REGISTRY_API_URL`，默认 `http://127.0.0.1:4240`） | `organization.services[]` —— 这个组织登记的服务：`name` / `description` / `git_repo_url` / `version` |

顺序即优先级：平台注册表答的 `name` 赢过进程里那份，而进程独有的 `description` / `domain` 留着；部门目录答不了（服务没起）时，
project 注册表里的 `organization{id,name}` 仍然在。部门**成员**故意不注入 —— 谁在这个部门是身份问题（组织服务的地盘），不是每个决策周期都需要。

`service_registry` 是**两跳**，两跳都从引用自己的事实出发：project 注册表（再由部门目录补上 `type`）说这个 project 属于哪个组织，
组织 id 才是服务列表被问的那个东西（`project → organization → services`）。这个组织有什么服务、每个服务的代码在哪，
是服务中心的事实，autonomy 里不再登记一份。service registry 自己的账（`namespace`、`owner`、审计字段）留在它那边 ——
一次决策周期要的是"有这个服务、它是什么、代码在哪"。project 没有组织时没有列表可查：不说，也不是失败。

三个注册表都**读得很省**：成功缓存 30s、失败缓存 5s（按 resolver 实例，服务列表按组织分别缓存），一次 run 的多个 cycle 基本只读一次。

## 环境变量

| 变量 | 作用 | 默认 |
|---|---|---|
| `PROJECTS_API_URL` | project 注册表地址 | `http://127.0.0.1:4211`（控制面） |
| `ORGANIZATION_API_URL` | 组织服务地址 | `http://127.0.0.1:4244` |
| `SERVICE_REGISTRY_API_URL` | 服务中心（服务注册表）地址 | `http://127.0.0.1:4240` |
| `AUTONOMY_CONTEXT_BUILDER` | `0` / `off` / `false` / `no` 关掉整个 builder | 开 |
| `AUTONOMY_CONTEXT_TIMEOUT` | 单个 resolver 的预算（Go duration） | `3s` |

关掉（或进程是手工拼的）时，prompt 退回**本进程自己的世界**：`context_entity` 只有注册过的容器知道的那些，
和没有 builder 之前的行为一致（`src/prompt.go` 的 `contextSectionFields`）。

## 注入到哪里

`DecisionContext.ContextSections`（`src/decision.go`）带着解析结果进入这一轮决策；prompt 的 delta 里渲染成
`context_entity`（帧里的 `{{CONTEXT_ENTITY}}` 标记不变）：

```json
"context_entity": [
  {"id": "project-749a0238", "type": "project", "name": "autonomy",
   "description": "the container's own description", "domain": "software_development",
   "git_repo_url": "https://github.com/kaulie/autonomy",
   "organization": {"id": "D0005", "name": "AI研发部", "type": "研发",
     "services": [
       {"name": "agent-control-plane", "description": "控制面",
        "git_repo_url": "https://github.com/kaulie/agent-control-plane.git", "version": "f8d53f2d"},
       {"name": "autonomy", "git_repo_url": "https://github.com/kaulie/autonomy", "version": "87e2bd3e"}
     ]}}
]
```

组织里登记了哪些服务（以及每个服务的代码在哪）随 `organization.services` 进 prompt，planner 不用先知道仓库地址再去找。

同一个解析也是 `GET /api/tasks/{id}` 里 `project` / `project.organization` 的来源（[http-api.md](http-api.md)、[project.md](project.md)）：
一个 resolver，两个消费者，答案一致。那份视图停在组织本身（`project.organization{id,name}`）—— `services` 是决策周期的世界，
不属于"这条 task 在哪个 project"的回答。

## 不变式

1. **引用是 task 的，解析是 runtime 的**：`context_ref` 写什么由调用方说，解析出什么由 resolver 决定；解析不了就只报引用本身。
2. **不致命**：注册表挂了、超时了、不认识了，都不改变这次运行的结果 —— prompt 变薄，不改判。
3. **不另立注册表**：project / organization 是平台的事实（控制面 + 组织服务），autonomy 只读；进程内那份只补充自己的知识。
4. **顺序即优先级**：注册顺序决定字段归属，也是**流水线**：加一个新来源就是加一个 resolver，它可以读前面 resolver 写下的字段。
   `service_registry` 就是这么加进来的 —— 它读 `organization.id`（前面两个 resolver 找到的那个组织），再去问服务中心那个组织的服务列表。
