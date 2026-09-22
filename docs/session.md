# Session（LLM 会话）

## 定义

一个 **Session** 是**一只 agent 与它的 LLM 之间的会话**：它的每一轮从这里出去，也记在这里。

它不是「planner 的会话」也不是「worker 的会话」：**谁都跑在同一套 session 上**，差别只在 agent 的身份（`Role`）。
一条 Task 自己的 agent 每一轮决策取一轮，被委托的 worker 每收到一次 prompt 取一轮 —— 同一段代码、同一份记录。

实现：`src/llm_session.go`（`LLMSession` / `SessionOpts`）。

## 身份决定一轮的形态，而不是"另一种 session"

| | 谁 | 一轮是什么 |
|---|---|---|
| `Role=planner` | runtime 为一条 Task 创建的 agent（`Autonomy.Run`） | `mode=plan`；输入行 `role=user`（user/runtime 在问它）；round = 它的**决策轮**（`ctx.Cycle`） |
| `Role=worker` | capability 通过 `AcquireAgent` 要来的 agent | `mode=agent`；输入行 `role=agent`（**委托方**在问它，用户只写顶层 Task）；round = 它自己的轮次，从 1 开始（`RoundAuto`） |

**这两个值是从 `Agent.Role` 推出来的**（`LLMSession.turnShape`），不是一个配置项：身份已经在 agent 上了，提示词的 `## Agent` 那节读的也是它。
「谁写的这句话」只表达一件事 —— 用户不会以 worker 的身份说话；「这一轮在做什么」（plan / agent）也不改变 cycle 的含义（见 [execution-loop.md](execution-loop.md) §cycle 与 step）。

## 一轮里发生什么

| 步骤 | 做什么 |
|---|---|
| 打开后端会话 | `ensureLLMSession`（幂等）：Task 自己的 agent 是 `local` 创建的，**第一轮**才挂后端（接受指令时**不挂**：accept 只是把消息放进队列，挂会话属于这一轮，见 [agent.md](agent.md)）；被 acquire 的 worker 在 acquire 时就挂好了 |
| 看门狗 | 按**空闲**超时（`AUTONOMY_LLM_TIMEOUT`，默认 3m），不是墙钟：一轮跑多久都行，只要 provider 一直在出声 |
| 调用 ≠ run | 开会话那几次 bridge 调用（Ping / Create / Resume）是**调用**：`CURSOR_SDK_CALL_TIMEOUT`（默认 1m）一过就放弃它（`no answer from the bridge within 1m0s`），不会拿 run 的空闲预算去等一个卡住的桥；而**流**不受这个上限约束 |
| 结束原因要留下来 | 这个 run 的上下文自己结束时（空闲被掐、被 stop），报的是它带着的原因（`run idle for 3m0s: no provider activity`），不是传输层的 `context canceled`（`bridgeCallErr` / `llmrun.CtxErr`） |
| 开 run header | `BeginLLMTraceFrom`：`reason_turns` 一行（task / agent / round / mode / provider / model）＋ 输入 `llm_messages` 行（role 见上表） |
| provider 流 | `Agent.PromptLLMStream`：折成对话消息落 `llm_messages`、写一行 stderr；原始事件落 `llm_events`（叶子表，默认不写，`AUTONOMY_LLM_EVENTS=1` 打开） |
| 收尾 | `trace.Finish`：status / usage / tokens / duration 写回 run header；成功的轮才 `markLLMFrameSent()`（首轮发的 frame 算送达） |
| 被截断 | provider 在输出上限处掐掉这一轮（`max-tokens`、没有完成的 tool call）：**在同一个会话上再问一次**，附 `src/agent_policy/TURN_TRUNCATED.md`（`AUTONOMY_LLM_TURN_RETRIES`，默认 1）。失败的原始轮与重试**各自一行** `reason_turns`，且**同一个 round**（重试不是新的决策轮）。其它失败（provider 报错 / 余额不足 / 会话丢了）不重试 |

## 两扇门，同一个构造器

