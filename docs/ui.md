# UI 主界面（shell）与模块

**入口是 `GET /`**：一个自包含的壳页面，左边是模块导航，右边用 `?embed=1` 嵌入选中的模块。

## 为什么要壳，不是把每个页面做大

runtime/agent/task/账号这些页面都在解一个具体问题，谁也不需要知道别人存在。壳只做三件事，
所以它不需要任何数据端口（`NewHTTPServer(nil)` 就能服务）：

1. **导航**：模块列在左边，选中项高亮；
2. **标题**：壳拥有页面标题，模块带 `?embed=1` 时**隐藏自己的标题**（`body.embedded h1 { … }`），
   避免同一行字印两遍；
3. **状态**：顶部一行轮询 `GET /health`，写明「哪个后端 · 哪个模型 · 池子里的哪个账号 · 多少 turn」，
   账号为空时直接提示去 Harness accounts 加一个 —— 「现在能不能跑任务」不用点进去才看得到。

路由用 hash 记（`/#accounts`），所以：

- 每个模块**仍然可以单独打开**（`/dashboard`、`/accounts`，老链接不会失效，也方便单独截图/调试）；
- 壳里的「open in a tab」把当前模块另开一个标签页。

## 现有模块

| 模块 | 路径 | 文档 |
|---|---|---|
| Agent status | `GET /dashboard`（数据 `GET /api/agents`） | [dashboard.md](dashboard.md) |
| Harness accounts | `GET /accounts`（数据 `/api/accounts`） | [accounts.md](accounts.md) |

## 加一个模块

1. 写一个**自包含**页面（一个 HTML 常量 + 一个 handler，像 `src/accounts_page.go` 那样），
   路径自己定，并在它的 CSS/JS 里加一行 `embed` 处理（见上面的第 2 条）；
2. 在 `src/ui_home_page.go` 的 `MODULES` 表里加一行、在 `<nav>` 里加一个按钮；
3. 注解带上 `@Router`（契约是注解生成的，`src/contract_test.go` 会盯着路由表与注解一一对应）。

没有构建步骤、没有打包器、没有外部资源 —— 这就是这套 UI 的全部约束。
