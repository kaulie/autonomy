# 测试怎么放：位置、命名、以及为什么不能随便搬

这份文档只有一件事要说清：**一个测试文件该放在哪、叫什么名字**，以及那三条 Go 强加的边界。

## 三条边界（先看这个，能省下很多争论）

1. **同包测试必须和被测包同目录。** `package autonomy` 的 `_test.go` 只能待在 `src/` 里 ——
   它能碰未导出的东西（`agent.llm`、`ensureLLMSession`、内部夹具），代价就是搬不走。
2. **黑盒测试（`package autonomy_test`）也必须在被测包的目录里。** Go 用「文件所在目录」决定
   测谁，所以没有「把某个包的测试放到别的文件夹」这回事。
3. 因此「测试分门别类放到各自模块文件夹」只有三条真实路径：
   - **被测代码进子包** → 测试跟着文件一起进去（本项目 `src/db`、`src/llmbackend/*`、
     `src/bridgesdk`、`src/context_builder`、`src/capability/*` 都是这么来的）；
   - **只依赖导出 API 的测试** → 可以放进一个**独立包**（`src/tests/<模块>/`，
     `package tests` 之类，import 根包），但它只能看见导出面；
   - **留在 `src/`** → 那就靠**命名**把它分门别类（下面这条）。

## 现状

| 位置 | 内容 | 数量 |
|---|---|---|
| `src/*_test.go` | 根包（`package autonomy`）的测试：同包测试 + 夹具 | 67 个 |
| `src/db`、`src/llmbackend/*`、`src/bridgesdk`、`src/clinesdk`、`src/codexsdk`、`src/context_builder`、`src/capability/*` | 各自子包的测试，**跟着代码在同一目录** | 各自若干 |
| `src/bridgesdk/fakebridge`、`src/codexsdk/fakebridge` | 假桥（测试替身，普通包，谁都能 import） | 2 个 |

根包里的 67 个文件里，**44 个**用了内部夹具或内部符号（`openStore` / `resumeTestStore` / `agent.llm` …）
→ 按边界 1，它们必须留在 `src/`。剩下的要么已经在子包里，要么还需要内部符号。

## 命名：`<模块>_<主题>_test.go`

`src/` 里按模块前缀排，一眼能看出一个测试属于谁：

| 前缀 | 覆盖 |
|---|---|
| `task_*` | 任务、上下文/项目引用、完成契约验证入口 |
| `agent_*` | agent 生命周期、初始化、恢复、账号、消息 |
| `plan_*` / `decision_*` | 计划与决策（含 lineage、规则） |
| `execution_*` / `turn_*` | 执行与 reason turn（含重试） |
| `llm_*` | 会话、消息日志、事件流、frame |
| `cline_*` / `cursor_*` | 两个 harness 的接线（`*_live_test.go` = 真打 provider，默认跳过） |
| `prompt_*` | 提示词渲染（frame / delta / worker） |
| `store_*` | store 端口与引擎（`src/db` 里另有一致性快照） |
| `http_*` / `ui_*` | HTTP 面与页面 |
| `accounts_*` | 账号池 |
| `graceful_*` | 优雅重启、drain、开机自愈 |
| `fixtures_test.go` | **唯一的**夹具入口（`openStore` / `seedTestAccounts` / …） |

`fixtures_test.go` 是刻意的：一个测试要一个能跑的 runtime（store + 池子 + 策略根），
就调它，不要在文件里各自造一套。

## 加一个测试

- 测**行为**、且只碰导出面 → 放进 `src/tests/<模块>/`（新目录，`package tests`），
  import 根包；这样它天然不会依赖内部实现。
- 需要**内部符号** → 留在 `src/`，文件名按上表的前缀。
- 需要**假桥/假引擎** → `src/bridgesdk/fakebridge`、`src/codexsdk/fakebridge`（普通包，可直接 import），
  或 `src/db` 的引擎测试（两引擎一致性快照会同时跑 sqlite 与 postgres）。
- 真打 provider 的测试：`*_live_test.go` + 一个环境变量门（默认跳过，别让 CI 花额度）。

## 还能更好（未做，需要单独决定）

**把 UI 抽成 `src/ui/` 子包**：`ui_home_page.go`、`agent_dashboard_page.go`、`accounts_page.go`、
`agent_events_page.go`、`agent_messages_page.go` + 它们 5 个 handler 与 5 个测试一起进
`src/ui/`，页面测试就真的「在自己的模块文件夹里」了。代价是三件事同时发生：

1. handler 不能再挂在 `HTTPServer` 上 → 需要一个很小的接口（页面只用 `Autonomy` 的几个读）；
2. 路由表要能收外部注册（`src/llmbackend` 那套 `Register` 已经证明可行），否则 `autonomy` ↔ `ui` 成环；
3. 契约测试（`src/contract_test.go`）目前只读 `http_server.go` 的注解，要改成扫描多文件。

这是一次「结构 + HTTP 面」的改动，值得做，但该单独一次评审。
