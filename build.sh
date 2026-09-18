#!/usr/bin/env bash
#
# 打包脚本 —— 遵循「agent-control-plane-deployment」部署系统规范。
#
# 调用方（二选一，均从仓库根执行）：
#   - 控制面流水线：POST /api/deploy-notify {serviceId:"autonomy"}
#   - 独立发版：部署平台 release.sh autonomy [ref]
#
# 约定：
#   - cwd = 仓库根；环境变量 APP_VERSION = <8 位短 hash>
#   - 必须产出 outputs/，其中必须包含 scripts/restart.sh（平台硬性要求）
#   - VERSION / COMMIT / GIT_REPO_URL 由调用方写入发版包，本脚本不写
#   - 运行期可变内容一律不放进 outputs/（库、.env、pid、日志都在 backend/ 下，
#     由平台在部署时保留，见 scripts/start.sh）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT}"

VERSION="${APP_VERSION:-dev}"
OUT="${ROOT}/outputs"

echo "[build] autonomy version=${VERSION}"
rm -rf "${OUT}"
mkdir -p "${OUT}/bin" "${OUT}/scripts" "${OUT}/src/agent_policy"

BUILD_CACHE="${AUTONOMY_BUILD_CACHE:-${TMPDIR:-/tmp}/autonomy-build-cache}"
export GOMODCACHE="${BUILD_CACHE}/gomodcache"
export GOCACHE="${BUILD_CACHE}/gocache"
export GOPATH="${BUILD_CACHE}/gopath"
export GOTMPDIR="${BUILD_CACHE}/gotmp"
mkdir -p "${GOMODCACHE}" "${GOCACHE}" "${GOPATH}" "${GOTMPDIR}"
[ -n "${GOPROXY:-}" ] || export GOPROXY="https://goproxy.cn,direct"

LDFLAGS="-s -w -X main.version=${VERSION}"

CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" \
  -o "${OUT}/bin/autonomyd" ./cmd/autonomy

cp "${ROOT}/scripts/start.sh" "${ROOT}/scripts/stop.sh" "${ROOT}/scripts/restart.sh" \
  "${OUT}/scripts/"
cp -R "${ROOT}/src/agent_policy/." "${OUT}/src/agent_policy/"

chmod +x "${OUT}/bin/"* "${OUT}/scripts/"*.sh

[ -f "${OUT}/scripts/restart.sh" ] || { echo "[build][错误] outputs/ 缺少 scripts/restart.sh" >&2; exit 1; }

echo "[build] outputs 就绪："
ls -1 "${OUT}" "${OUT}/bin" "${OUT}/scripts" | sed 's/^/  /'
