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
`broker.WorkerPromptContext` 由 `Runtime.AcquireAgent` 发出的 session 实现，见 `Runtime.WorkerPlaceholders`），
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
| `name` / `domain` / `provider` | 语义名、语义域、真正干活的一方（见 [Provider](provider.md)） |
| `description` | 一段散文：语义、默认值、拒绝时的原因 |
| `input` | 入参：`name`（规范键）、`aliases`（同一入参的别名）、`required`、`description` |
| `output` | 返回：`name` + `description` |

- 内置能力都声明了 `input` / `output`（`spec.Declared`，类型在 `src/capability/spec`）：plan step 里哪个键写什么、下一步从输出的哪个键读，只看这份列表就能决定，不必解析散文。`src/capability` 渲染列表、`spec` 提供类型，是因为子包（能力实现）不能反向 import 父包。
- 声明**可选**（和 `broker.WorkerPromptContext` 一样是可选实现的接口）：只有 `description` 的能力照样注册、照样出现，只是没有 `input` / `output`；**内置的五个都声明**，这条被 `capability_test.go` 钉住。
- 链路在列表里就能看出来：`service.deploy` 输出的 `poll` 正是 `deployment.monitor` 入参的 `poll`；`code_edit` 的 `summary` 带着它自己开的 PR URL，而 `pull_request.review` 的 `pr` 直接吃那个 URL。

### `{{CONSTRAINTS}}` 从哪来

`## Constraints` 那节渲染两样东西：**runtime 自己的事实**（这条 Task、唯一可改文件的沙箱、scope）+
**runtime 的 policy**（`$PROJECT_ROOT/src/agent_policy/CONSTRAINTS.json`，见 [policy.md](policy.md)）。
planner 的 frame 与每轮 delta、每个被委托 worker 的提示词拿到的都是这两样；渲染它的那层
（`src/prompt.go` / `src/policy.go`）不认识其中任何一条规则属于哪个领域 —— 「deploy 是 runtime 的动作」
是**部署边界**自己的一句话，不是提示词层的知识。

## `service.deploy`（触发指定服务、指定分支的流水线部署）

