# Broadcast（一句话，说给一批 agent）

## 定义

一次**广播** = 一条消息，投递给一批 agent：**某个 project 下面的**所有 agent，或**所有 project 下面的**所有 agent。

它不是新概念，而是**投递的复数形式**：每个目标收到的那条消息，与 `POST /api/tasks` 投给它的**逐字段相同**
（`user` 发的 `instruction`，见 [inbox.md](inbox.md)）—— 换句话说，`Broadcast` 是 `AcceptTask` 的复数，
没有第二条投递路径，也没有第二种消息。

它**不是**：一次"群发通知"这种平台能力；不是绕过 agent 队列直接戳对话；不是"给一堆 task 建一个超级 task"。
消息到了谁的队列里，就由谁按自己的顺序处理（[inbox.md](inbox.md) 的不变式一条不改）。

## 范围怎么算：agent 属于哪个 project，是它负责的 task 说的

这个 runtime 里没有"project 的成员表"。project 归属只有一处事实来源：

| 事实 | 写在哪 |
|---|---|
| 这条 task 在哪个 project | `tasks.context_ref`（`{"project": "<id>"}`，见 [task.md](task.md)、[project.md](project.md)） |
| 这条 task 由谁负责 | `tasks.agent_id`（见 [agent.md](agent.md)） |

所以范围是**先落到 task，再由 task 落到它的 owner agent**：

- 「某个 project 下的所有 agent」= 那个 project 的所有 task 的 owner agent；
- 「所有 project 下的所有 agent」= 所有 task 的 owner agent（没写 project 的 task 也算：它照样有 agent）。

**被委托出去的 worker 不是"某个 project 的 agent"**：它是某条 task 的执行细节（capability `AcquireAgent` 要来的），
责任主体仍然是那条 task 的 owner（[capability.md](capability.md)、[delegation.md](delegation.md)）。
广播说的那句话是"**关于那条 task 的**指令"，worker 拿不到 context 去理解它，也就不该拿到它。

## 请求与报告

`POST /api/broadcast`（见 [http-api.md](http-api.md)）：

```json
{ "content": "上线窗口挪到今晚 20:00", "project_id": "project-749a0238" }
{ "content": "今天 18:00 全员停服演练", "all_projects": true }
```

- `content`：说的话本身，逐字成为每个目标 agent 那一轮运行的输入。
- 范围**必须说出来**：`project_id` 或 `all_projects=true`，二选一。
  "忘了写 project"和"就是想发给所有 project"不是同一个请求，而后者太宽 —— 不能靠省略来猜。

报告是"每个目标一行"：

```json
{
  "scope": "project", "project_id": "project-749a0238",
  "targets": 3, "delivered": 2, "skipped": 1, "failed": 0,
  "deliveries": [
    { "task_id": "task-a", "agent_id": 10001, "project_id": "project-749a0238",
      "status": "delivered", "message_id": 42, "queued": 0 },
    { "task_id": "task-b", "status": "skipped", "reason": "the task has no agent" }
  ]
}
```

| status | 含义 |
|---|---|
| `delivered` | 消息已经在那只 agent 的队列里（`message_id` / `queued` 与 `POST /api/tasks` 的答复同义） |
| `skipped` | 这个目标投不了，`reason` 说的是目标自己的事实：task 还没有 agent，或它的 agent 已经被 let go |
| `failed` | 投递这件事失败（`reason` 是 runtime 自己的话）；**其它目标不受影响** |

## 不变式

1. **一条一条，各回各家**：广播不新造投递机制 —— 每个目标收到的就是一条普通 `instruction` 消息，
   落在它自己的队里，由它自己的消费者按到达顺序处理；它忙就排在它正在做的事后面（指令可以持续接收）。
   目标 agent 若不在本进程手上（重启之后），跟单条指令一样按 `tasks.agent_id` resume（同一个
   `resumeAgentForTask`，会话重新挂上，见 [agent.md](agent.md)）。
2. **范围从事实算出来，不从请求猜**：目标来自 `tasks` 行本身（`context_ref` 的 project、`agent_id` 的 agent），
   不是调用方给的一份名单；同一个 project 的 agent 集合是"这个 project 的 task 们现在由谁负责"。
3. **不发就不发，不为了发而造**：目标投不了（没有 agent / agent 已 let go）就报 `skipped`，
   不顺手复活那条 task、不新建 agent —— 广播是把话交给**已经在那里**的 agent。
4. **部分失败不回滚**：`delivered` 的那些不会因为别的目标 `failed` 而被撤销；一次广播不是一次事务。
5. **不等**：广播只把消息放进队列就返回；跑不跑、跑多久，是各只 agent 自己的事（要跟进用
   `GET /api/tasks/{id}` / `-progress`，要停用 `POST /api/tasks/{id}/stop`）。

## 代码位置

| 关注点 | 文件 |
|---|---|
| 广播（范围解析 / 投递 / 报告） | `src/broadcast.go` |
| 目标从哪来（`ListTasks` 端口） | `src/store.go`、`src/sqlite_query.go` |
| 一条指令消息怎么来的（广播复用同一条路） | `src/autonomy.go`（`instruction`）、`src/api_service.go` |
| 消息模型与队列 | `src/message.go`、`src/inbox.go` |
| HTTP 端点 | `src/http_server.go`（`POST /api/broadcast`） |
| 命令行客户端 | `cmd/autonomy`（`-broadcast all` / `-broadcast <project id>`） |
| 测试 | `src/broadcast_test.go`、`cmd/autonomy/main_test.go` |

## 演化注记

- **先看再发**：今天只有"投递后报告"，没有"只算范围不投递"的 dry-run；范围很宽（`all_projects`）时，
  先看一眼会收到的是谁是有价值的，可以加一个 `dry_run`，报告复用同一份 `deliveries`。
- **更细的范围**：今天的范围只有 project / 所有 project 两种。另有它义的集合（组织、某个 goal_type、
  某批 task id）都可以作为**范围的一种**加进来，而不用动"投递"本身 —— 范围的解析在 `broadcastTargets` 一处。
- **广播本身要不要留痕**：今天一条广播的记录就是"它对每个目标写下的那条消息"（外加这次调用的响应）。
  如果将来要能反查"哪条消息来自哪次广播"，最小改法是在响应/消息上带一个广播 id，而不是给消息加一种新的 kind
  —— 对收到的 agent 来说，它就是一条指令，这个语义不该被投递方式改变。
