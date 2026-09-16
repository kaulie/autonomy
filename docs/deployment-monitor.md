# deployment.monitor

## 定义

`deployment.monitor` 是 [Capability](capability.md)：**跟随一次部署，观察它走到哪一步、有没有出问题、证据在哪**。

它只观察，不改变世界 —— 不重试、不回滚、不部署。发现的问题以 [Event](event.md) 语义返回给 Agent，
由 Agent（下一次 decide）决定继续等待、修、回滚还是叫人。

流水线（Pipeline）部署期间的典型用法：

```
Deploy → deployment.monitor → 有问题？→ 看 evidence/diagnosis → Fix → Deploy → deployment.monitor → Done
```

## 为什么需要它

部署「失败」通常不是一次调用失败，而是一段时间里状态没往前走。
所以监控能力要回答三件事：

1. 现在到哪了（state / phase / progress）；
2. 是不是出问题了（problem），依据是什么（signals）；
3. 排障看什么（evidence / suggestions）。

`deployment.monitor` 成功执行 ≠ 部署成功：部署失败也是一次**成功的观察**（`state=failed`），
只有当部署根本观察不到时才返回 error（见 [Verification](verification.md) 的同一分离）。

## 输入

| 输入 | 必填 | 默认 | 说明 |
|------|------|------|------|
| `deployment` | 是 | — | 部署 / 流水线标识（也接受 `pipeline_id` / `pipeline` / `request_id` / `target` / `run` / `id`） |
| `task_id` | 否 | — | 本次观察所属 Task（runtime 会自动带上）；agent 监控时记在它的 run 上 |
| `poll` | 否 | — | 触发能力返回的相对状态路径（如 service.deploy 的 `poll`：`/api/pipelines/<id>`），会拼到 `endpoint` 上 |
| `status_url` | 否 | — | 完整状态 URL（优先级最高） |
| `endpoint` | 否 | `$DEPLOYMENT_API_URL` → `http://127.0.0.1:4220` | 部署服务 base URL；状态地址 = `<endpoint>/api/pipelines/<deployment>` |
| `logs_url` | 否 | `<status_url>/logs` | 单独的日志地址（状态里已带 `logs` 时不会去取） |
| `watch` | 否 | `false` | `true` 时轮询到终态或超时为止 |
| `interval` | 否 | `5`（秒） | 轮询间隔，最小 1s |
| `timeout` | 否 | `60`（秒） | 整个观察窗口（`watch` 时生效），上限 600s |
| `tail` | 否 | `40` | 保留最近多少行日志作为证据，上限 500 |

默认 **只看一次**（`watch=false`）：循环本身就是重复的来源 ——
下一轮 decide 可以再调一次 `deployment.monitor`（见 [execution-loop.md](execution-loop.md)）。
`watch=true` 是「在一次调用内跟一段」，窗口有界，不会把决策循环挂死。

## 与 `service.deploy` 组合

`service.deploy`（同一个 control plane，见 [capability.md](capability.md)）负责**触发**流水线并立即返回
`pipeline_id` / `poll`；`deployment.monitor` 负责**跟随**它：

```
service.deploy {service, branch} → {pipeline_id, state:"queued", poll:"/api/pipelines/<id>"}
   ↓ （下一轮 decide）
deployment.monitor {pipeline_id, poll} → running/failed/succeeded + signals + evidence
```

两者指向同一个 `DEPLOYMENT_API_URL`，无需额外配置；默认 `status_url` 规则与 control plane 的
`GET /api/pipelines/<id>` 对齐。


## 输出

| 输出 | 说明 |
|------|------|
| `state` | `unknown` / `pending` / `running` / `succeeded` / `failed`（未告知即 `unknown`，不猜） |
| `terminal` | 是否终态 |
| `phase` / `progress` | 部署系统报的阶段与进度 |
| `healthy` | 部署自报的健康状态（没报则不出现该字段） |
| `service` / `version` / `deployment_name` | 部署系统自报的服务、版本、部署对象名（有则出现） |
| `message` | 部署系统自报的消息（control plane 的失败原因常在这里） |
| `problem` | 是否发现需要处理的问题 |
| `signals` | 命中的信号，逗号分隔 |
| `diagnosis` | 一句话结论（状态 + 阶段 + 信号 + 首个可疑错误行 / message） |
| `evidence` | 日志窗口：有命中行时取其上下文，否则取最近 `tail` 行；总长有上限 |
| `suggestions` | 每个信号对应的下一步（去查什么 / 改什么） |

**不回显、不带 bookkeeping、不带"谁在观察"**：`polls` / `observed_at` / `provider` / `source` / `note` 都不在输出里——"看了几次、什么时候看的、是哪个实现、有没有降级"是这次调用的元信息（agent 那次 run 与它的原文在 `llm_messages` 里可回看），观察的内容才是输出（能力只报产出，见 [capability.md](capability.md)）。

## 信号

结构性信号：

