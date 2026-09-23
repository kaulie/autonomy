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

掩码不是「小心返回」：领域类型 `autonomy.Account` 的 key 是 `json:"-"`，**从类型上就渲染不出来**。

页面：**`GET /accounts`** —— 与 dashboard 同款自包含页（0 构建），列表/新增/编辑/启停/设默认/
verify/删除。它写 key 一次、永不读回，与 API 同一条规矩。agent 状态页（`/dashboard`）多一列
`Account`，写明每只 agent 花的是谁的额度。

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
- **任务级配置**：`POST /api/tasks` 可以带 `account_id`（`AcceptTaskRequest.AccountID`）。
  它在**受理时**校验：id 不存在或账号被停用 = 这条指令被拒（而不是任务跑到第一个 cycle 才死）。
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
