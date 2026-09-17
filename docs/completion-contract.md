# Completion Contract

## 定义

Completion Contract 是执行系统的**完成锚点**：在 Agent 可自由选择路径的前提下，规定「什么状态才算真正完成」。

## 职责

- **负责**：定义期望世界状态、成功判据、验证方法与所需证据。
- **不负责**：规定到达该状态的步骤；被 Capability 的「执行成功」声明所替代。

## 核心字段（逻辑）

| 字段 | 含义 |
|------|------|
| Expected State | 目标世界状态（例如服务 Healthy） |
| Success Criteria | 可判定的条件集合 |
| Verification Method | 如何验真（规则、探针、测试、传感器、人） |
| Evidence | 完成时需要留下的证据形态 |

示例（逻辑，非实现）：

- Goal：让服务恢复正常
- Contract：`/health` 返回 200；核心 API 测试通过；错误率低于阈值；新版本已部署

## 关系

- 挂在 [Task](task.md) 上，约束 [Agent](agent.md)
- 由 [Verification](verification.md) 对照世界状态求值
- 与 [Capability](capability.md) 的自报成功分离：Capability 成功 ≠ Contract 满足
- 受 [Policy](policy.md) 约束（例如禁止降低验证严格度）

## 不变式

1. Agent 可以选择 Diagnose → Fix → Deploy → Health Check，或其他完全不同的路径；**不能**自行宣布「这样也算完成」。
2. 验证是外置的：相对执行方独立，或至少相对「自述成功」独立。
3. 未满足 Contract 时，循环应进入 Continue / Re-plan，而不是静默 Done。

## 实现

契约是**数据**，不是散文（`src/completion_contract.go`）：

- planner 在**第一轮**答复里给出 `completion_contracts.steps[]`；运行时把每一条钉进 `completion_contract`
  （一条判据一行，`INSERT OR IGNORE`），并在第一轮就先校验它（槽可读、本计划里那个 step 确实声明了这个 output、
  `check` 指向的能力存在且报得出要比较的字段）。之后每轮的 Runtime Context 都带回**这份钉住的**合同。
- 一条判据 = 一个必须成立的事实：`requirement`（事实，planner 的话）+ `evidence`（**证据槽**：这个事实的对象
  将从哪里来，与 plan input 同一套绑定语法）+ `expect`（事实的可判定形态：`exists`，或某个字段等于某个值）+
  可选 `check`（真相在哪个权威能力那里 —— 不是「怎么验证」）。
- `done` 由 [Verification](verification.md) 对照这份契约判定：证据槽由**运行时**填（解析 planner 的绑定，
  范围是这个 task 的步历史），判定来自权威来源。不成立（`fail` / `inconclusive`）就是这一轮失败，planner 拿着
  verdict 重规划；运行结束时最后那个 `done` 仍没验过，任务落 `unverified`。

于是不变式 1 与 3 在这里变成了机制而不是纪律：planner **不能**自己宣布完成（它说的话要过权威来源），也**不能**
在最后一轮把标准改弱（先写下的那条就是标准 —— 后面的重述被 `UNIQUE(task_id, idx)` 挡掉）。

## 演化注记

V1 可用极简 Contract（例如假服务 health 返回成功即 Done）。字段集合应预留 Expected State / Criteria / Method / Evidence，避免日后只能靠硬编码 if 判断完成。
