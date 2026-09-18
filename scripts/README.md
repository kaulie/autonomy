# scripts

| Script | Purpose |
|--------|---------|
| `start.sh` | 部署平台启动：`SERVICE_PORT` → `127.0.0.1`，探 `/health`，pid/日志在 `backend/` |
| `stop.sh` | TERM → 等待 → KILL |
| `restart.sh` | 控制面默认 `restartCmd`（`stop` 然后 `start`） |
| `fetch-bridge.sh` | Download the pinned `cursor-sdk-bridge` standalone binary into `third_party/` (Cursor backend). |
| `install-cline-bridge.sh` | `npm install` the Cline bridge's dependencies (`@cline/sdk`) under `src/clinesdk/bridge/` (Cline backend). |
