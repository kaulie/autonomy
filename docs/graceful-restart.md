# Graceful Restart

## 定义

**优雅重启**：重启这个服务时，**不在途切断任何一次运行**。

它不是「重启得快」，而是「重启前先问一句：现在可以吗」。回答这个问题的是服务自己。

## 谁在问

部署平台（`agent-control-plane-deployment`）重启一个服务的方式本是 `stop` + `start`。
服务若在部署配置里登记了**两个端点**，重启就变成 graceful：

```
POST <restartNotifyUrl>   部署前：即将重启，你先安排
   ↓（每 15s）
GET  <restartPollUrl>     现在能重启吗？（canRestart / canDeploy / ready 任一为真即放行）
   ↓（就绪，或等满 gracefulRestartMaxWaitMs——默认 10 分钟——强制继续）
rsync → restartCmd → 探活 /health
```

autonomy 这一侧就是这两个端点（[http-api.md](http-api.md)）：

| 端点 | 作用 |
|------|------|
| `POST /api/ops/restart-notify` | 受理重启通知：**停止启动新 run**，并回报此刻还有什么在途 |
| `GET  /api/ops/restart-status` | 轮询：`canRestart`（= `canDeploy` = `ready`）+ `running` / `runningTasks` + `held` + `reason` |

登记方式（部署平台面板「配置」）：`http://127.0.0.1:4300/api/ops/restart-notify`、
`http://127.0.0.1:4300/api/ops/restart-status`。**两个都给**才启用 graceful；只给一个或都不给，
平台按老办法直接 rsync + restart（不违法，只是可能切在 cycle 中间）。

## 什么叫「可以重启」

**没有在途 run**。一次 run 是一个 decision cycle，它可能已经改过世界（推了分支、触发了部署），
所以「切断它」不是「少做一步」，而是「做了一半」。于是 drain 的语义是：

1. **不再启动新 run**：通知之后到达的指令**照常受理**（它是 agent inbox 里的一行，见 [inbox.md](inbox.md)），
   但它的 agent **不启动**它。重启后的新进程会把它捡起来（`resumeAcceptedInstructions`）——
   **调用方交出来的东西不会丢**，这是「hold」而不是「拒绝」的原因。
2. **在途的 run 各自跑完**。它们在途的判据只有一个：`inFlightTasks`（`POST /api/tasks/{id}/stop` 取消的也是它）。
3. `running` 归零的那一刻，`canRestart` 为真。

`held` 是「被 hold 住的指令」（本进程内存里的数，重启后为 0 —— 指令本身是行）。它不是在途：
重启不会切断它，只会换一个进程把它跑起来。

## 进程自己的那一半

重启的最后一脚永远是 `stop`（`scripts/stop.sh`：`TERM` → 等 15s → `KILL`）。**TERM 也被当作一次 drain**
（`Autonomy.Shutdown`，`cmd/autonomyd`）：

1. 不再启动新 run；
2. 在途 run 给 `AUTONOMY_SHUTDOWN_GRACE`（默认 10s，必须落在 stop.sh 的 15s 窗口里）自己回来；
3. 还不回来的，用 `POST /api/tasks/{id}/stop` 的那条路停掉（任务的结局是 `stopped`，而不是一个
   谁也不会再 finish 的 `running`）——**并在记录里写明是谁停的**：这条 stop 消息与 `tasks.error`
   都带上是运行时为重启而停、以及平台给的 `requestId`（还有 `deployment` / `version`），所以「重启切断」
   不会在库里、面板上冒充「用户停的」（`src/stop_reason.go`；用户自己停的记录不变），再给一点时间落库；
4. `Server.Shutdown` 不再接新连接、等在途 HTTP 请求答完；
5. `Autonomy.Close` 拆掉常驻会话、两个 bridge，关掉 Store —— 与正常退出走的是同一个口。

被停掉的那一轮不会因此「没发生过」：它的计划与已经跑过的步骤都在 runtime 的记录里，下一次 run 的 `briefing`
会如实带出来（计划跑了一半的 step 是 `status: pending`、带的还是计划原文的 input）。所以接手的那一轮看得见
**它停在哪一步、已经产出过什么**（PR url、pipeline id 都在 step 的 output 里），而不是从零再排一遍。

没有这半边，SIGTERM 会把进程直接带走：deferred 的 `Close` 不跑，桥上的会话只会过期，
而 stop.sh 在 15s 之后补的那一刀更是想写什么都写不进去。

## 开机自愈：被切断的 run 不等指令

