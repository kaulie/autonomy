# 开发流程与分支规范

根目录 [`BRANCHING.md`](../BRANCHING.md) 是本页的短规约。细节以本页为准。

## 工作区

只在 task 简报给出的 `workspace` 里开发（本任务是 `/Users/gaolei/agent-workspace-gaolei0811/agent-65dd08b161e547ea`）。clone 进该目录或其子目录。

- origin **只能**是服务中心注入的仓库地址。本仓库：`https://github.com/kaulie/autonomy`。
- 不要改其它 task 的目录。
- **永远不要**改 `/Users/gaolei/runtime/**`。
- 不要把 `/Users/gaolei/Projects/deepseek_web_cursor` 当开发目录，除非用户明确要求。

## 分支（主干开发）

基于最新 `origin/main` 开短命分支，名称必须带 task id：

| 类型 | 分支名 |
|---|---|
| 功能 | `feature/<taskId>` |
| 缺陷 | `fix/<taskId>` 或 `issue/<taskId>` |

例：`feature/task-158e4356688b4d4d`。

仓库历史上也有 `feat/…`、`docs/…`、`chore/…`。新工作按上表，不要再发明不含 task id 的分支。

禁止：在 `main` 上直接开发或提交；`git push origin main`；force push `main`。

```bash
git fetch origin
git checkout main
git pull --ff-only origin main
git checkout -b feature/<taskId>
# …改、测、commit…
git push -u origin HEAD
gh pr create --base main --head "$(git branch --show-current)" …
```

任务简报已有 `prUrl` 时不要再开重复 PR。

合入 / 部署看任务的 `goal`：

- `merge`：检查绿了由执行该任务的 agent 合入 `main`，**到此停止，不部署**。
- `deploy`：合入后再走部署平台（不是本仓库脚本）。
- 没有 `goal`：默认只开 PR，不合入、不部署。
- 检查红、冲突、真实不确定：先问人。

## 改代码时的边界

1. **先读 [`../AGENT.md`](../AGENT.md) 与本目录**，再读 `docs/` 里相关概念文。
2. 提示词只改 `src/agent_policy/`，不要把大段政策写回 Go。
3. 新能力：实现放 `src/capability/<domain>/`，在 `RegisterDefaults` 登记，并给 `spec.Declared` 的 input/output。
4. 新 harness：`src/llmbackend/<name>` + `src/llmbackend/all` 一行 blank import。
5. 新 HTTP 路由：写进 `HTTPServer.routes()`，注解跟 handler，跑 `src/contract_test.go`。
6. 存储：上层只走端口；SQL / driver 只进 `src/db`。
7. 不要在本仓库加「发起部署」的接口或脚本。

## 测试

```bash
go test ./...
```

放置规则（`docs/testing.md`，三条 Go 边界）：

- 同包测试（碰未导出符号）必须留在 `src/`（或该子包目录）。
- 黑盒测试也必须在被测包目录；`src/tests/<模块>/` 只能看见导出面。
- 根包测试用 `<模块>_<主题>_test.go`；夹具只走 `src/fixtures_test.go`。
- 真打模型：`*_live_test.go` + 环境门，默认跳过，不要让常规测试烧额度。

改 UI / 路由 / store 端口后至少跑相关包，不要只靠「看起来能编过」。

本仓库当前检出**没有** `.github/workflows`。不要把「GitHub 上一定有 CI」写进结论；合入前至少本地 `go test ./...`。

## 提交与敏感物

小步、说清为什么。不要提交：

- `backend/.env`、任何 API key、GitHub App 私钥
- `third_party/`（cursor 桥下载物）
- `src/*/bridge/node_modules/`
- `outputs/`、`docs/swagger.json`
- `~/database/autonomy/*.db`

`gitRepos` / 账号池的真实 key 只存在运行库里，不进 git。

## 本地怎么跑

```bash
export PROJECT_ROOT=/absolute/path/to/autonomy
export AUTONOMY_REASONER=llm
# 不要占用 4300（线上契约端口）除非你清楚线上已停
go test ./...
go run ./cmd/autonomyd
# 另一个终端：
go run ./cmd/autonomy -description "…"
```

需要 Cursor 桥：`./scripts/fetch-bridge.sh`。需要 Cline：`./scripts/install-cline-bridge.sh` 且 runtime 设 `AUTONOMY_LLM_BACKEND=cline`。账号在 `http://127.0.0.1:<port>/accounts` 先加好。

`GET /health` 报的是 **runtime 进程**的 backend，不是你给 CLI 导出的变量。

## 文档同步

改了行为就改 `project_map/`（本目录）和仍有效的 `docs/`。发现 `docs/` 或本目录与代码不符：按 [`AGENT.md`](../AGENT.md) 更新或提出异议，不要默默以文档为准继续做。
