# Event Center 客户端（Runtime 观察世界的方式）

## 定义

[event-center](https://github.com/kaulie/event-center) 是平台的**事件中心**：接收 GitHub webhook 与其它生产者，持久化成分发日志。Autonomy runtime **依赖这个服务**来看见外部世界，**不**直连 GitHub。

本仓库只有客户端（`src/eventcenter`），没有事件中心本体。

## 职责

- **负责**：按游标拉取 event-center 的流、把一条事件收成 world fact、给 `watch` 的 pull request Probe 提供快照。
- **不负责**：校验 GitHub HMAC；保存事件日志；把 GitHub REST 当观察手段；规定下一步调用谁。

## 边界

```
GitHub webhook ──▶ event-center ──pull──▶ Runtime
                         ▲                  │
                         │                  ▼
                   其它生产者           Event Gateway → World / inbox
```

| 层 | 位置 | 知道什么 |
|---|---|---|
| 服务 | event-center 进程 | GitHub 签名、去重、seq、push/pull |
| 客户端 | `src/eventcenter/` | HTTP pull、PR 身份、Fact。不知道 task / inbox / GitHub token |
| runtime 接线 | `src/event_center.go`、`src/watch.go` | 后台 tail 流 → ingest；PR Snapshot → Watcher |

`pr.check` / `pull_request.review` 仍可直连 GitHub：那是验真与评审，不是观察。观察走 event-center。

## HTTP（客户端调用的契约）

默认基址 `http://127.0.0.1:9099`（`EVENT_CENTER_API_URL`）。消费口可用 `EVENT_CENTER_ADMIN_TOKEN` / `EVENTD_ADMIN_TOKEN`。

- `GET /v1/streams` — 各流的 `stream_seq` 头
- `GET /v1/streams/{stream}/events?after=&limit=&wait=` — 游标拉取（`wait` 为 long-poll）

`AUTONOMY_EVENT_CENTER=0` 关掉客户端（PR 的 `watch` 会失败，不再回退到 GitHub）。`AUTONOMY_EVENT_CENTER_STREAMS` 默认 `github`。

## 与 Gateway / Watcher

- **Feed**：进程后台 tail 配置的流，每条事件经 Gateway ingest。已登记的 `watch` 把 `subject.task_id` 填上。
- **watch**：PR 的 Snapshot 读 event-center 最近事件，不读 `api.github.com`。到达 `until` 后 Watcher 发的 Change 与 Feed ingest 共用稳定幂等键（`github:owner/name#N:merged`），不会叫醒两次。

## 不变式

1. Runtime 不把 GitHub 当作观察依赖。
2. Fact 的 `source` 是生产者（`github`），不是 `event-center`。
3. 事件中心里还没有这条 PR 的事件，不等于对象不存在：仍然可以 `watch`，等以后的注入。
