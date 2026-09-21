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

- 实现：`src/capability/`（`asset_change.go`、`software_development/code_edit.go`、`software_development/deploy.go`、`software_development/pull_request_review.go`、`deployment/monitor.go`），注册在 `capability.RegisterDefaults`。
- `deployment.monitor`：跟随一次部署、发现并解释问题（见 [deployment-monitor.md](deployment-monitor.md)）。
- **提示词不进代码**：委托给 worker 的提示词放在仓库文件里，运行时按次读取 —— 与 agent policy 同一套路（见 `src/prompt.go`）：

| 文件 | 占位符 | 说明 |
|---|---|---|
| `$PROJECT_ROOT/src/agent_policy/AGENT_V2.md` | `{{AGENT}}` / `{{TASK}}` / `{{RUNTIME_CONTEXT}}` … | planner 的 policy（见 [agent.md](agent.md)） |
| `$PROJECT_ROOT/src/agent_policy/CODE_EDIT.md` | `{{WORKSPACE}}` / `{{GOAL}}` + 整套 frame：`{{AGENT}}` / `{{WORLD}}` / `{{RUNTIME_CONTEXT}}` / `{{COMPLETION_PRINCIPLES}}` / `{{CONSTRAINTS}}` / `{{CONSTRUCTS}}` | `code_edit` 委托给 worker 的提示词（见 [delegation.md](delegation.md)） |
| `$PROJECT_ROOT/src/agent_policy/DEPLOYMENT_MONITOR.md` | `{{DEPLOYMENT}}` / `{{STATUS_URL}}` / `{{OBSERVATION}}` … + `{{AGENT}}` / `{{WORLD}}` / `{{RUNTIME_CONTEXT}}` / `{{COMPLETION_PRINCIPLES}}` / `{{CONSTRAINTS}}` | `deployment.monitor` 委托给监控 agent 的提示词（见 [deployment-monitor.md](deployment-monitor.md)） |

改措辞只要改文件、重跑即生效（不用重新编译）；文件缺失时该次委托直接失败（不会先建 agent 再没法 prompt）。

**凡是「需要 agent」的能力，交给这个 agent 的提示词都注入委托方 runtime 的 frame，并且身份那一节写的是它自己**：`{{AGENT}}`（role / id / name / backend / model / lifecycle / workspace，见 [agent.md](agent.md)）、`{{WORLD}}` /
`{{RUNTIME_CONTEXT}}` / `{{COMPLETION_PRINCIPLES}}` / `{{CONSTRAINTS}}` / `{{CONSTRUCTS}}`
（以及 `{{TASK}}` / `{{CONTEXT_ENTITY}}` / `{{GOAL_TYPE}}`）—— `code_edit` 与 `deployment.monitor`
都一样。词表、取值与渲染规则只有一份，在 `src/capability/broker`（`WorkerFramePlaceholders` /
`WorkerFrame` / `RenderWorkerPrompt`）：取值来自 planner 用的同一个渲染器（`src/prompt.go`，
`broker.WorkerPromptContext` 由 `Runtime.AcquireAgent` 发出的 session（`LLMSession`，与 planner 用的是同一种，见 [session.md](session.md)）实现，见 `Runtime.WorkerPlaceholders`），
所以被委托的 agent 看到的世界、完成原则和 planner 看到的是同一份。差别只有视角：worker 那份按**它自己**渲染
—— agent 身份 / workspace / backend 是 worker 的（委托方的沙箱永远不会被说成 worker 的，委托方以
`delegated_by` 出现），Task 与 `previous_actions` 是它被委托的那条 Task。host 取不到（测试替身）则该节渲染成
`(not provided by this runtime)`，而不是把 `{{NAME}}` 原样丢给模型；能力自己的占位符（`{{WORKSPACE}}` /
`{{GOAL}}`、监控用的 URL）永远由该能力自己填。不拿 agent 的能力（`pull_request.review` /
`service.deploy`）没有 frame：它们是确定性调用，值只走输入与环境。

### `{{CONSTRUCTS}}` 里有什么

planner 的 policy 和每个被委托的 worker 提示词拿到的是**同一份** constructs：runtime 现在能做的事，而且带调用形状。

| 字段 | 含义 |
|---|---|
| `name` / `domain` | 语义名、语义域 |
| `description` | 一段散文：语义、默认值、拒绝时的原因 |
| `input` | 入参：`name`（规范键）、`aliases`（同一入参的别名）、`required`、`description` |
| `output` | 返回：`name` + `description` |

**列表里没有 `provider`**：谁能干这活是 runtime 的决定，不是 step 能填的字段（step = capability + input），
所以 planner 根本不该看到这个轴。runtime 把这份归属记在**它自己录下来的那一步**上
（`execution_step.provider`，`Runtime.providerOf`，见 [execution-step.md](execution-step.md)）—— 那是审计事实，
不是给计划的选项。（capability 自己声明的**数据字段**里可以有 `provider`：例如 `code_edit` 会报
"哪个 backend 跑了这个 worker"，那是它返回的值。）

