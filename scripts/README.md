# scripts

| Script | Purpose |
|--------|---------|
| `start.sh` | 部署平台启动：`SERVICE_PORT` → `127.0.0.1`，探 `/health`，pid/日志在 `backend/`；顺带把两个桥指好（包自带 `bin/cursor-sdk-bridge` 与 `src/clinesdk/bridge`，后者的依赖按 tarball 的 sha256 解到 `${AUTONOMY_CACHE_DIR:-$RUNTIME_DIR/.cache}/cline-bridge/` 并 symlink 回源码目录），并在拉起进程前**自检所选后端的桥**（不过就非 0 退出 → 部署失败、留在上一版；见 docs/llm-backend.md） |
| `stop.sh` | TERM → 等待 → KILL |
| `restart.sh` | 控制面默认 `restartCmd`（`stop` 然后 `start`） |
| `fetch-bridge.sh` | Download the pinned `cursor-sdk-bridge` standalone binary (Cursor backend)。`--print-version` 打印 pin 的版本（**版本只 pin 在它这一处**，`build.sh` 问它而不是自己知道），`--dest DIR` 下到别处（build.sh 用它填构建机缓存） |
| `install-cline-bridge.sh` | `npm install` the Cline bridge's dependencies (`@cline/sdk`) under `src/clinesdk/bridge/` (Cline backend) — 开发机上跑一次；打包时 `build.sh` 自己会取（优先 checkout 的 `node_modules`，其次构建机缓存 `~/.cache/autonomy/cline-bridge-deps/<lock 的 sha256>.tgz`，最后 `npm ci --omit=dev`），取不到就**构建失败** |

## 构建缓存（`build.sh`）

`build.sh` 把 Go 的 module/build 缓存放在 **`${AUTONOMY_BUILD_CACHE:-~/.cache/autonomy/build}`**（`gomodcache` /
`gocache` / `gopath` / `gotmp` 四个子目录）——**不在 `$TMPDIR` 里**：macOS 会清理临时目录，而且**一次被中途杀掉的
构建**可能留下只解压了一半的工具链或模块缓存，之后**每一次**构建都以一堆
`package X is not in std` / `no required module provides package Y` 失败，看上去完全不像缓存问题
（线上真的这么失败过一次）。放到 durable 目录后这两种情况都不会再发生。

- 从旧位置（`$TMPDIR/autonomy-build-cache`）**自动搬一次**过来（rename，不触发重下）；`AUTONOMY_BUILD_CACHE` 可指定别处。
- 万一缓存真的坏了：**删掉这个目录**就是全部修复（代价是一次完整的重新下载 + 重新编译）。
- 里面还有两个按内容去重的缓存：`cline-bridge-deps/<lock 的 sha256>.tgz`（发版包的 Cline 桥依赖）与
  `cursor-sdk-bridge/<版本>/`（pin 的 cursor 桥二进制）。
