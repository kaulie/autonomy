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
| `deployment` | 是 | — | 部署 / 流水线 run 标识（也可写 `target` / `run` / `id`） |
| `status_url` | 否 | — | 完整状态 URL（优先于 `endpoint`） |
| `endpoint` | 否 | `$AUTONOMY_DEPLOYMENT_ENDPOINT` | 部署服务 base URL，状态地址 = `<endpoint>/deployments/<deployment>` |
| `logs_url` | 否 | `<status_url>/logs` | 单独的日志地址（状态里已带 `logs` 时不会去取） |
| `watch` | 否 | `false` | `true` 时轮询到终态或超时为止 |
| `interval` | 否 | `5`（秒） | 轮询间隔，最小 1s |
| `timeout` | 否 | `60`（秒） | 整个观察窗口（`watch` 时生效），上限 600s |
| `tail` | 否 | `40` | 保留最近多少行日志作为证据，上限 500 |

默认 **只看一次**（`watch=false`）：循环本身就是重复的来源 ——
下一轮 decide 可以再调一次 `deployment.monitor`（见 [execution-loop.md](execution-loop.md)）。
`watch=true` 是「在一次调用内跟一段」，窗口有界，不会把决策循环挂死。

## 输出

| 输出 | 说明 |
|------|------|
| `state` | `unknown` / `pending` / `running` / `succeeded` / `failed`（未告知即 `unknown`，不猜） |
| `terminal` | 是否终态 |
| `phase` / `progress` | 部署系统报的阶段与进度 |
| `healthy` | 部署自报的健康状态（没报则不出现该字段） |
| `problem` | 是否发现需要处理的问题 |
| `signals` | 命中的信号，逗号分隔 |
| `diagnosis` | 一句话结论（状态 + 阶段 + 信号 + 首个可疑错误行） |
| `evidence` | 日志窗口：有命中行时取其上下文，否则取最近 `tail` 行；总长有上限 |
| `suggestions` | 每个信号对应的下一步（去查什么 / 改什么） |
| `polls` / `observed_at` / `provider` | 轮询次数、观察时间、实现方 |

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

## Provider

能力语义固定，观察来源可换（见 [Provider](provider.md)）：

- 默认 `HTTPObserver`：`GET <status_url>` 读 JSON（宽容字段：`state`/`status`、`phase`/`stage`、`healthy`/`health`、`logs`/`lines`、`updated_at`），
  状态里没有日志时再 `GET <logs_url>`（JSON `lines`/`logs` 或纯文本）。
- 宿主可在注册时注入自定义 Observer（CI API、编排器、本地部署记录）：
  `capability.RegisterDefaults(f, capability.Deps{Deployments: myObserver})`。

部署系统状态词汇通过 `normalizeState` 归一到上面五个状态。

## 代码位置

- 能力：`src/capability/deployment/monitor.go`
- 默认 Provider：`src/capability/deployment/http_observer.go`
- 注册：`src/capability/register.go`（`RegisterDefaults`）
- 测试：`src/capability/deployment/*_test.go`

## 不变式

1. 只观察，不改变世界（不重试、不回滚、不改配置）。
2. 未被告知的状态就是 `unknown`，不推断。
3. 观察不到 → error；观察到失败 → `state=failed` + `problem=true`（不是 error）。
4. `problem=false` 不等于 [Completion Contract](completion-contract.md) 满足。
