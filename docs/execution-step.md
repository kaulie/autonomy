# Execution Plan 与 Execution Step

## 定义

运行时的**计划**与**执行**是两张分开的记录，谁也不改谁：

- **Plan（计划）**：planner 一次 decide 的产物。计划中的每个 **step** 在**执行之前**就写全；计划一旦写下就**不可改动** —— 重规划是**新的一份计划**，哪怕它计划的内容一模一样。
- **Step（执行步）**：一次能力调用（`plan.steps[]` 里的一个）。真正跑到的每一个 step 另写一行执行记录；**计划里有、执行里没有的那一步 = 没跑**。
- **Plan 的结果不单独存**：它就是**最后一条执行 step 的状态**（跑了几个 / 最后那个是什么结局）。没人维护、也就不会漂移。

> 术语见 [execution-loop.md](execution-loop.md)：**cycle** 是「相对某个 agent 的第几轮」，**step** 是「某份 plan 里的第几个动作」。计划/执行记录一律按 **id** 关联，**不依赖 cycle**。

## 四张表

| 表 | 一行是什么 | 写法 |
|---|---|---|
| `execution_plan` | 一份计划（一次 decide） | **只 INSERT**，永不 UPDATE |
| `execution_step_plan` | 计划里的一个 step（执行前写全） | 只 INSERT |
| `execution_step` | 真正跑过的一个 step | 只 INSERT |
| `execution_step_interaction` | 一个 step 与 provider 的一次交互 | 只 INSERT |

```sql
execution_plan(id, task_id, agent_id, cycle, decision_type, reason, evidence, need,
               step_count, plan_hash,
               reply_message_id, input_message_id, task_input_message_id, reason_turn_id,
               created_at, UNIQUE(reply_message_id))
execution_step_plan(id, plan_id, idx, capability, input, expected_effect, evidence_refs, created_at,
                    UNIQUE(plan_id, idx))
execution_step(id, plan_id, plan_step_id, task_id, agent_id, cycle, idx,
               capability, provider, status, input, output, error,
               started_at, ended_at, duration_ms, created_at)
execution_step_interaction(id, step_id, seq, kind, provider, reason_turn_id, created_at,
                           UNIQUE(step_id, seq))
```

## 计划的数据来源（Plan Data Lineage）

step 的每个入参都有**来源**，而且写在计划里：要么是 planner 写的**字面量**，要么是一个**绑定**，只认两种：

```
step:<name>.output.<key>          同一计划里更早那个 step 的输出（key 必须是它声明过的 output）
world_model:asset.<id>.<kind|state>  World Model 的一个值（就是 ## World 里看到的那个资产）
```

- **能力之间的字段名各自为政、不做全局统一**：`code_edit` 报 `pr_url`、`pull_request.review` 收 `pr`（别名 `pr_url`），
  "A 的 `artifact_version` 就是 B 的 `version`" 这种**语义映射是 planner 的判断**，runtime 从不跨能力猜名字。
- **runtime 只做搬运**：绑定解析成值、值传给能力、缺的来源报出来。它**不**去共享 Context 里找键、不覆盖、不取"最新值"、
  也不替 planner 编一个入参——能力声明为 `Required` 的入参没给，**计划直接不成立**（见《五条规矩》第 1 条）。
- **不做隐式聚合/变换**：计划不得把多个输出揉成一个入参。要组合就（a）把需要的那几个值都传给能力，或（b）把变换做成**一个显式 step**，下一步绑定它的输出。
- **绑定只能往前看、只在本计划内看**：不能引用后面的 step，也不跨 cycle。上一轮产出的值在 `previous_actions` 里，planner 把它**作为字面量**写进来。
- **不因为后续步骤缺输入而重开已完成的 step**：缺依赖就报缺依赖（`previous_actions` 里能看到原因），而不是把已经满足 contract 的步骤再跑一遍。
- **runtime 自己只补一个入参：`task_id`**，而且只补给它**声明过** `task_id` 的能力（`code_edit` 用它把 worker 的 run 归属到这条 Task）。
  其余一切——包括 `instruction`——都必须来自计划：以前 runtime 会拿任务描述兜底，现在不兜了（那正是"隐式来源"）。

**两类行各存一半**（这正是 plan 行与 step 行分工的用处）：

| 行 | `input` 存什么 |
|---|---|
| `execution_step_plan`（计划，执行前写） | **planner 原始入参**：字面量按原样，绑定按 `{"source":"…"}` 原样 |
| `execution_step`（执行记录） | **实际调用入参**：绑定已解析成真值 |

**校验在计划写之前**（`src/plan_lineage.go`，`Runtime.Execute` 第一件事）：

- 绑定的语法、目标 step 是否存在且在**更前**、`key` 是否是那个 step 能力的**已声明 output**；
- step 名唯一、可寻址（无点号）；
- 每个 step 的入参键都是该能力**声明过**的（含别名）——打错的名字是拼写错误，不是值；
- 能力 `Required` 的入参都被供给。

任一条不成立 → **这一轮的计划不写、不执行**，错误里带 step 名与入参名（planner 下一轮据此改）。

## 五条规矩

