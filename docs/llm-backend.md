# LLM 后端（cursor / cline / codex）与怎么切换

一次决策轮、一次委派，真正打给模型的那一步发生在 **runtime 进程**里（本地是
`go run ./cmd/autonomyd`，部署时是 `bin/autonomyd`）。`cmd/autonomy` 只是 HTTP 客户端
（[http-api.md](http-api.md)）——所以 `go run ./cmd/autonomy` 打出来的
`decide: …` 是 **runtime 的结论**，给它加环境变量没有任何作用。

**切后端 = 让 runtime 以另一个后端启动。** 这就是全文。

## 谁决定后端

`AUTONOMY_LLM_BACKEND`（`src/llmbackend/cline_client.go: DefaultBackend()`）：

| 值 | 后端 | 说明 |
|----|------|------|
| 空 / `cursor` | Cursor SDK 桥 | 默认（standalone 二进制） |
| `cline` / `cline_sdk` | Cline SDK 桥（[cline-reasoner.md](cline-reasoner.md)） | Node 桥 + `@cline/sdk` |
| `codex` / `codex_sdk` | Codex SDK 桥（`src/codexsdk/bridge`） | Node 桥 + `@openai/codex-sdk`（它自己驱动 `codex` CLI） |

The three harnesses live in `src/llmbackend/{cursor,cline,codex}`; a backend with no harness
linked in is refused by name (import `src/llmbackend/all`). Codex is the newest: its thread is
persisted by the CLI under `~/.codex/sessions`, so `codex.resumeThread(id)` continues the same
conversation after a restart — the agent row's recorded thread id is what the next process
resumes, the same contract the other two keep.

它决定 `LLMReasoner`（planner 的决策轮）与 capability 拿到的 agent（`code_edit` →
`Runtime.AcquireAgent`，`src/runtime.go`）跑在哪个 provider 上。`AUTONOMY_REASONER=local`
是离线 reasoner，与后端无关。

## 当前在哪个后端？问 runtime 自己

```bash
curl -s 127.0.0.1:4300/health
# {"status":"ok","llm_backend":"cursor","llm_model":"composer-2"}
```

`/health` 答的是**本进程将要用的**后端与它默认的模型（`src/http_server.go`）。`llm_model`
在 Cline 且没设 `AUTONOMY_CLINE_MODEL` 时是缺省的——那时模型由桥按 `cline auth` 保存的
provider/model 解析，runtime 自己并不知道。runtime 的日志里也有同样的事实，例如
`[autonomy] agent agent-10001 (cursor) continues task task-29 on a new session`。

## 本地切到 Cline

```bash
./scripts/install-cline-bridge.sh             # npm install @cline/sdk 到 src/clinesdk/bridge（Node ≥ 22，node_modules 不入库）
export AUTONOMY_LLM_BACKEND=cline
export AUTONOMY_CLINE_PROVIDER=deepseek       # 可省：用 `cline auth` 保存的 provider
export AUTONOMY_CLINE_MODEL=deepseek-v4-pro   # 可省：同上；AUTONOMY_LLM_MODEL 是 Cursor 的，不通用
export PROJECT_ROOT=$(pwd)
go run ./cmd/autonomyd                        # runtime 起在这里；另开一个终端跑 cmd/autonomy
```

桥脚本默认按 **cwd** 找 `src/clinesdk/bridge/bridge.mjs`
（`src/clinesdk/bridge.go: defaultBridgeScript()`，也会试 `../`、`../../`）；它不在时用
`AUTONOMY_CLINE_BRIDGE_SCRIPT` 指定绝对路径，Node 不在 PATH 上用 `AUTONOMY_CLINE_NODE_BIN`。
端口/后端/模型这些"运行期配置"的完整清单见 [cline-reasoner.md](cline-reasoner.md) 的环境变量表。

## 部署的 runtime 切到 Cline