| 信号 | 触发 |
|------|------|
| `deployment_failed` | 状态为 failed |
| `unhealthy` | 部署自报不健康 |
| `stalled` | 仍在 running/pending，但超过 10 分钟没有 `updated_at` 更新 |

日志指纹信号（对日志行与 `error` 字段做大小写不敏感匹配）：
`oom`、`image_pull`、`crash_loop`、`timeout`、`connection`、`permission`、`config`、`crash`。
每个信号都自带一条 `suggestion`（例如 `oom` → 提高内存上限或修泄漏）。

指纹只在**结果未定**时生效：已经 `succeeded` 的部署不会因为日志里出现 `connection refused`（重试后成功）被判成 problem。

## 谁来监控（Provider）

能力语义固定，监控来源可换（见 [Provider](provider.md)）：

- **默认：agent 监控**（agent-backed）。注册时宿主注入了 agent broker，`deployment.monitor` 就通过
  `Runtime.AcquireAgent` 拿一个 worker（它有自己的 workspace 和工具），把**部署坐标**（`status_url` / `logs_url`）
  和**原始观察**一起交给它，让它自己去看、自己判断，并按要求返回结构化 JSON
  （`state` / `problem` / `signals` / `diagnosis` / `suggestions` / `logs`）。
  agent 的判断优先于内置规则。提示词在仓库文件里，运行时按次读取：
  `$PROJECT_ROOT/src/agent_policy/DEPLOYMENT_MONITOR.md`（缺失时该次观察直接失败，不会先建 agent）。
  它和别的被委托的 worker 一样，拿到的是**委托方那个 runtime 的 frame**：Agent 身份（它自己的 role / id /
  name / backend / workspace —— 不是委托方的）、World、Runtime Context、Completion /
  Completion Principles、Constraints（`{{AGENT}}` / `{{WORLD}}` / `{{RUNTIME_CONTEXT}}` /
  `{{COMPLETION_PRINCIPLES}}` / `{{CONSTRAINTS}}`，由 session 带下来，见 [delegation.md](delegation.md)）
  —— 它观察的世界就是 runtime 的 World，而「只观察不修复」这条约束在提示词里写死。
- **确定性读取**：没有 agent broker（或显式注入 `Observer`）时，`HTTPObserver` 直接读部署 API
  （`GET <status_url>`，必要时再取 `logs_url`；宽容字段 `state`/`status`、`phase`/`stage`、`healthy`/`health`、
  `logs`/`lines`、`updated_at`，以及 control plane 的 `requestId`/`serviceId`/`message`/`version`/`deployment`），
  由内置规则给信号。
- **自定义**：宿主可以注入自己的 Observer（CI API、编排器、本地部署记录）：
  `capability.RegisterDefaults(f, capability.Deps{Deployments: myObserver})`。

**"是谁在观察"不进输出**：走的是 agent 还是直接读 API，是能力的**接线**（agent 那条路在这步自己的交互行上看得见）；输出只说观察到了什么，`source` / `polls` / `observed_at` / `provider` 都不在里面。

agent 返回的 JSON 读不出来时，**原始观察仍然有效** —— 不会因此编造一个状态；那次 agent 的 run 和它的原文都在 `llm_messages` 里可回看，两者都没有（读取失败且 agent 也没答）才是 error。

代价：agent 监控每次观察都要跑一次 LLM；`watch=true` 会在一次调用里最多轮询 61 次。
要控制成本时，用 `Deps.Deployments` 注入确定性 Observer，或保持默认的一次观察、由决策循环决定何时再看。

部署系统状态词汇通过 `normalizeState` 归一到上面五个状态（`queued` → pending，`packaging`/`deploying` → running，…）。

## 代码位置

- 能力（语义 + 诊断合成）：`src/capability/deployment/monitor.go`
- 默认 Provider（agent 监控）：`src/capability/deployment/agent_observer.go` + 提示词文件
  `src/agent_policy/DEPLOYMENT_MONITOR.md`（每次观察现读现渲染，见 `prompt.go`）
- 确定性 Provider：`src/capability/deployment/http_observer.go`
- 注册：`src/capability/register.go`（`RegisterDefaults`，注入 `deps.Agents` / `deps.Deployments`）
- 测试：`src/capability/deployment/*_test.go`

## 不变式

1. 只观察，不改变世界（不重试、不回滚、不改配置）；监控 agent 也被这样约束（提示词明写，见 `DEPLOYMENT_MONITOR.md`）。
2. 未被告知的状态就是 `unknown`，不推断 —— agent 也不例外（答复读不出来时回退到原始观察，而不是编一个状态）。
3. 观察不到 → error；观察到失败 → `state=failed` + `problem=true`（不是 error）。
4. `problem=false` 不等于 [Completion Contract](completion-contract.md) 满足。
5. 事实与判断分开：`state` 是观察到的事实（结构性信号由它推出），`problem`/`signals`/`diagnosis` 是监控 agent 的判断；agent 的判断优先，内置规则做兜底。
