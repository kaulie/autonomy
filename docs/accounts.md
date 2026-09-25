# 账号池（harness accounts）

**一个账号 = 一个 harness（`cursor` / `cline` / `codex`）+ 一个 vendor + 一把凭据。**

运行时的凭据**只从这里来**：不再有 `AUTONOMY_*_API_KEY` / `CURSOR_API_KEY` 这种注入 ——
`.env` 只留路径、端口、store DSN 这类部署参数，不留给「谁付费」。这条规矩的写法是
`src/agent_account.go` 里的解析：会话 attach 之前先解析账号，解析不到就**拒绝**（错误里
指向 `/accounts`），不静默换一个人跑。

## 为什么需要 vendor

三个 harness 对「vendor」的含义不同：

| harness | vendor | 说明 |
|---|---|---|
| `cursor` | `cursor` | Cursor 没有二级；这一层只是为了让三种账号长得一样 |
| `cline` | `deepseek` / `minimax` / `anthropic` … | Cline 的 key 属于某个 LLM 厂商，离开 vendor 一把 key 没有意义 |
| `codex` | `openai`（默认） | Codex 走 OpenAI；写了 `baseUrl` 就以它为准 |

> **与 web-cursor 的一处差异（如实说明）**：web-cursor 的账号**不带 model** —— 它把 model 放在
> runtime/project settings 里、再由 task 输入覆盖。autonomy 目前**没有 settings 存储**，所以
> 「这个账号跑哪个 model」暂时挂在账号上（`model`，可省 = 交给该 harness 自己解析）。要完全对齐
> 就得先加一层 settings（或把 model 挪到 task 输入上）—— 那是一个独立的改动。

### 工作区根目录：**必填、必须是绝对路径、且互斥**

（与 web-cursor 的账号池同一条规则；互斥是它没有、我们要的那一条。）

- `agent_root_workspace` **必填**，且必须是**绝对路径**（相对路径会被拒）；
- 每个账号**独占**一个根：写入时检查、并由唯一索引兜底 —— 两个账号拿到同一个根会被拒绝
  （错误点名已经占用它的那个账号）；
- 路径先归一化（`…/a/` 与 `…/a` 是同一个主张）；
- 每只 agent 在自己的子目录里：`{agent_root_workspace}/{agent-name}/`。

**凭据可以空着**：那样就跑 provider 自己保存的 auth（`cline auth` / `codex auth`）——「这台
机器已经登录过」是最常见的用法，账号只要说清 harness/vendor/model 即可。

## API

| 端点 | 说明 |
|---|---|
| `GET /api/accounts` | 列出账号（`harness` / `vendor` / `enabled` 可筛）。**key 只回掩码** |
| `POST /api/accounts` | 新增（`harness` + `label` 必填；`vendor`/`model`/`apiKey`/`baseUrl`/`agentRootWorkspace`/`enabled`/`isDefault` 可选） |
| `PATCH /api/accounts/{accountId}` | 改（只传要改的字段；`apiKey` 不传就保持原样） |
| `DELETE /api/accounts/{accountId}` | 删除 |
| `POST /api/accounts/{accountId}/verify` | 探活：默认只**加载桥**（免费）；`?live=1` 用该账号的凭据**真跑一轮** |
| `GET /api/accounts/vendors?harness=` | 该 harness 可选的 **vendor 列表**（UI 的下拉数据） |
| `GET /api/accounts/models?harness=&vendor=` | 该 vendor 的 **model 列表**（可为空 = 由 harness 自己解析） |

掩码不是「小心返回」：领域类型 `autonomy.Account` 的 key 是 `json:"-"`，**从类型上就渲染不出来**。

页面：**`GET /accounts`** —— 与 dashboard 同款自包含页（0 构建），列表/新增/编辑/启停/设默认/
verify/删除。它写 key 一次、永不读回，与 API 同一条规矩。agent 状态页（`/dashboard`）多一列
`Account`，写明每只 agent 花的是谁的额度。

## vendor 与 model 是**选**出来的，不是打出来的

- 页面上的 vendor 是**下拉**，数据来自 `GET /api/accounts/vendors`：cline 会去问桥，桥问 Cline SDK
  （本机实测 **216** 个 provider）；拿不到桥时回落到一份静态清单（与 web-cursor 的
  `FALLBACK_CLINE_VENDORS` 同一份：deepseek / minimax / anthropic / openai / openai-compatible）；
- model 是**带建议的输入框**（datalist），数据来自 `GET /api/accounts/models`：有些 harness 能列出
  自己的模型，有些（codex/cursor）由 CLI/SDK 自己解析 —— 那种情况下列表为空、字段仍可手填；
- 两个接口都可以带 `accountId`：目录是**用那个账号的凭据**去读的（有些 provider 不给 key 不回答）；
- 这与 web-cursor 完全同构：它的 `GET /api/providers` + `GET /api/models` 就是这两个问题；
- 目录只是**建议**：池子仍然接受手填的 vendor（provider 的清单会变，不该由我们替它把关）。

## 怎么被选中（`src/agent_account.go`）