部署包只有 `bin/autonomyd`、`scripts/`、`src/agent_policy/`、`src/clinesdk/bridge/` 和
`backend/`：**cline 桥随包发出**（源码 + 依赖包 `bridge-deps.tgz`，`build.sh` 在打包时
`npm ci --omit=dev` 装好再压）。`scripts/start.sh` 启动时把依赖解到**服务目录之外的缓存**
（`${AUTONOMY_CACHE_DIR:-$RUNTIME_DIR/.cache}/cline-bridge/<tarball 的 sha256>`，部署平台的
rsync 不碰 `.cache/`，所以每次部署不用重解；按 sha 去重、只留最近两份），再在源码目录建一个
`node_modules` 符号链接（Node 的裸包解析要从脚本所在目录往上找，ESM 又不认 `NODE_PATH`），
最后**强制**把 `AUTONOMY_CLINE_BRIDGE_SCRIPT` 指到包里的桥。所以部署运行时切 Cline 是两步：

1. 在 `~/runtime/<service>/backend/.env` 里加（`scripts/start.sh` 生成的模板里这几行已有注释版）：

   ```bash
   AUTONOMY_LLM_BACKEND=cline
   AUTONOMY_CLINE_PROVIDER=deepseek
   AUTONOMY_CLINE_MODEL=deepseek-v4-pro
   # 桥不用指：包自带（src/clinesdk/bridge），start.sh 会解包依赖并指过去。
   # 只有当包里没有桥（老包）或你要用外部桥时，才写这一行：
   # AUTONOMY_CLINE_BRIDGE_SCRIPT=/path/to/some/checkout/src/clinesdk/bridge/bridge.mjs
   ```

2. **走部署平台重启**（`~/runtime/agent-control-plane-deployment` 的 UI / 管线）。不要在
   agent 进程里同步跑重启脚本——那会把网关一起带走。

为什么 start.sh 要*强制*包里的桥（而不是尊重 `.env` 里的值）：`backend/.env` 每次部署都保留，
一条指向某个 checkout 的路径会把桥钉死在那份没人更新的代码上（这正是 2026-09-21 线上那次
的现场：`.env` 指着 `~/Projects/autonomy/...`，桥的改动永远不生效）。要跑外部桥，就把那行
写在 `.env` 里并接受它不随部署更新（或者让包里那份缺席）。

起来后 `curl -s 127.0.0.1:4300/health` 应看到 `"llm_backend":"cline"`，`backend/server.log`
里会出现 `[cline-bridge] …`（还有 start.sh 的 `cline桥=…` 一行，写的是它实际用的路径）。

### 代码边界：`src/llmbackend` 是唯一知道 provider 的地方

两个后端的**全部差异**都在一个模块里，而且**各占一个文件夹**（这也是它存在的理由）：

```
src/llmbackend/                    核心：词表 + Session 门面 + 事件词表 + 注册表
  doc.go / backend.go / values.go   Backend/Provider/Mode 词表、环境选择与默认值、PROJECT_ROOT、共享小工具
  session.go                        Session（Attach / Prompt / PromptText / Dispose + Mode/Resumed/… 观察点）
                                    + Host 接口（Facts() 与写回）+ missingHarness（没接 harness 就明说）
  registry.go                       Harness = {Backend, Provider, New, Adapter, DefaultModel, CloseClient}
                                    + Register / Harnesses / ModelDefault / CloseClients
  events.go / events_kind.go        中性 Event 词表（kind / channel / key / status / usage）+ 适配器接口与注册表
  cursor/                           一个 harness：session.go（Resume/Create）、client.go（进程内共享桥）、
                                    events.go（原生流→中性事件）、trace.go、register.go（自注册）
  cline/                            另一个 harness：session.go（按 (mode,cwd) 常驻 + transcript 播种）、
                                    client.go、events.go、register.go（自注册）
  all/                              `import _ ".../llmbackend/all"`：把这个 build 要跑的 harness 链进来
```

