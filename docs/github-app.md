# 每个账号自己的 GitHub 权限

整机 `gh auth login` 是救急：所有 worker 共用 `kaulie` 能写的一切。要拆开，用 **GitHub App** 按账号发卡。

LLM key 仍在账号池里。Git 是另一列：`gitRepos`（这个账号能推哪些 `owner/name`）和可选的 `gitToken`（fine-grained PAT）。没配 App、也没 PAT 时，行为与现在一样，继续用主机上的 `gh`。

## 你需要做的（网页上，我做不了）

1. 打开 [Register a GitHub App](https://github.com/settings/apps/new)（个人）或 org 的 Developer settings。
2. **GitHub App name**：`autonomy-worker`（被占用就换一个）。
3. **Homepage URL**：`https://github.com/kaulie/autonomy`
4. **Webhook**：关掉 Active。
5. **Repository permissions**：
   - Contents: Read and write
   - Pull requests: Read and write
   - Metadata: Read-only
6. **Where can this GitHub App be installed?**：Only on this account。
7. Create → **Generate a private key**（会下载一个 `.pem`）。
8. Install App → 只勾要让 agent 写的仓库（至少 `kaulie/autonomy`）。

做完后把这三样给我（私钥不要贴到聊天里，放到机器上即可）：

| 项 | 在哪看 |
|---|---|
| **App ID** | App 设置页顶部的数字 |
| **Installation ID** | 安装后浏览器地址：`https://github.com/settings/installations/<这一串数字>` |
| **私钥文件** | 下载的 `*.pem`，放到本机和海外机，例如 `/home/ubuntu/runtime/autonomy/backend/github-app.pem`（权限 `600`） |

## 配到 autonomyd

```
GITHUB_APP_ID=…
GITHUB_APP_INSTALLATION_ID=…
GITHUB_APP_PRIVATE_KEY_PATH=/home/ubuntu/runtime/autonomy/backend/github-app.pem
```

账号上填 `gitRepos`：`kaulie/autonomy`。worker 开跑时会在该账号工作区根下写 `.autonomy/github-token`（0600），`git` / `gh` 从 cwd 往上找这份，不再用整机那把万能钥匙。

`pull_request.review` 在 App 配好后也走安装 token，按当前仓库收窄。