重启之后，**每个 agent 自己看自己的状态并接着做**（`resumeInterruptedRuns`，`Autonomy` 装配完成时执行），
而不是等用户再想起它、再发一条指令：

| 判据（只有这些算被切断） | 说明 |
|---|---|
| 该 agent 有一条 `running` 的消息 | 上一个进程把它取走了、没放回来（`agent_messages.status=running`）——这是「跑到一半」的唯一物证 |
| 任务行是 `pending`/`running`（硬 kill，没来得及写结局）或 `stopped` 且原因是**运行时**停的 | 后者是优雅收尾留下的行；**用户自己停的任务不在此列**（那是决定，不是意外） |
| 切断时间在 `AUTONOMY_RESUME_MAX_AGE` 之内 | 默认 24h；更旧的留给下一条指令——几天前切断的 run 不该在开机时自己复活 |

接续方式分两层，先会话后记录：

1. **能从会话续就从会话续**：任务行记着 provider 的 session id，桥用它的 transcript 给新会话播种
   （[session.md](session.md)、`src/clinesdk/bridge/resume.mjs`）——对话本身接着走；
2. **会话不在了才用记录重建**：`briefing` 里带着既有轮次、它们的产出、当前状态、**被切断在哪一步**
   （`state.interrupted`：原因 + `stopped_at_step` + `next_step`）以及还差什么（`open_criteria`）——
   `docs/execution-loop.md`。

两个开关（都在 `backend/.env`）：`AUTONOMY_RESUME_INTERRUPTED=0` 整体关掉自愈；
`AUTONOMY_RESUME_MAX_AGE=<时长>`（如 `30m`；`0` = 不限）调整年龄上限。日志里能看到它做了什么：
`开机自愈：续做 N 个被切断的 run（M 个因超龄跳过）`，以及每个 agent 的
`… 上一轮在 <时间> 被切断，开机自动续做（会话可用则续会话，否则按记录重建）`。

与「已受理、从未开始」的指令是两条**互不重叠**的路径：那条由 `resumeAcceptedInstructions` 在开机时启动，
判据是消息还 `queued`；这条只管消息已经 `running` 的。两条加起来，重启既不丢已受理的指令，
也不丢跑到一半的 run。

## 有界
drain 不会无限期：通知之后 **`AUTONOMY_DRAIN_TIMEOUT`（默认 10 分钟）** 内没有等到重启，
autonomy 自己恢复（把 hold 住的指令放回去跑）。理由是部署可能死在通知之后（打包失败、冲突），
而「服务从此不再受理新 run」是比「这次重启不优雅」严重得多的故障。

`0`（或负值）关掉这个保险：drain 一直等到平台动作。

## 不变式

1. **受理 ≠ 启动**。指令进 inbox 是受理，agent 开始跑才是启动；优雅重启只挡后者。
2. **在途 = `inFlightTasks`**。stop、重启、进程退出看的是同一个登记表，没有第二套「什么算在跑」。
3. **hold 的指令是行，不是内存**。重启换进程也丢不了；内存里那份（`held`）只是「本进程欠它们一次启动」。
4. **不猜**。`canRestart` 只由 `running == 0` 决定；没有通知时状态里 `draining=false` —— 「没人要重启我」和
   「现在可以重启」是两句话。
5. **进程退出与平台重启讲同一件事**。两边都是：不再启动新 run、等在途、然后干净地关。
6. **SIGKILL 留下的 run 不自动重跑**。那是一条 `running` 的消息（进程死在它中间）：入队时把它放回队首、
   由下一次启动这只 agent 的人接着跑（`RequeueRunningMessages`），这是原有语义；但**本次启动的扫描不做这件事**
   ——「开机自动重跑一个可能已经改过世界的 cycle」不是可以替人做的决定。它没有丢：那条 task 的下一条指令会把它带起来。

## 代码位置

- 语义与接线：`src/graceful.go`（`RestartDrain` / `RestartNotice` / `RestartStatus`、`Shutdown`、`resumeAcceptedInstructions`）
- 两个端点：`src/http_server.go`（`handleRestartNotify` / `handleRestartStatus`，注解即契约）
- 队列的 hold：`src/inbox.go`（`holdWhile` / `holdStart` / `resumePaused`；单条消息回队 `InboxStore.RequeueMessage`）
- 抓信号：`cmd/autonomyd/main.go`（`SIGTERM` / `SIGINT`）
- 脚本：`scripts/stop.sh`（`TERM` → 15s → `KILL`；这 15s 就是 `AUTONOMY_SHUTDOWN_GRACE` 的上界）
- 测试：`src/graceful_test.go`
