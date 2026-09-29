# BRANCHING.md — Autonomy 主干开发（Agent 强制）

完整流程、测试与提交边界见 [`project_map/development.md`](project_map/development.md)。  
项目理解入口：[`AGENT.md`](AGENT.md)。

## 工作区与 origin

- 只在 task 简报的 `workspace` 里开发。
- origin 只能是服务中心注入的地址：`https://github.com/kaulie/autonomy`。
- 禁止改 `/Users/gaolei/runtime/**`，禁止在本仓库或 agent 进程里发起部署。

## 分支

基于最新 `origin/main`：

- 功能：`feature/<taskId>`
- 缺陷：`fix/<taskId>` 或 `issue/<taskId>`

禁止在 `main` 上开发或直推 `main`。

## 默认交付

```
commit → git push -u origin HEAD → gh pr create
```

然后按任务 `goal`：

- `merge`：检查可通过后合入 `main`，不合部署。
- `deploy`：合入后再走部署平台 `:4220`。
- 无 `goal`：止于 PR。

任务已有 `prUrl` 时不要重复开 PR。检查失败或冲突先问人。