- 语义：把「某个服务的某个分支」交给部署控制面（agent-control-plane），由它打包该 git ref、再部署。
- 输入：`{"service":"<service id>","branch":"<branch/tag>"}`（`service_id` / `ref` 亦可；`branch` 留空 = 用服务契约里的默认分支）。
- 输出：`{"pipeline_id","state","service","branch","poll","identity"}`；流水线已经有的 `deployment` / `version` 一并带上。
- **触发 ≠ 等待**：打包→部署要跑几分钟，所以 Run 在控制面**受理**（HTTP 202）后立刻返回 pipeline id，不占住 decision cycle；最终结果由后续观察决定（`poll` 就是 `GET /api/pipelines/<id>`）—— 触发成功不等于世界状态已达成（见[不变式](#不变式) 第 3 条）。
- **identity 注入（每个触发都要带）**：控制面的两个写接口（`/api/deploy-notify`、`/api/deploys`）要求调用方自报身份（[第一阶段身份校验](https://github.com/kaulie/agent-control-plane-deployment)：`identity_role: user|agent` + `identity_id: user_001 / agent_002`，缺失/非法 → 401），部署才能被审计与面板归因。`service.deploy` **永远**带这两个头，不依赖控制面 `IDENTITY_ENFORCE=0` 的逃生开关：取值顺序是 **输入**（`identity_role` / `identity_id`，可选，可把某次部署归给某个身份）→ **环境**（`IDENTITY_ROLE` / `IDENTITY_ID`，与控制面自己调用方同名，改归属不用改代码）→ **默认**（`agent:autonomy`：autonomy 是 agent 不是人，默认就署它自己的名）。角色大小写不敏感，非法值在**发请求之前**就报错，而不是拿 401 回来；输出里的 `identity` 是控制面**记录下来的**触发者（`triggeredBy`），比调用方自述更权威。
- 配置：`DEPLOYMENT_API_URL`（与 gateway 同名的变量）指向部署控制面，缺省 `http://127.0.0.1:4220`；`IDENTITY_ROLE` / `IDENTITY_ID` 覆盖署谁的名，缺省 `agent` / `autonomy`。
- 失败即失败：控制面自己的原因（如 `service not found: x`）原样进 error，不被吞成「已触发」。

## `pull_request.review`（按 PR 本身或分支对合入 PR）

- 语义：把**指定的那个 PR** 合入它的 base 分支。合入 ≠ 写代码：`code_edit` 负责产出 PR（也可以合自己开的那条，见 `src/agent_policy/CODE_EDIT.md`），而这个能力是通用的那一个 —— 谁来开的 PR 都合。
- 指名方式有两种，**给 PR 自己的引用最直接**（`code_edit` 的报告里就有这个 URL）：
  - `{"pr":"https://github.com/owner/name/pull/43"}`（`pr_url` / `pull_request` 亦可）：按编号直接读这个 PR，**不做分支对查询**，head/base 以 PR 自己为准。认得的写法：`https://<host>/owner/name/pull/43`（任意 host；`/pulls/43`、结尾 `/`、`?query`、`#discussion_r1` 都认）、`owner/name#43`、以及 `43` / `#43`（仓库另有出处时）。
  - `{"from":"<head/topic 分支>","to":"<base 分支>"}`（`from_branch`/`head`、`to_branch`/`base` 亦可；`to` 留空 = 主干：`PR_BASE_BRANCH` → 仓库自己的 `default_branch` → `main`）。
- 可选 `method`：`merge`（默认）/ `squash` / `rebase`。
- 输出：`{"from","to","number","pr","merged":"true","method","sha","checks"}`；`sha` 是主干上那个合并提交，`checks` 是 `passed` / `none`。
- **是代码，不是 agent**：合入是确定性动作（触发已知、结果可验证），所以直接走 GitHub REST API，不拿 worker（对比 `code_edit` / `deployment.monitor` 的委托）。
- **拒绝也是契约**（绝不硬合，原因原样返回）：`not_found`（该分支对没有开着的 PR，或该编号的 PR 已经不在）、`draft`、`conflict`（`mergeable_state=dirty`）、`checks_failed`（点名失败的 check）、`checks_pending`（还没跑完，下一轮再问）、`blocked`（branch protection 不满足）。
- **读不懂的引用是失败，不是猜**：给了 `pr` 但解析不出 PR（例如只给了仓库 URL、`owner/name`、`#abc`、没有编号）在**发出任何请求之前**就报 `cannot read a pull request from ...`；给了 `pr` 又给了互相矛盾的 `repo` / `from` / `to`（同一个调用点了两件不同的东西）也一样拒绝 —— 静默合掉其中一个正是这个能力不该有的行为。
- 合入时把**刚检查过的那个 head commit** 一并交给 GitHub（`sha`）：期间分支被人推了新提交，GitHub 会拒（409），而不是把没人看过的提交合进去。
- 没配 CI 的仓库视为通过（报 `checks: none`）—— 不是失败，只是没有东西要等。
- 配置：`GITHUB_TOKEN`（或 `GH_TOKEN`）必需；仓库取 PR 引用 → 输入 `repo` → `GITHUB_REPOSITORY` → `GIT_REPO_URL`（任务工作区的 origin，`owner/name`、https、ssh 三种写法都认）；`GITHUB_API_URL` 换 API 基地址（GitHub Enterprise / 测试）。

## 不变式

1. Provider 可变，Capability 语义应尽量稳定。
2. Task Owner 面向 Capability Space，而不是设备列表。
3. Capability 报告执行成功，不自动等于 [Completion Contract](completion-contract.md) 满足。

## 演化注记

V1：注册少量固定 Capability（例如 `service.health_check`）即可。注册模型应允许后续动态发现与异构 Provider 排列组合。
