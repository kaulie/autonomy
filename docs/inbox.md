# Inbox（每个 agent 的消息队列）

## 定义

每个 agent 有一个 **inbox**：发给它的消息，按到达顺序排在那里，由它自己**一条一条**处理。
一条消息 = 谁说的（`sender` + `sender_id`）+ 说什么（`content`）；它产生的那一轮就是一次普通的 turn
（`reason_turns` / `llm_messages`），**消息就是那一轮回答的输入**。

三个来源（`MessageSender`）：

| sender | 谁 | 今天从哪来 | kind |
|---|---|---|---|
| `user` | 用户 | `POST /api/tasks` 的指令 | `instruction` |
| `agent` | 别的 agent | capability 交给 worker 的那句 prompt（`LLMSession.Prompt`），`sender_id` 记委托方 agent | `delegation` |
| `system` | runtime 自己 | `POST /api/tasks/{id}/stop` 的停止通知（`sender_id=runtime`） | `stop` |

实现：`src/message.go`（模型）、`src/inbox.go`（队列与消费者）、`src/sqlite_inbox.go`（engine 侧的表与读写）。

## 一条消息的一生

| 状态 | 含义 |
|---|---|
| `queued` | 在队里等 agent |
| `running` | 正在处理（被某个进程 claim 走） |
| `done` / `failed` / `stopped` | 处理完了：成功 / 失败（原因在 `error`，对任务意味着什么在 `tasks.status`） / 被取消 |

- **顺序 = 行 id**：下一条 = 这个 agent 最老的 `queued` 行；claim 是一条带条件的 UPDATE，两个消费者拿不到同一条。
- **一次一条**：每个 agent 一个消费者 goroutine，按到达顺序处理；忙的 agent 占一个 goroutine，闲的 agent 不占。
- **队是持久的**：消息是 `agent_messages` 的行，重启后上一个进程 claim 过的 `running` 消息会被新进程放回队里继续处理
  （与 agent 本身被 resume 是同一件事，见 [agent.md](agent.md)）。
- **用户指令可以持续接收**：指令到达时 agent 正忙 → 照样接受，排在它正在做的那件事后面
  （以前是拒绝：`task %s is already running`）。`POST /api/tasks` 的响应里给出这条消息的 id（`message_id`）和它前面还有几条（`queued`）。

## 三种消息各自意味着什么

- **`instruction` = 一次决策运行**：runtime 把消息内容作为**这一轮回答的输入**（prompt 里
  `runtime_context.additional_input.text`，见 [execution-loop.md](execution-loop.md)），循环、计划、验证照旧。
  所以同一条 Task 的第二条指令不会被丢掉，也不顶掉任务原来的描述：任务是什么写在行里，**现在被要求什么**写在消息里。
- **`delegation` = worker 的一次 turn**：capability 的 `Prompt` 把 prompt 发进 worker 的 inbox，由 worker 自己的消费者跑这一轮，
  结果等回来交给 capability —— 对话仍记在同一个 session 上，顺序由队列保证（见 [session.md](session.md)、[delegation.md](delegation.md)）。
- **`stop` 不是一次 turn**：停止是靠**取消正在处理的那条消息**做到的，这条消息是它的记录，排在它停下的那条指令之后。
  它后面的消息仍然在队里（已接受的不丢）。

## 与 agent 生命周期的关系

队空的时候 runtime 才让 agent 歇下来（关掉 provider 会话，行与 handle 留着，见 [agent.md](agent.md)）：
一条指令与下一条之间，对话是连着的。worker 不在这里被关 —— 它由拿到它的 capability 在 `Release` 时结束。

`Autonomy.Run(task)` 是同步的那扇门：它把指令放进队里，等**这条消息处理完且队空了**才返回，
所以调用方（测试、`cmd/autonomy`）拿到的仍然是这次运行的错误；`AcceptTask` 是 HTTP 的那扇门，不等。

## 不变式

1. **一个 agent 一条队，一次一条消息**：顺序就是到达顺序，谁发的都一样。
2. **先落库，再被看见**：入队是最先写下的，消费者不会漏掉刚到队尾的一条，也不会重复处理同一条。
3. **消息记自己的结果**：`done` / `failed` / `stopped` 是消息的结局；任务的落点仍然只在 `tasks.status`。
4. **不丢**：停止不删队里的消息，重启不丢已 claim 的消息。

## 代码位置

| 关注点 | 文件 |
|---|---|
| 消息模型（sender / kind / status） | `src/message.go` |
| 队列与消费者（每个 agent 一个） | `src/inbox.go` |
| 端口 `InboxStore` | `src/store.go` |
| sqlite 实现（表 / claim / 收尾 / 回收 / 计数） | `src/sqlite_inbox.go` |
| 谁发什么（指令 / 委托 / 停止） | `src/autonomy.go`、`src/api_service.go`、`src/llm_session.go` |
