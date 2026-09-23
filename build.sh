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

# Where Go's module and build caches live. Deliberately *not* $TMPDIR: macOS cleans temp
# dirs, and a build killed mid-extraction leaves a half-unpacked toolchain or module cache
# behind — after which every later build fails with a wall of "package X is not in std" /
# "no required module provides package Y" that says nothing about the cache. A durable
# directory survives both; if it ever does go bad, deleting it is the whole repair
# (scripts/README.md says so; the cost is one full re-download and recompile).
# AUTONOMY_BUILD_CACHE overrides the location.
BUILD_CACHE="${AUTONOMY_BUILD_CACHE:-${HOME:-/tmp}/.cache/autonomy/build}"
# Move a cache left in the old temp location over, once: a rename, so the first build
# after this change does not re-download the toolchain and every module.
LEGACY_BUILD_CACHE="${TMPDIR:-/tmp}/autonomy-build-cache"
if [ ! -e "${BUILD_CACHE}" ] && [ -d "${LEGACY_BUILD_CACHE}" ] && [ "${LEGACY_BUILD_CACHE}" != "${BUILD_CACHE}" ]; then
  mkdir -p "$(dirname "${BUILD_CACHE}")"
  if mv "${LEGACY_BUILD_CACHE}" "${BUILD_CACHE}" 2>/dev/null; then
    echo "[build] 构建缓存从 ${LEGACY_BUILD_CACHE} 搬到 ${BUILD_CACHE}（原位置在 TMPDIR，会被系统清理）"
  fi
fi
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

# cursor bridge: the runtime spawns it (src/cursorsdk), so the release package carries it
# — scripts/start.sh then points CURSOR_SDK_BRIDGE_BIN at ${RUNTIME_DIR}/bin/cursor-sdk-bridge.
# The binary is a pinned download (gitignored), so a build without it fetches it: from the
# build-machine cache first — keyed by the pinned version, so a bump cannot pick up a stale
# binary — then by running scripts/fetch-bridge.sh. Failing both is a warning, not an
# error: which bridge a deployment needs is decided by its *selected* backend, and
# scripts/start.sh's pre-start self-check refuses a deploy whose backend has no bridge —
# loudly, at deploy time, instead of every task failing at run time.
BRIDGE="${ROOT}/third_party/bin/cursor-sdk-bridge"
if [ ! -x "${BRIDGE}" ]; then
  CURSOR_BRIDGE_VERSION="$(bash "${ROOT}/scripts/fetch-bridge.sh" --print-version 2>/dev/null || echo unknown)"
  BRIDGE_CACHE="${AUTONOMY_CACHE_DIR:-${HOME}/.cache/autonomy}/cursor-sdk-bridge/${CURSOR_BRIDGE_VERSION}"
  if [ ! -x "${BRIDGE_CACHE}/bin/cursor-sdk-bridge" ] && [ "${AUTONOMY_SKIP_FETCH_CURSOR_BRIDGE:-0}" != "1" ]; then
    echo "[build] checkout 里没有 cursor bridge，取 v${CURSOR_BRIDGE_VERSION} 到构建机缓存"
    bash "${ROOT}/scripts/fetch-bridge.sh" --dest "${BRIDGE_CACHE}" || true
  fi
  if [ -x "${BRIDGE_CACHE}/bin/cursor-sdk-bridge" ]; then
    mkdir -p "${ROOT}/third_party/bin"
    # Also leave it where a dev run and the tests look for it (third_party/bin), which is
    # what src/cursorsdk's default bridge path resolves to.
    cp "${BRIDGE_CACHE}/bin/cursor-sdk-bridge" "${BRIDGE}"
  fi
fi
if [ -x "${BRIDGE}" ]; then
  cp "${BRIDGE}" "${OUT}/bin/cursor-sdk-bridge"
  echo "[build] cursor bridge 随包发出：$(du -h "${OUT}/bin/cursor-sdk-bridge" | cut -f1)"
else
  echo "[build][警告] 没有 ${BRIDGE}（scripts/fetch-bridge.sh 也没取到）；发版包不自带 cursor bridge，" >&2
  echo "[build][警告] 用 cursor 后端部署时 scripts/start.sh 的启动自检会拒绝启动（用 cline 后端不受影响）。" >&2
fi