**依赖只朝一个方向**：runtime → 模块 → harness；harness **不** import runtime（它用 `Host` 接口拿事实、写回它拥有的东西），核心也**不** import harness（它通过注册表拿实现）。这也是为什么**中性事件词表必须留在核心**：适配器产出它、并注册进核心的注册表 —— 只搬「客户端 + 会话」会让依赖成环。

**上层只依赖接口**：runtime（以及 store / prompt / 一切消费者）编译期只认 **`llmbackend.Session` 这个接口** —— `Agent.llm` 就是它，类型里没有 `cursor`/`cline` 的任何东西。「按所选 provider 调不同实现」发生在**运行时**：`llmbackend.New(host)` 用 `host.Facts().Backend` 去注册表取那个 harness，用它的 `New` 造出实现并包在接口后面。加一个 harness，上层一行不用改、也不需要重新编译出分支。

**runtime 侧**只剩它自己知道的事：agent **是**哪个后端（行）、身份/生命周期/工作区、`llm_agent_id`、frame 记账；一轮 turn 走 `Agent.llmSession()` 一扇门，关桥走 `llmbackend.CloseClients()`（不点名任何 harness）。

**接入一个新 harness（cursor / cline / codex 之外，例如 deepseek）**——四步，核心与 runtime 一行不用改（codex 就是照这四步加的）：

1. 新建 `src/llmbackend/<name>/`：实现 `llmbackend.SessionImpl`（`Attach/Prompt/Dispose/SessionID/Mode/Resumed/ResumedFrom`）+ 一个 `llmbackend.StreamAdapter`（把它的原生事件映射成中性 `Event`）；
2. 写 `register.go`：`func init() { llmbackend.Register(llmbackend.Harness{Backend: …, Provider: …, New: …, Adapter: …, DefaultModel: …, CloseClient: …}) }`；
3. `src/llmbackend/all/all.go` 加一行 blank import；
4. `AUTONOMY_LLM_BACKEND=<name>` 即可选中（`DefaultBackend` 里 `case` 一行，若它要被默认选中）。

**保持这条边界的规则**（改动时照着做，也是验收标准）：`src/llmbackend/{cursor,cline}/` 之外**没有非测试文件** import `src/cursorsdk` 或 `src/clinesdk`。

### 启动前自检：桥不可用 = 这次部署失败（不是起个坏服务）

`scripts/start.sh` 在拉起 `autonomyd` 之前会检查**所选后端**的桥（`AUTONOMY_REASONER=local`
跳过；要临时放行设 `AUTONOMY_SKIP_BRIDGE_CHECK=1`）：

- `cline`：桥脚本存在、依赖能被 Node 从脚本目录向上解析到（`node_modules/@cline/sdk`），并且**真加载一次**
  （`printf '{"id":"…","cmd":"ping"}' | node bridge.mjs` 要回 `"type":"ready"`；不联网、不用凭据）；
- `cursor`：`CURSOR_SDK_BRIDGE_URL` 有值（附到外部桥），或 `CURSOR_SDK_BRIDGE_BIN` 指向可执行文件——
  **包里的 `bin/cursor-sdk-bridge` 会被自动指上、并覆盖 `.env` 里的值**（与端口、cline 桥同一条规矩：
  `.env` 每次部署都保留，一条指向某个 checkout 的路径会把桥钉死在那份没人更新的代码上）。
  `CURSOR_SDK_BRIDGE_URL` 是一种**模式**而不是一条路径，设了它就不动。

两份桥都随发版包发出：cline 的依赖由 `build.sh` 装/压，cursor 的二进制在 checkout 里没有时
**自动按 pin 的版本取**（先看构建机缓存 `~/.cache/autonomy/cursor-sdk-bridge/<版本>/`，再跑
`scripts/fetch-bridge.sh`；`AUTONOMY_SKIP_FETCH_CURSOR_BRIDGE=1` 可关掉下载）。取不到只警告、不失败
（用哪个桥由部署的**所选后端**决定，启动自检在那里把关）。

