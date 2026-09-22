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
#   - cursor bridge（third_party/bin/cursor-sdk-bridge，gitignore 的下载产物）随包
#     发出：runtime 只按 cwd 找 bridge，而部署的 cwd 是 runtimeDir，包外它找不到。
#     没抓到就给个警告（部署上的 llm 任务会在 cursor bridge ping 处失败）。
#   - 末尾登记服务契约：注解（cmd/autonomyd/main.go + src/http_server.go）是唯一真源，
#     由 scripts/register-contract.sh 读代码 → swag → 上报服务中心（幂等）。
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
  -o "${OUT}/bin/autonomyd" ./cmd/autonomyd

cp "${ROOT}/scripts/start.sh" "${ROOT}/scripts/stop.sh" "${ROOT}/scripts/restart.sh" \
  "${OUT}/scripts/"
cp -R "${ROOT}/src/agent_policy/." "${OUT}/src/agent_policy/"

# cursor bridge: the runtime spawns it (src/cursorsdk), so the release package
# carries it — scripts/start.sh then points CURSOR_SDK_BRIDGE_BIN at
# ${RUNTIME_DIR}/bin/cursor-sdk-bridge. Without it a deployed runtime fails every
# task at the decision that pings the bridge.
BRIDGE="${ROOT}/third_party/bin/cursor-sdk-bridge"
if [ -x "${BRIDGE}" ]; then
  cp "${BRIDGE}" "${OUT}/bin/cursor-sdk-bridge"
else
  echo "[build][警告] 没有 ${BRIDGE}（先跑 scripts/fetch-bridge.sh）；发版包不自带 cursor bridge，" >&2
  echo "[build][警告] 部署上的 llm 任务会失败在 cursor bridge ping 处。" >&2
fi

# cline bridge: same idea, but it is a Node script with a dependency tree
# (@cline/sdk), so the package carries the sources plus a tarball of a production
# install — scripts/start.sh extracts it into a cache *outside* the packaged tree
# (${RUNTIME_DIR}/.cache/cline-bridge/<sha256>, which the deploy rsync does not
# touch), symlinks the sources' node_modules into it and points
# AUTONOMY_CLINE_BRIDGE_SCRIPT at the packaged bridge, which is what makes a
# deployed runtime's cline backend come from *this release* instead of some
# checkout on the machine. The tarball still rides in the package (node_modules is
# 251MB unpacked / ~37MB packed) so a cold machine starts offline, with neither
# npm nor the network.
CLINE_BRIDGE="${ROOT}/src/clinesdk/bridge"
if [ -f "${CLINE_BRIDGE}/bridge.mjs" ]; then
  mkdir -p "${OUT}/src/clinesdk/bridge"
  # 只带运行时那四个模块（tests/smoke 是开发用的，不进部署包）。
  for m in bridge.mjs config.mjs resume.mjs trace.mjs; do
    [ -f "${CLINE_BRIDGE}/${m}" ] && cp "${CLINE_BRIDGE}/${m}" "${OUT}/src/clinesdk/bridge/"
  done
  cp "${CLINE_BRIDGE}/package.json" "${OUT}/src/clinesdk/bridge/"
  [ -f "${CLINE_BRIDGE}/package-lock.json" ] && cp "${CLINE_BRIDGE}/package-lock.json" "${OUT}/src/clinesdk/bridge/"
  # A production install of the bridge's dependencies. Installed here (build time,
  # where npm and the network are) and shipped packed, so a deploy needs neither.
  if [ ! -d "${CLINE_BRIDGE}/node_modules" ]; then
    echo "[build] 安装 cline bridge 依赖（npm ci --omit=dev，首次或 node_modules 被清了）"
    (cd "${CLINE_BRIDGE}" && npm ci --omit=dev --no-audit --no-fund) || {
      echo "[build][警告] cline bridge 依赖安装失败；发版包不带 cline bridge，部署上的 cline 后端需要" >&2
      echo "[build][警告] 自己指向一个装好 @cline/sdk 的桥（AUTONOMY_CLINE_BRIDGE_SCRIPT）。" >&2
    }
  fi
  if [ -d "${CLINE_BRIDGE}/node_modules" ]; then
    tar czf "${OUT}/src/clinesdk/bridge/bridge-deps.tgz" -C "${CLINE_BRIDGE}" node_modules
    echo "[build] cline bridge 随包发出：$(du -h "${OUT}/src/clinesdk/bridge/bridge-deps.tgz" | cut -f1) 依赖包 + $(ls "${OUT}/src/clinesdk/bridge"/*.mjs | wc -l | tr -d ' ') 个脚本"
  else
    echo "[build][警告] 没有 ${CLINE_BRIDGE}/node_modules：发版包不带 cline bridge 依赖，" >&2
    echo "[build][警告] 部署上的 cline 后端要自己指一个装好 @cline/sdk 的桥。" >&2
  fi
fi

chmod +x "${OUT}/bin/"* "${OUT}/scripts/"*.sh

[ -f "${OUT}/scripts/restart.sh" ] || { echo "[build][错误] outputs/ 缺少 scripts/restart.sh" >&2; exit 1; }

# ---- 契约自动登记（注解是唯一真源）----
# 读 cmd/autonomyd/main.go（General API Info）+ src/http_server.go（每个 handler 的注解）
# → swag 生成 OpenAPI → 上报服务中心（幂等：契约没变化不写库、不刷 revision）。
# 发版流程本来就跑在本机，而服务中心只绑 127.0.0.1:4240，所以这里是天然合适的触发点。
# 默认「尽力而为」：服务中心不可达只告警，不阻塞发版（REGISTER_CONTRACT_STRICT=1 改成硬失败，
# SKIP_REGISTER_CONTRACT=1 跳过；两种情况下都可以事后用 scripts/register-contract.sh 补登记）。
if [ "${SKIP_REGISTER_CONTRACT:-0}" = "1" ]; then
  echo "[build] 跳过契约登记（SKIP_REGISTER_CONTRACT=1）"
else
  echo "[build] 登记服务契约到服务中心（注解 → OpenAPI）"
  if ! VERSION="${VERSION}" bash "${ROOT}/scripts/register-contract.sh"; then
    if [ "${REGISTER_CONTRACT_STRICT:-0}" = "1" ]; then
      echo "[build][错误] 契约登记失败（REGISTER_CONTRACT_STRICT=1）" >&2
      exit 1
    fi
    echo "[build][警告] 契约登记失败（服务中心可能不可达）；发版继续，可事后补跑 scripts/register-contract.sh" >&2
  fi
fi

echo "[build] outputs 就绪："
ls -1 "${OUT}" "${OUT}/bin" "${OUT}/scripts" | sed 's/^/  /'
