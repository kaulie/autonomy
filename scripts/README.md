# scripts

| Script | Purpose |
|--------|---------|
| `start.sh` | 部署平台启动：`SERVICE_PORT` → `127.0.0.1`，探 `/health`，pid/日志在 `backend/`；顺带把两个桥指好（包自带 `bin/cursor-sdk-bridge` 与 `src/clinesdk/bridge`，后者的依赖按 tarball 的 sha256 解到 `${AUTONOMY_CACHE_DIR:-$RUNTIME_DIR/.cache}/cline-bridge/` 并 symlink 回源码目录），并在拉起进程前**自检所选后端的桥**（不过就非 0 退出 → 部署失败、留在上一版；见 docs/llm-backend.md） |
| `stop.sh` | TERM → 等待 → KILL |
| `restart.sh` | 控制面默认 `restartCmd`（`stop` 然后 `start`） |
| `fetch-bridge.sh` | Download the pinned `cursor-sdk-bridge` standalone binary into `third_party/` (Cursor backend). |
| `install-cline-bridge.sh` | `npm install` the Cline bridge's dependencies (`@cline/sdk`) under `src/clinesdk/bridge/` (Cline backend) — 开发机上跑一次；打包时 `build.sh` 自己会取（优先 checkout 的 `node_modules`，其次构建机缓存 `~/.cache/autonomy/cline-bridge-deps/<lock 的 sha256>.tgz`，最后 `npm ci --omit=dev`），取不到就**构建失败** |