**不过就 `die`（非 0 退出）**，于是部署平台把这次部署判为失败、线上留在上一个可用版本——桥是 LLM
后端唯一的执行通道，缺了它 `/health` 照样 `ok`，但每个任务都会失败在「ping the bridge」那一步；
与其静默降级，不如让部署失败（`.cache` 里保留上一版的依赖，回滚不用重新解包）。

对应的构建侧约束：`build.sh` 拿不到 cline 桥依赖时**直接报错**（不再只警告），三条来源依次是
（1）checkout 的 `node_modules`、（2）构建机缓存 `${AUTONOMY_CACHE_DIR:-~/.cache/autonomy}/cline-bridge-deps/<package-lock.json 的 sha256>.tgz`、
（3）`npm ci --omit=dev`（`AUTONOMY_NPM_BIN` 可指定 npm）。缓存命中时**构建不需要 npm、也不需要网**；
确实要发一个不带桥的包时用 `AUTONOMY_ALLOW_MISSING_CLINE_BRIDGE=1`（例如部署的 `.env` 把
`AUTONOMY_CLINE_BRIDGE_SCRIPT` 指到了外部 checkout）。


### Cline 的会话落盘在哪（重启怎么续）

Cline SDK 把每个会话写进 **Cline 数据目录**，默认 `~/.cline/data/sessions/<sessionId>/`：

```
cls-….json           会话 manifest（provider / model / cwd / interactive / status / messages_path …）
cls-….messages.json  transcript（role + content blocks），turn 边界追平
```

`CLINE_DIR` / `CLINE_DATA_DIR` / `AUTONOMY_CLINE_DATA_DIR` 可以把它指到别处（`config.mjs` 找
`cline auth` 的 `settings/providers.json` 用的是同一套变量）。**这个目录在部署的 runtime 之外**
（不是 `backend/`），所以换版不会把它带走。

会话说到底活在 bridge 进程里，重启就没了，所以续的是**对话**而不是会话对象：旧会话 id 记在
agent 行上（`agents.llm_agent_id`，只记 planner 的 plan 会话），新进程 attach 时把它交给 bridge，
bridge `readMessages` 读回 transcript 再开一个新会话播种给它（`src/clinesdk/bridge/resume.mjs`；
为什么不是「用老 id 直接 start」那儿写着）。读不回来就从头开——记录层（`briefing`）仍然保证不会把
续做当成新任务，见 [session.md](session.md)。

## 触发这一切的那条报错怎么读

```
[task task-29] status error: decide: empty model response (status=error msg=Increase limits
for faster responses You're out of usage. Switch to Auto, or ask your admin to increase your limit
to continue.)
```

`text_bytes=0`：**模型一个字都没答**，provider 把这个 run 以 `status=error` 结束。账号额度耗尽、
限流、模型下架都会长成这样——不是 prompt 的问题，也不是 autonomy 的 bug。两个后端
（`src/llmbackend/cursor.go` / `src/llmbackend/cline.go`）都把这种 run 记成失败并在错误里点名
`AUTONOMY_LLM_BACKEND`：空答案唯一有效的修法是换后端 / 换账号 / 换模型（Cursor 侧换模型是
`AUTONOMY_LLM_MODEL`）。重试没有意义——不在 `AUTONOMY_LLM_TURN_RETRIES` 的覆盖范围内，
provider 报错、余额不足都不重试（`src/llm_turn_retry.go`）。

再接别的 provider 是 `LLMStreamAdapter`（[llm-event-stream.md](llm-event-stream.md)）的事，
不是这条路径的事。

## 别和 web-cursor 的 `provider=cursor` 搞混

`AUTONOMY_LLM_BACKEND=cursor` 说的是 **本 runtime 进程**里打模型走哪座桥。
控制面任务上的 `provider=cursor` 说的是另一件事：这条 task 由 **web-cursor agent**
在独立 workspace 里改代码、开 PR（分支名带 task id，合入走 GitHub）。两条路径都叫
cursor，但一个是 LLM 后端，一个是任务执行器；改这边的环境变量不会让那边换人。
