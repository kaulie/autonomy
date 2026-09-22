# scripts

| Script | Purpose |
|--------|---------|
| `start.sh` | 部署平台启动：`SERVICE_PORT` → `127.0.0.1`，探 `/health`，pid/日志在 `backend/`；顺带把两个桥指好（包自带 `bin/cursor-sdk-bridge` 与 `src/clinesdk/bridge`，后者的依赖按 tarball 的 sha256 解到 `${AUTONOMY_CACHE_DIR:-$RUNTIME_DIR/.cache}/cline-bridge/` 并 symlink 回源码目录） |
| `stop.sh` | TERM → 等待 → KILL |
| `restart.sh` | 控制面默认 `restartCmd`（`stop` 然后 `start`） |
| `fetch-bridge.sh` | Download the pinned `cursor-sdk-bridge` standalone binary into `third_party/` (Cursor backend). |
| `install-cline-bridge.sh` | `npm install` the Cline bridge's dependencies (`@cline/sdk`) under `src/clinesdk/bridge/` (Cline backend) — 开发机上跑一次；打包时 `build.sh` 自己会装（`npm ci --omit=dev`）并把依赖压进发版包 |
