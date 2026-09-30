# 提示词是文件

**一只 agent 被模型看到的每一句提示词，都来自 `src/agent_policy/` 下的一个文件。** Go 只做装配：
读文件、填占位符、把两半拼在一起。改措辞 = 改文件。

## 两个半：初始化（frame）与每轮（delta）

一只 agent 的一生有**两次 prompt 注入**，顺序是定死的：**先建 agent，再给它第一条 prompt，然后才有 task 的那条**。
这就是两个文件：

| 文件 | 是哪条 prompt | 什么时候注入 |
|---|---|---|
| `REASONING_FRAME.md` | **第一条 prompt（也就是 system prompt）**：`{{SYSTEM_PROMPT}}` + `{{AGENT_POLICY}}`（`AGENT_V2.md`）+ `{{FRAME_REPLY}}`（初始化 ack） | **agent 创建时**（`Autonomy.InitializeAgent` → `LLMSession.GiveFirstPrompt`）；之后任何换会话的时刻也再给一次 —— 见 `Agent.needsLLMFrame()` |
| `REASONING_DELTA.md` | **task prompt（task 那条）**：轮次标题、`{{DELTA_MARKER}}` 那句「这些值本轮给你」、可选的 `{{BRIEFING_NOTE}}`（重启续做的说明）、`{{PAYLOAD}}`（当前 Task / Runtime Context / Context Entity / World / Constraints 的 JSON） | **task 到了之后**：task 到来后的那一轮起，每个决策轮一条 |

**两条互不拼接**：frame 不再是「首轮那条消息的前半段」——`reasoningPrompt` 只渲染 delta。而一条会话的
**第一条 prompt 永远是 frame**（`LLMSession.Say` 在接手任何 prompt 前先 `GiveFirstPrompt`），所以没有任何一轮
会在没被告知规则的情况下跑；task 的话只走 delta，换 task、换 cycle 都不重发规则。

**上层没有 harness 之分。** 第一条 system 只做一件事：定位这只 agent 的角色（frame）。
区别仅仅是各 harness **注入时机**（`llmbackend.SystemInject`）：

| 时机 | 谁 | 怎么放同一份 frame |
|---|---|---|
| `session` | Cline | 建会话时写入原生 `systemPrompt`；之后第一条 turn 已是 task |
| `first_turn` | Cursor / Claude / Codex | 没有可用的 system 字段（Codex 桥以前用前缀冒充），作为会话的第一条 Prompt |

Cline SDK 在字段为 `undefined` 时会 `.trim()` 崩：只有 **还没有** frame 的探活 / 提前 attach 才用无语义 stub。不要再为某个后端单写政策文件。

两条各自为什么长这样，写在 `src/prompt.go` 的注释里，简单说：会话自己记着 frame，所以每轮只该付 delta 的
token；而「frame 里的值不会变」这条，是靠把每轮会变的占位符在 frame 里替换成
`{{DELTA_MARKER}}` 那句话来保证的（`reasoningDeltaPlaceholders`）。

## 其余提示词文件

| 文件 | 给谁 |
|---|---|
| `AGENT_V2.md` | planner 的策略（Decision Output Schema、规则、完成契约）—— frame 的主体；Schema 只用于后续 Decision Cycle，不用于初始化那条 |
| `FRAME_REPLY.md` | 第一条 system 自己的回复：轻量角色 ack（`type: ready` + 对自己定位的一句话），不是决策，不钉完成契约 |
| `CODE_EDIT.md` / `DEPLOYMENT_MONITOR.md` | 被委托出去的 worker（`code_edit` / `deployment.monitor`）各自的提示词；`CODE_EDIT.md` 同样要求优先读项目根 `AGENT.md` |
| `TURN_TRUNCATED.md` | 一轮被截断时的追加重试提示（`src/llm_turn_retry.go`） |
| `CHAT_MODE.md` | chat 那一轮的硬约束（「只回话、别碰计划」）—— 拼在用户的话前面（`src/chat_mode.go`） |
| `BRIEFING_NOTE.md` / `INTERRUPTED_NOTE.md` | 重启续做时那块「你自己的记录」自己的话（`src/task_record.go`；后者拼在前者后面） |
| `PROBE.md` | 探活只问的那一句（`src/llmbackend/*/probe.go`） |
| `CONSTRAINTS.json` | 运行时自己的事实与边界（不是提示词文字，而是 `{{CONSTRAINTS}}` 的值，见 [policy.md](policy.md)） |

**这一条现在没有例外**：chat 守卫、两个 note、探活那句都是文件。曾经按 harness 各写一份
`CLINE_SYSTEM.md` / `CODEX_SYSTEM.md`，那是把「SDK 有没有 systemPrompt 字段」误当成了
「agent 的初始化词要分后端写」—— 已删除。

## 怎么被加载

- **先读磁盘**：`$PROJECT_ROOT/src/agent_policy/<文件>`。所以部署上可以直接改措辞（改完重启），
  不必重新编译 —— 这是把它们做成文件的第二个理由。
- **读不到就退回二进制里带的那份**：所有这些文件都用 `//go:embed` 打进 binary。一个长跑的会话不该
  因为某个文件不在了，就让后面的决策轮失败。
  （策略 `AGENT_V2.md` 仍然是「只读磁盘」：它本来就是部署要改的那个文件。）
- **harness 读探活那句走 `llmbackend.PromptFile`**（`src/llmbackend/prompt_file.go`）：同一个规则
  （先读 `$PROJECT_ROOT` 的文件，再退回构建里带的那份），但读的人在**子包**里 —— `//go:embed` 到不了
  父目录，所以由 package autonomy 在 `init` 里把内嵌副本注册进去（`SetPromptFallback`）。
  相对路径在 `llmbackend` 里命名（`ProbePromptRel`）。
- **占位符就是一张表**：`{{TASK}}`、`{{WORLD}}`、`{{CONSTRUCTS}}`… 名字在 `src/prompt.go` 的
  `promptPlaceholders`；planner 的策略与 worker 的提示词**共用同一套名字**（一处定义，四处用）。
- **模板尾部空行会被裁掉**（`applyPromptTemplate`）：文件常以换行结尾，而值自带结尾 ——
  渲染结果由占位符决定，不由文件的最后一个字节决定。

## 加一个新提示词

1. 在 `src/agent_policy/` 建文件（占位符用上面那套名字）；
2. 在 `src/prompt.go` 里给出它的相对路径常量 + 用 `loadPromptFile` 读、`applyPromptTemplate` 填；
3. 测试（`src/prompt_files_test.go` 是模板）：**改文件 → 渲染结果跟着变**，这一条断言是
   「文字真的在文件里」的唯一证据。

`src/fixtures_test.go` 的 `preparePolicyRoot` 会**整个目录**复制到临时 `PROJECT_ROOT`，
所以新加一个提示词文件不需要动夹具。