- 内置能力都声明了 `input` / `output`（`spec.Declared`，类型在 `src/capability/spec`）：plan step 里哪个键写什么、下一步从输出的哪个键读，只看这份列表就能决定，不必解析散文。`src/capability` 渲染列表、`spec` 提供类型，是因为子包（能力实现）不能反向 import 父包。
- 声明**可选**（和 `broker.WorkerPromptContext` 一样是可选实现的接口）：只有 `description` 的能力照样注册、照样出现，只是没有 `input` / `output`；**内置的五个都声明**，这条被 `capability_test.go` 钉住。
- 链路在列表里就能看出来：`service.deploy` 输出的 `poll` 正是 `deployment.monitor` 入参的 `poll`；`code_edit` 输出的 `pr_url` 就是它开的那条 PR（从 worker 的报告里读出 URL 形式，没开 PR 就是空），而 `pull_request.review` 的 `pr`（别名 `pr_url`）直接吃那个 URL。
- **planner 只按 capability 派发，不按 agent 派发**：plan step 就是「capability + input」，没有任何字段能写「派给谁」—— 哪个 capability 背后有 agent（`code_edit` / `deployment.monitor` 会 acquire 一个 worker）由 runtime 决定，planner 也不需要知道（`src/agent_policy/AGENT_V2.md` 的 `## Capability Dispatch`）。这正是本节开头那句「我能做什么，而不是谁来做」和[不变式](#不变式) 第 2 条：谁干活由 runtime 决定，「把这一步交给某个 agent」不是计划里能写的动作。
- **字段名是本能力的局部约定**：`output` 里的键只对**这个**能力有意义，下一个能力怎么叫、要不要它，由 planner 在计划里显式绑定（`{"source":"step:<name>.output.<key>"}`）—— runtime 从不跨能力猜名字、也不把共享 Context 当数据通道（见 [execution-step.md](execution-step.md) 的「计划的数据来源」）。所以能力声明里 `input`/`output` 的**描述**要写清语义：那是 planner 唯一能据以做映射的东西。
- **`output` 只放本职产出**：能力这次调用**产出的东西**（结果、它创建/改动的对象与状态）。谁触发的、解析出的是哪个 ref、轮询了几次、哪个实现——都是**这次调用的元信息**，不进 `output`；下游真需要这类**世界状态**就走世界模型（`world_model:…`）。"别人要读"不是把它当输出的理由。

### `{{CONSTRAINTS}}` 从哪来

`## Constraints` 那节渲染两样东西：**runtime 自己的事实**（这条 Task、唯一可改文件的沙箱、scope）+
**runtime 的 policy**（`$PROJECT_ROOT/src/agent_policy/CONSTRAINTS.json`，见 [policy.md](policy.md)）。
planner 的 frame 与每轮 delta、每个被委托 worker 的提示词拿到的都是这两样；渲染它的那层
（`src/prompt.go` / `src/policy.go`）不认识其中任何一条规则属于哪个领域 —— 「deploy 是 runtime 的动作」
是**部署边界**自己的一句话，不是提示词层的知识。

## `service.deploy`（触发指定服务、指定分支的流水线部署）

- 语义：把「某个服务的某个分支」交给部署控制面（agent-control-plane），由它打包该 git ref、再部署。
- 输入：`{"service":"<service id>","branch":"<branch/tag>"}`（`service_id` / `ref` 亦可；`branch` 留空 = 用服务契约里的默认分支）。
- 输出：`{"pipeline_id","state","poll","deployment","version"}` —— **这次调用产出的那条流水线**，以及控制面随后报回来的归属（`deployment` / `version`）。不回显输入（`service`、`branch`：控制面对空 `branch` 的解析也是它对这个输入的解析）、不带控制面那句散文 `message`、也不带归属审计 `identity`（谁是触发者由控制面自己的面板/审计记录说了算，能力不把它当产出回传）。能力只报产出，见 [execution-step.md](execution-step.md) 的「计划的数据来源」。
- **触发 ≠ 等待**：打包→部署要跑几分钟，所以 Run 在控制面**受理**（HTTP 202）后立刻返回 pipeline id，不占住 decision cycle；最终结果由后续观察决定（`poll` 就是 `GET /api/pipelines/<id>`）—— 触发成功不等于世界状态已达成（见[不变式](#不变式) 第 3 条）。
- **identity 注入（每个触发都要带）**：控制面的两个写接口（`/api/deploy-notify`、`/api/deploys`）要求调用方自报身份（[第一阶段身份校验](https://github.com/kaulie/agent-control-plane-deployment)：`identity_role: user|agent` + `identity_id: user_001 / agent_002`，缺失/非法 → 401），部署才能被审计与面板归因。`service.deploy` **永远**带这两个头，不依赖控制面 `IDENTITY_ENFORCE=0` 的逃生开关：取值顺序是 **输入**（`identity_role` / `identity_id`，可选，可把某次部署归给某个身份）→ **环境**（`IDENTITY_ROLE` / `IDENTITY_ID`，与控制面自己调用方同名，改归属不用改代码）→ **默认**（`agent:autonomy`：autonomy 是 agent 不是人，默认就署它自己的名）。角色大小写不敏感，非法值在**发请求之前**就报错，而不是拿 401 回来。**归属只走请求头**：它是这次调用的元信息，权威记录在控制面那边（`triggeredBy` / 面板审计），能力不把它当产出回传。
- 配置：`DEPLOYMENT_API_URL`（与 gateway 同名的变量）指向部署控制面，缺省 `http://127.0.0.1:4220`；`IDENTITY_ROLE` / `IDENTITY_ID` 覆盖署谁的名，缺省 `agent` / `autonomy`。
- 失败即失败：控制面自己的原因（如 `service not found: x`）原样进 error，不被吞成「已触发」。

## `pull_request.review`（读某个 PR 的评审意见，**不合并**）

- 语义：读**指定的那个 PR** 的评审意见（reviews）并返回给调用方。它**绝不合并** —— 合入由人来批准并执行，能力只把观察结果交出来，并明确报告「未合并、需要人工批准/合并」。原来那条自动合入的代码路径连同 merge-method / `mergeable_state` 的处理一起删掉了：不是关掉的可选项，而是根本没有这条路径。
- 指名方式有两种，**给 PR 自己的引用最直接**（`code_edit` 的报告里就有这个 URL）：
  - `{"pr":"https://github.com/owner/name/pull/43"}`（`pr_url` / `pull_request` 亦可）：按编号直接读这个 PR，**不做分支对查询**，head/base 以 PR 自己为准。认得的写法：`https://<host>/owner/name/pull/43`（任意 host；`/pulls/43`、结尾 `/`、`?query`、`#discussion_r1` 都认）、`owner/name#43`、以及 `43` / `#43`（仓库另有出处时）。
  - `{"from":"<head/topic 分支>","to":"<base 分支>"}`（`from_branch`/`head`、`to_branch`/`base` 亦可；`to` 留空 = 主干：`PR_BASE_BRANCH` → 仓库自己的 `default_branch` → `main`）。
- 输出：`{"from","to","number","pr","title","state","draft","merged":"false","requires_human_approval":"true","reviews","reviews_count","review_summary"}` —— `reviews` 是评审意见（`author` / `state` / `body` / …）的 JSON 数组；`merged` 恒为 `false`、`requires_human_approval` 恒为 `true`：**能力没有合并这条路径**，所以调用方永远读不出「已合并」，人必须自己批准并执行合并。
- **是代码，不是 agent**：读 PR 与它的评审是确定性动作（触发已知、结果可验证），所以直接走 GitHub REST API，不拿 worker（对比 `code_edit` / `deployment.monitor` 的委托）。
- **拒绝也是契约**（只读，原因原样返回）：`not_found`（该分支对没有开着的 PR，或该编号的 PR 已经不在）。git host 自己对读评审的拒绝（限额、权限等）原样进 error。
- **读不懂的引用是失败，不是猜**：给了 `pr` 但解析不出 PR（例如只给了仓库 URL、`owner/name`、`#abc`、没有编号）在**发出任何请求之前**就报 `cannot read a pull request from ...`；给了 `pr` 又给了互相矛盾的 `repo` / `from` / `to`（同一个调用点了两件不同的东西）也一样拒绝 —— 静默读掉其中一个正是这个能力不该有的行为。
- 配置：凭据**按顺序**取 —— capability 自己的 `Token` → `GITHUB_TOKEN` → `GH_TOKEN` → **`gh` CLI 自己的凭据（`gh auth token`）**；四处都没有才拒绝，且拒绝信息点名这几处（`src/capability/software_development/github_credential.go`）。最后一档是有意加的：这台机器上 worker 开 PR 就是跑 `gh`（web-cursor 的 GitHub 动作也全是 `gh`），所以"已经 `gh auth login` 的机器"不该因为 runtime 进程环境里没有 `GITHUB_TOKEN` 就走不通。仓库取 PR 引用 → 输入 `repo` → `GITHUB_REPOSITORY` → `GIT_REPO_URL`（任务工作区的 origin，`owner/name`、https、ssh 三种写法都认）；`GITHUB_API_URL` 换 API 基地址（GitHub Enterprise / 测试）。

## 不变式

1. Provider 可变，Capability 语义应尽量稳定。
2. Task Owner 面向 Capability Space，而不是设备列表。
3. Capability 报告执行成功，不自动等于 [Completion Contract](completion-contract.md) 满足。

## 演化注记

V1：注册少量固定 Capability（例如 `service.health_check`）即可。注册模型应允许后续动态发现与异构 Provider 排列组合。