1. **计划是权威，写失败就不执行**：`Runtime.Execute` 先写计划头 + **全部**计划 step（一个事务），成功之后才允许跑第一个 action。计划写不下去（如存储故障）→ 该轮直接失败，**任何 step 都不跑**。
2. **不可变**：`execution_plan` / `execution_step_plan` 没有任何 UPDATE 路径（Store 接口里也不提供）。执行记录（`execution_step`）同样是 INSERT。
3. **结果派生**：计划的结局 = 最后一条 `execution_step` 的 `status`；`ExecutionPlanOutcome(planID)` 顺手给出「计划了几步 / 跑了几步」。**没有结果列可以腐化**。
4. **「计划了没跑」用左连接表达**，不给计划行加状态列：
   ```sql
   select s.idx, s.capability from execution_step_plan s
     left join execution_step e on e.plan_step_id = s.id
    where s.plan_id = ? and e.id is null order by s.idx;
   ```
5. **一律按 id 关联，不提供按 step/cycle 取行的入口**：`plan_step_id` 指**行**而不是位置，所以重规划里看起来一样的 step 是**另一行**。Store 只有按 id / 按 task 的读法。

## 追溯：这份计划是哪条回复、哪条输入

计划行带三个 `llm_messages` 引用 + 一个 `reason_turns` 引用（**只读**，不去改 LLM 那三张表）：

| 列 | 指向 | 含义 |
|---|---|---|
| `reply_message_id` | `llm_messages.id` | planner 的这条回复（`UNIQUE`：一条回复只能产生一份计划） |
| `input_message_id` | `llm_messages.id` | 它当时在回答的输入 |
| `task_input_message_id` | `llm_messages.id` | **这条 task 自己的第一条用户输入**（同一 task 的每份计划都指同一行） |
| `reason_turn_id` | `reason_turns.id` | 产出这条回复的 LLM run（status / 用量 / 错误在那） |

于是「这份计划是从哪条回复来的」是一条 join：

```sql
select p.id, p.cycle, p.decision_type,
       m.content as reply,            -- 回复正文本身就是那份 plan JSON
       p.task_input_message_id as task_input
  from execution_plan p
  join llm_messages m on m.id = p.reply_message_id
 where p.task_id = ? order by p.id;
```

`step_count` 与 `execution_step_plan` 之和必须等于回复里的 `plan.steps[]` 条数 —— 这条等式是「我们记的计划 = 模型说的计划」的硬证据（测试里钉着）。

## 一个 step 与 provider 的交互

`execution_step_interaction` 让**一个 step 可以挂多次交互**（今天的代码是 1:1，但模型不预设）：

| kind | 含义 | `reason_turn_id` |
|---|---|---|
| `llm` | 能力委托给了 agent：指向**那个 worker 自己的 run** | 有 |
| `http` | 能力直接调 API（GitHub / 部署控制面） | 无（**请求/响应细节还没有落库通道**，见下） |
| `local` | 能力在进程内改世界（asset store），没有 provider 交互 | 无 |

`execution_step.provider` 是**能力的 provider**（`github` / `agent-control-plane` / `cursor` / `autonomy`…），`execution_step_interaction.provider` 是**这次交互的 provider**（LLM 后端 `cline`/`cursor`）。两者不同是有意的：step 说「这一步属于谁」，interaction 说「它跟谁说话」。

## 能回答什么

```sql
-- 一次任务的计划 vs 执行
select p.id, p.cycle, p.decision_type, p.step_count,
       (select count(*) from execution_step e where e.plan_id = p.id) as executed,
       (select status from execution_step e where e.plan_id = p.id order by e.idx desc limit 1) as outcome
  from execution_plan p where p.task_id = 'task-18' order by p.id;

-- 第 N 份计划计划了什么、哪一步没成功
select s.idx, s.capability, s.input, e.status, e.error
  from execution_step_plan s left join execution_step e on e.plan_step_id = s.id
 where s.plan_id = ? order by s.idx;

-- 在原地打转？（同一份计划内容被反复规划）
select plan_hash, count(*) from execution_plan where task_id = ? group by plan_hash having count(*) > 1;

-- 这一步跟谁说过话
select i.seq, i.kind, i.provider, i.reason_turn_id, r.status
  from execution_step_interaction i left join reason_turns r on r.id = i.reason_turn_id
 where i.step_id = ? order by i.seq;
```

## 还没做的（有意）

- **非 LLM 交互的过程细节**（`gate` / `request` / `response` 事件）：`execution_step_interaction` 只有 kind/provider，请求与响应还没有落库通道 —— 那是下一步（provider trace），落库时挂到**交互行**（`step_id` + `seq`）上，不再挂到 step 上。
- **prompt 里的 id**：`previous_actions` 仍只带 input/output/error（不塞 trace 内容，也不塞 id），避免每轮被自己的记录放大。

## 代码位置

| 关注点 | 文件 |
|---|---|
| 类型与派生结果 | `src/execution.go` |
| Store 契约 | `src/store.go`（`CreateExecutionPlan` / `AppendExecutionStepPlans` / `AppendExecutionStep` / `AppendExecutionStepInteraction` / `ExecutionPlanOutcome` …） |
| sqlite 实现 | `src/sqlite_execution.go`（DDL 在 `src/sqlite_store.go`） |
| 写序（先计划、后执行） | `src/runtime.go`（`Execute` / `recordPlan` / `recordStep`） |
| 入参来源（值与绑定） | `src/plan_input.go`（`StepInput` / 绑定语法）、`src/plan_lineage.go`（校验与解析）、`src/action.go`（解析后调用能力） |
| 追溯来源 | `src/llm_trace.go`（`Origin`）、`src/reasoner.go`、`src/decision.go`（`DecisionOrigin`） |