```
① agent 行记着 account_id（任务的 account_id，或上一次选择）
       ↓ 找不到 / 被停用 → 拒绝（不换人跑）
② 该 harness 的默认账号（is_default）
       ↓
③ 该 harness 里第一个启用的账号
       ↓
④ 池子里没有 → 拒绝：「no enabled <harness> account in the pool: add one at /accounts」
```

- **不轮换**：同一只 agent 的会话粘住同一个账号；换账号是显式的配置动作（`account_id`），
  不是隐式的负载均衡 —— 否则同一个 task 的相邻 cycle 会突然换一个 key、换一份额度，
  而 agent 的常驻会话并不知道自己换了人。
- **委派也是同一个账号**：capability 要 agent（`AcquireAgent`）时，被委托的那只 worker **继承委托方
  （任务自己的 agent）的账号**，连 harness 一起 —— 任务在 codex 账号上，写代码的 worker 也是 codex
  （见 [delegation.md](delegation.md)）。这样「一个任务一个账号」对整条委派链成立：同一把凭据、
  同一份额度、同一个 workspace root（`<账号 root>/agent-N/`）。账号被删/停用时**委托会失败**，
  不会回落到另一条账号。
- **工作目录也来自账号**：agent 的工作目录是「账号的 root + 它自己的目录名」，即使账号是在第一轮
  才被解析出来的（`ensureLLMSession`）。只有两种例外：账号没填 root（用运行时默认 root），
  或者 capability 显式要了一个目录（`AcquireAgentOpts.Workspace`）。
- **任务级配置**：`POST /api/tasks` 可以带 `account_id`（`AcceptTaskRequest.AccountID`）。
  它在**受理时**校验：id 不存在或账号被停用 = 这条指令被拒（而不是任务跑到第一个 cycle 才死）。
- **三种指定方式**（同一个字段，三个入口）：
  1. **页面**：`http://127.0.0.1:4300/tasks`（壳里的 Tasks 模块）—— 发指令时从**账号下拉**里选，
     第一项 `pool default` 就是「不指定，交给池子」；页面还会先把「这次跑在哪个
     harness/vendor/model/工作目录上」写在按钮旁边（见 [ui.md](ui.md)）；
  2. **API**：`curl -X POST …/api/tasks -d '{"description":"…","account_id":"acct-…"}'`；
  3. **CLI**：`autonomy -account acct-… -description "…"`（env `AUTONOMY_ACCOUNT_ID`）；
     与 `-broadcast` 同用会被拒 —— 广播没有「一条任务」可以指向某个账号。
- **续做不会换账号**：账号记在 agent 行上（`account_id`），所以下一条指令、以及重启后的自动续做
  都还在同一个账号上；要换就显式再指定一次。
- **agent 采纳什么**：harness（= 后端）、model、工作区根、凭据；`account_id` 落库，所以重启后
  同一只 agent 还在同一个账号上。

## 探活（verify）

每种 harness 自己回答「这份凭据能不能用」（`llmbackend.Harness.Probe`）：

- **默认（免费）**：拉起桥并握手 —— cline 报协议/SDK/provider 数量，codex 报 ready；
- **`?live=1`**：用**这个账号的凭据**真跑一轮短对话（会花一点额度），返回模型答复或
  provider 自己的报错；
- **cursor**：现在没有便宜的探法，就**明说**「cannot be probed」，而不是假装成功。

探活用**自己的** client，不碰进程级的那个：报告「这份凭据坏了」不能扰动运行时正在跑的会话。

## 迁移与运维

- **从 env 迁移**：把 key 从 `.env` 挪进账号池（页面或 API），`AUTONOMY_*_API_KEY` /
  `AUTONOMY_*_MODEL` 之后不再被读取。**池子里没有该 harness 的启用账号时，任务会被拒绝** ——
  所以顺序是先加账号、再部署。
- **`AUTONOMY_LLM_BACKEND`** 仍然在（它选的是「默认 harness」，不是凭据）：没有它时默认
  cursor；账号池决定的是「这个 harness 用谁的凭据」。
- **一致性**：两个引擎都实现这个端口（sqlite/postgres），池子的规则（第一个账号自动成默认、
  改默认是移动标记、patch 走同一套校验）在库里，并且被**两引擎一致性测试**覆盖。

## 代码位置

| 文件 | 内容 |
|---|---|
| `src/accounts.go` | 领域类型、vendor/harness 归一化、掩码、校验 |
| `src/store.go` | `AccountStore` 端口（+ `AccountFilter` / `AccountPatch`） |
| `src/db/sqlite_accounts.go` / `postgres_accounts.go` | 两个引擎的实现与建表（`provider_accounts`） |
| `src/accounts_service.go` | API 形状：`AccountView`（掩码）、增删改查、`DefaultAccount`、`VerifyAccount` |
| `src/accounts_page.go` | `GET /accounts` 的页面 |
| `src/agent_account.go` | 解析链 + agent 采纳账号 |
| `src/llmbackend/*/probe.go` | 各 harness 的探活 |
| `src/llmbackend/*/session.go` | 会话用 `Facts().Creds`（不再读 env） |
