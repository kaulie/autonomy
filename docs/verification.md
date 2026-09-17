# Verification

## 定义

Verification 判定：**当前世界状态是否满足 Completion Contract**。

它把「执行成功」与「目标达成」分开：`printer.print(image)` 调用成功，不等于纸上的图像正确。

## 四句话

| 谁 | 负责什么 |
|------|----------|
| Planner | 定义什么必须成立（Goal → Plan → Completion Contract），并**预先绑定**每条判据的证据槽 |
| Executor（Capability / agent） | 干活，产出事实（一个 step 的 output、一次状态变化）；它不需要知道合同的存在 |
| Runtime | 按 Planner 的绑定**确定性地**把事实填进证据槽 —— 不搜索、不猜测、不补默认值 |
| Verifier | 拿填好的证据去**权威来源**查真，和合同说的比 → pass / fail / inconclusive |

## 职责

- **负责**：按 Contract 的判据，从权威来源取得事实，产出通过 / 未通过 / 不确定。
- **不负责**：选择下一步（那是 Planner）；给执行方的自报成功背书；自己去「找一个证据」。

## 判据（Criterion）

一条判据 = 一个必须成立的事实：

| 字段 | 含义 |
|------|------|
| `requirement` | 事实本身，Planner 用自己的话说 —— 作为记录 |
| `evidence` | **证据槽**：这个事实的对象将从哪里来（`step:<name>.output.<key>` 或 `world_model:asset.<id>.<field>`），和 plan input 用同一套绑定语法 |
| `expect` | 事实的可判定形态：`{"exists": true}`，或 `{"field": "…", "equals": "…"}` |
| `check` | 可选：这份事实的真相在哪个权威能力那里（runtime 的 registry 里没有该证据的 reader 时才需要）。它不是「怎么验证」，是「真相在哪里」，且必须是只读能力 |

Planner 在计划时**还不知道** artifact 的 id —— 那个 id 要到执行时才产生。所以它不是猜一个 id，而是写「哪个 step 的哪个 output 将来应该成为这条判据的证据」：这是语义上的预先绑定。

## 事实链

```
Plan（Planner 预绑定 evidence 槽）
  ↓ Execute
Capability 产出 output（它只回答"我产出了什么"）
  ↓ Runtime 解析绑定、填槽
Evidence Slot = {slot, reference}（reference 是证据，不是真相）
  ↓ Verifier 去权威来源查
World Model / 只读能力 → pass / fail / inconclusive
```

**执行方给的是 Evidence Reference，不是 Evidence Truth。** 一个 step 的 output 只能回答「去问哪个对象」，它不能证明那个对象存在、也不能证明它处于什么状态：**Agent 可以提供 Evidence Reference，但不能提供 Evidence Truth。**

## 权威来源（谁来回答）

- **World Model**：本地 asset 的 `kind` / `state`，runtime 直接读。这类证据的槽本身就是读取（`world_model:asset.<id>.<field>`），不需要再去问谁。
- **只读能力**：证据的**产出者**说明它是什么东西（`service.deploy` 的 output 是一条 pipeline），运行时的 `verificationReaders` 表说明这类证据该问谁、用什么 input 问（`service.deploy → deployment.monitor`）。表里没有、判据也没写 `check` → **inconclusive**。
- **问的方式和 step 调用同一条路**：runtime 用它调用能力的方式调用读者 —— 判据绑定的入参原样传下去，不额外添加、不覆盖任何绑定；只有那个「runtime 自己只补一个入参」的 `task_id` 照样补给它（见 [execution-step.md](execution-step.md) §Runtime Responsibility）。所以一个会 acquire worker 的读者（`deployment.monitor` 就是）产出的 agent 行与 `reason_turns` 行**挂在同一条 Task 下**：验证所问的对象和产出它的那一步，属于同一个任务。
- **Planner 指定的 `check`**：该域还没有注册 reader 时，判据可以直接写明权威能力。

## 判定与用途

| 权威来源的回答 | verdict |
|------|------|
| 读到值，且等于 `expect` | `pass` |
| 读到值，但不等于 `expect`；或对象根本不存在 | `fail` |
| 读不到：能力报错 / 没报那个字段 / 没有权威来源 / 槽解析不到 / 判据没说清什么叫成立 | `inconclusive` |

- 只有 `done` 被验证：`blocked` / `need_input` 不是「完成」的声称，`need_input` 交给人的那一步本身就是请人来判。
- 每条判据都 `pass`，`done` 才收束成 `completed`；否则**这一轮失败**，verdict（判据名 + 期望 + 权威来源给的事实）进 `previous_actions`，Planner 重规划 —— 继续观察、补验证、重做、换策略、请求人，都是它的选择。预算由 `MaxSteps` 兜住。
- 运行结束时最后那个答复是 `done` 而它没验过 → 任务落 `unverified`，从来不是 `completed`（[task.md](task.md)）。
- `INCONCLUSIVE` 必须存在：Verifier 不该被迫二选一，「看起来没问题」不是 pass。

## 落库

- `completion_contract`：一条判据一行，**首轮写一次**（`INSERT OR IGNORE`）——「合同不可改」是 schema 的事实，不是谁记得去检查。
- `verification`：一次判定一行，只追加：`requirement` / `method`（`world_model`、`registry:<能力>`、`declared:<能力>`）/ `evidence`（槽 + 解析出的 reference）/ `expected` / `observed` / `result` / `reason`。

两张表的形状见 [execution-step.md](execution-step.md)。

## 关系

- 锚定 [Completion Contract](completion-contract.md)：合同由 Planner 在第一轮声明，被 Verifier 对照世界求值
- 消费 [Action](action.md) 的证据与 [Asset](asset.md) 的观察，证据最终还是尽量落到 World Model
- 结果回到 [Agent](agent.md) / [execution-loop](execution-loop.md)：Done 或 Continue
- 与 [Capability](capability.md) 的自报成功分离：Capability 成功 ≠ 合同满足
- 长期方向：把人类判断持续沉淀为可机器验证的判据（hello 场景：对假服务做 health check）

## 不变式

1. External verification over self-declared success：执行成功不短路验证，执行方的 output 是证据引用而不是真相。
2. Fail / Inconclusive 必须可驱动再规划，而不是被吞掉。
3. Verification 不偷偷改写 Contract：合同在第一轮被钉住，之后的运行只对照它 —— 一个 `done` 不能挑一份对自己有利的标准。
4. Verifier 不找证据：槽是 Planner 绑的、Runtime 填的；找不到就是 inconclusive，不是「我找找有没有别的」。
5. Verifier 不改世界：`check` 必须是只读能力。
6. 判不了的判据 → inconclusive，绝不默认 pass；一个任务只要有一条判不了，它就不能以 `completed` 结束。

## 还没做的（说清楚边界）

- **能回答的域很窄**：本地 World Model 的 `state` / `kind`，以及部署事实（`deployment.monitor`）。artifact registry、service registry、health check 之类还没有对应的权威读取能力 —— 没有权威来源的事实一律 `inconclusive`，并把这个原因交给 Planner。
- **World Model 还没有 provenance / state transition**：状态证据只有当前值，还没有「谁在什么时候观察到的、变化前后是什么」。
- **`check` 由 Planner 显式指定**是过渡口子：registry 长全后应当收掉。
- **agent-backed verifier**（确定性判不了时用独立 agent 判断）、**human**（`need_input`）、**Trust 回流**：都还没做（见 [trust.md](trust.md)）。