| 门 | 谁走 | 拿到什么 |
|---|---|---|
| `Autonomy.Run` | 一条 Task 自己 | `NewLLMSession(rt, agent, SessionOpts{TaskID: task.ID})`，身份 `Role=planner` |
| `Runtime.AcquireAgent` | capability（`code_edit` / `deployment.monitor`） | `NewLLMSession(rt, agent, SessionOpts{TaskID, Model, Workspace, Provider, DelegatedBy, Inbox})`，身份 `Role=worker`；它的 `Prompt` 是**委托方 agent 发的一条消息**，走 worker 自己的 inbox，由 worker 的消费者跑这一轮（见 [inbox.md](inbox.md)） |

capability 看到的只是 `broker.AgentSession` 那个窄视图（`ID` / `Workspace` / `Prompt` / `Release`）—— 它们不编号 round，也没见过 mode；宿主（planner 那一侧）看到的是 `Say(prompt, round)`，因为**决策轮由 runtime 数**。

## 结束：一个函数

`Close`（= capability 侧看到的 `Release`）走 `closeAgent`（`src/agent.go`）：停、拆掉 provider 会话，然后按 `Lifecycle` 决定**留**（`persistent`，**默认**：行与 session id 留着，下一条指令还能 Resume，见 [agent.md](agent.md)）还是**删**（`ephemeral`：软删 + 从 factory 摘掉；只有明确要一个用完即弃的 worker 才这样，`broker.AcquireAgentOpts.Ephemeral`）。

注意**一次运行结束不走这里**：agent 常驻，一次运行结束只是标 `idle`（`Autonomy.drainedAgent`），会话留着给下一条消息复用 —— 走 `closeAgent` 的是真正的结束：worker 的 `Release`、后端没挂上、进程退出。**结束一只 agent 与它是谁无关**，所以只有一份实现。

## 不变式

1. **一个 agent 一条会话**：它的每一轮都在这里记；没有 session 就没有 run（`LLMReasoner` 要求 `Agent.Session`，报错而不是偷偷造一条）。
2. **差异来自身份，不是开关**：plan / agent、输入行的 role、round 的来源，全由 `Agent.Role` 与调用方给的 round 推出；接口上没有"我是不是被委托的"这种字段。
3. **记录与重试只有一处**：`reason_turns` / `llm_events` / `llm_messages` 的写入、空闲看门狗、截断重试都在这一层，planner 与 worker 不会有谁少一份。
4. **归属写在会话上**：`reason_turns.task_id` 与 worker 的 `agents.current_task_id` 都是 `SessionOpts.TaskID`（委托方那条 Task），见 [delegation.md](delegation.md)。
5. **会话丢 ≠ 上下文丢**。provider 会话会随进程消失（Cline bridge 的会话活在 bridge 进程里，bridge 一重启就没了），但**这条 task 做过什么**写在 runtime 自己的记录里（`execution_plan` / `execution_step_plan` / `execution_step`）：一次 run 开始时读回来，作为 Runtime Context 的 `briefing` 交给它的每一轮（`src/task_record.go`）。重启之后的一次指令因此仍然是**同一条 task 的续做**，而不是一个「从头开始的新任务」—— 会话层能恢复的是对话与 prompt cache（跨重启恢复见 [graceful-restart.md](graceful-restart.md)），记录层保证的是「我做过什么、还差什么」不丢。

## 关系

- [agent.md](agent.md)：agent 的身份（planner / worker）、workspace、lifecycle —— 会话从身份推出这一轮的形态
- [delegation.md](delegation.md)：委托出去的那只 worker 拿到的就是这里说的会话
- [execution-loop.md](execution-loop.md)：planner 的 cycle 与 worker 自己的轮次
- [llm-message.md](llm-message.md) / [llm-event-stream.md](llm-event-stream.md)：一轮落下的两张表（`reason_turns` / `llm_messages` / `llm_events`）
- [cline-reasoner.md](cline-reasoner.md)：被截断的一轮如何续（`TURN_TRUNCATED.md`）