# —— Node 桥（cline / codex）随包发出 ——
# 每个 harness 的桥都是「一个 Node 脚本 + 一棵依赖树」，打包方式完全一样，所以这里是一个
# 函数：源码（运行时那几个模块）+ package.json/lock + 一份生产安装的依赖压缩包
# bridge-deps.tgz。scripts/start.sh 把它解到包外的缓存（${RUNTIME_DIR}/.cache/<name>-bridge/<sha256>，
# 部署 rsync 不碰它）、在源码目录建 node_modules 符号链接，并把 AUTONOMY_<NAME>_BRIDGE_SCRIPT
# 指到包里的桥 —— 这样部署跑的桥来自*这个发版包*，而不是机器上的某份 checkout。
#
# 依赖有三个来源，按序，绝不静默产出缺桥的包：
#   1. checkout 的 node_modules（跑过 scripts/install-<name>-bridge.sh 的开发机），
#   2. 构建机缓存，按 lockfile 的 sha256 做 key —— 命中即不需要 npm 也不需要网络，
#   3. `npm ci --omit=dev`（需要 npm + 网络；AUTONOMY_NPM_BIN 指定 npm）。
# 三者都不成 = 构建失败（不是警告）：缺桥的包在部署时会被 scripts/start.sh 的启动前自检拒
# 掉，不如在这里说清。显式接受缺桥：AUTONOMY_ALLOW_MISSING_<NAME>_BRIDGE=1。
package_node_bridge() { # <name> <package-relative dir> <module>...
  local name="$1" pkgdir="$2"
  shift 2
  local modules=("$@")
  local src="${ROOT}/${pkgdir}"
  [ -f "${src}/bridge.mjs" ] || return 0
  mkdir -p "${OUT}/${pkgdir}"
  local m
  for m in "${modules[@]}"; do
    [ -f "${src}/${m}" ] && cp "${src}/${m}" "${OUT}/${pkgdir}/"
  done
  cp "${src}/package.json" "${OUT}/${pkgdir}/"
  [ -f "${src}/package-lock.json" ] && cp "${src}/package-lock.json" "${OUT}/${pkgdir}/"

  local tgz_out="${OUT}/${pkgdir}/bridge-deps.tgz"
  local cache="${AUTONOMY_CACHE_DIR:-${HOME}/.cache/autonomy}/${name}-bridge-deps"
  local lock_sha=""
  if [ -f "${src}/package-lock.json" ]; then
    if command -v shasum >/dev/null 2>&1; then
      lock_sha="$(shasum -a 256 "${src}/package-lock.json" | cut -d' ' -f1)"
    else
      lock_sha="$(sha256sum "${src}/package-lock.json" | cut -d' ' -f1)"
    fi
  fi
  local cached_tgz="${cache}/${lock_sha:-nolock}.tgz"
  if [ -d "${src}/node_modules" ] && tar czf "${tgz_out}" -C "${src}" node_modules; then
    mkdir -p "${cache}"
    cp -f "${tgz_out}" "${cached_tgz}" 2>/dev/null || true
  elif [ -f "${cached_tgz}" ] && cp -f "${cached_tgz}" "${tgz_out}"; then
    echo "[build] 复用构建机缓存的 ${name} bridge 依赖：${cached_tgz}"
  else
    local npm_bin="${AUTONOMY_NPM_BIN:-npm}"
    echo "[build] 安装 ${name} bridge 依赖（${npm_bin} ci --omit=dev；装好后进构建机缓存）"
    if (cd "${src}" && "${npm_bin}" ci --omit=dev --no-audit --no-fund) && tar czf "${tgz_out}" -C "${src}" node_modules; then
      mkdir -p "${cache}"
      cp -f "${tgz_out}" "${cached_tgz}" 2>/dev/null || true
    fi
  fi
  local allow_var="AUTONOMY_ALLOW_MISSING_$(printf '%s' "${name}" | tr '[:lower:]' '[:upper:]')_BRIDGE"
  if [ -f "${tgz_out}" ]; then
    echo "[build] ${name} bridge 随包发出：$(du -h "${tgz_out}" | cut -f1) 依赖包 + $(ls "${OUT}/${pkgdir}"/*.mjs | wc -l | tr -d ' ') 个脚本"
  elif [ "${!allow_var:-0}" = "1" ]; then
    echo "[build][警告] 发版包不带 ${name} bridge 依赖（${allow_var}=1 显式接受）；部署上的 ${name} 后端要自己指一个装好的桥。" >&2
  else
    echo "[build][错误] 拿不到 ${name} bridge 依赖（${src}/node_modules、${cached_tgz}、${npm_bin:-npm} ci 都不成）：" >&2
    echo "[build][错误] 发版包会缺桥，用 ${name} 后端部署时 scripts/start.sh 的自检会判这次部署失败。" >&2
    echo "[build][错误] 解决：在构建机上跑一次 scripts/install-${name}-bridge.sh（或用 AUTONOMY_NPM_BIN 指定 npm）；或显式接受缺桥：${allow_var}=1。" >&2
    exit 1
  fi
}

package_node_bridge cline src/clinesdk/bridge bridge.mjs config.mjs resume.mjs trace.mjs
package_node_bridge codex src/codexsdk/bridge bridge.mjs config.mjs

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
