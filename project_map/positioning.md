# 项目定位

## 一句话

Autonomy 是一个**目标驱动的 Agent Runtime**：人（或上游系统）给出要达成的目标与完成判据，runtime 持有世界、能力与验证，让 agent 自己选路径。它不是 Workflow Engine，也不替代部署平台或控制面。

仓库：[`https://github.com/kaulie/autonomy`](https://github.com/kaulie/autonomy)  
组织：服务中心部门 `D0005`（AI研发部）  
服务名：`autonomy`  
契约端口：`4300`

## 它要解决什么

多数 agent 产品把路径写成「Task → 固定 Workflow → Skill A/B/C」。结果不确定时人必须介入。Autonomy 把主轴换成：

```
Task → Agent → Capability → Action → 世界状态变化 → Verification → Done / Re-plan
```

因此本项目的产品主张是：

- **目标优先于流程**：流程是决策的产物，不是仓库里的主制品。
- **世界状态优先于自称成功**：能力返回成功 ≠ 完成契约满足。
- **能力可组合**：规划面对的是语义能力表（`{{CONSTRUCTS}}`），不是硬编码步骤链。
- **Runtime 可靠、Agent 灵活**：排队、超时、会话、持久化、重启自愈在 runtime；「下一步做什么」在 agent。

## 它不是什么

| 不是 | 谁才是 |
|---|---|
| Web Cursor / 任务面板产品 | [`agent-control-plane`](https://github.com/kaulie/agent-control-plane)（本机默认 `:4211`） |
| 部署 / 发版平台 | [`agent-control-plane-deployment`](https://github.com/kaulie/agent-control-plane-deployment)（本机 `:4220`） |
| 服务目录 / 契约中心 | 服务中心（本机默认 `:4240`） |
| 组织 / 部门目录 | organization 服务（本机默认 `:4244`） |
| 评测套件 | [`agent-benchmark-tool`](https://github.com/kaulie/agent-benchmark-tool)（读本服务的数据 API，不直读库） |
| 通用 LLM 网关 | 它只驱动已注册的 harness（Cursor / Cline / Codex / Claude） |

本仓库**没有**「对本机 runtime 发起部署」的入口。`service.deploy` 能力只是去打部署平台的 `POST /api/deploy-notify`，包装和重启仍由平台完成。

## 两个入口，不要混

| 入口 | 角色 |
|---|---|
| `cmd/autonomyd` | **Runtime 进程**：持有 store、agent、world，对外提供 HTTP。部署平台启动的就是它。 |
| `cmd/autonomy` | **HTTP 客户端**：对 `http://127.0.0.1:4300` 投递指令 / 广播 / 查进展。给它设 `AUTONOMY_LLM_BACKEND` 不会改变 runtime。 |
| `cmd/hello` | 进程内 hello 演示（假服务 health check + `StateVerifier`）。它证明「能力成功不够、世界状态才算」，不是线上形态。 |

## 和「Project」这个词

在 ontology 里，Project 是一种 Context，不是 Task 的父生命周期（见 `docs/project.md`）。  
在平台里，本仓库对应控制面项目 **autonomy**（task 简报里的 `project-749a0238`），所属组织由控制面指向部门，服务列表来自服务中心，不在本进程里再登记一份。

## 当前形态（不要再用「只有 hello」来理解）

已落地、且后续开发必须当现状的：

- 常驻 agent + 每 agent 一条 inbox（用户 / 其它 agent / runtime 停指令）
- 多 harness 账号池（凭据只从池子来）
- planner / worker 共用一套 LLM session
- 内置能力：`code_edit`、`service.deploy`、`pull_request.review`、`pr.check`、`deployment.monitor`、资产变更
- 自包含 UI：`/`、`/tasks`、`/dashboard`、`/accounts`
- 发版包装 + 优雅重启对接部署平台
- sqlite（默认，全机一份库）与 postgres 两套 engine

实验性仍成立：没有锁死为单 agent 或固定 workflow；信任选择（`docs/trust.md`）等仍可后置。但「早期骨架」已经不能描述这个仓库。
