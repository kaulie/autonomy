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
  # A production install of the bridge's dependencies, shipped packed. Three sources,
  # in order, so a build never silently produces a package without the bridge:
  #   1. the checkout's own node_modules (a dev machine that ran install-cline-bridge.sh),
  #   2. this machine's build cache, keyed by the lockfile's sha256 — a cache hit means
  #      builds keep working with neither npm nor the network (and it is fed by every
  #      build that had the tree), 
  #   3. `npm ci --omit=dev` (needs npm + the network; AUTONOMY_NPM_BIN overrides npm).
  # Failing all three is a build error, not a warning: a package without the bridge
  # fails scripts/start.sh's pre-start self-check at deploy time anyway — better to say
  # so here. AUTONOMY_ALLOW_MISSING_CLINE_BRIDGE=1 accepts the gap (e.g. a deployment
  # whose .env points AUTONOMY_CLINE_BRIDGE_SCRIPT at an external checkout).
  DEPS_TGZ_OUT="${OUT}/src/clinesdk/bridge/bridge-deps.tgz"
  DEPS_CACHE="${AUTONOMY_CACHE_DIR:-${HOME}/.cache/autonomy}/cline-bridge-deps"
  lock_sha=""
  if [ -f "${CLINE_BRIDGE}/package-lock.json" ]; then
    if command -v shasum >/dev/null 2>&1; then
      lock_sha="$(shasum -a 256 "${CLINE_BRIDGE}/package-lock.json" | cut -d' ' -f1)"
    else
      lock_sha="$(sha256sum "${CLINE_BRIDGE}/package-lock.json" | cut -d' ' -f1)"
    fi
  fi
  cached_tgz="${DEPS_CACHE}/${lock_sha:-nolock}.tgz"
  if [ -d "${CLINE_BRIDGE}/node_modules" ] &&
    tar czf "${DEPS_TGZ_OUT}" -C "${CLINE_BRIDGE}" node_modules; then
    mkdir -p "${DEPS_CACHE}"
    cp -f "${DEPS_TGZ_OUT}" "${cached_tgz}" 2>/dev/null || true
  elif [ -f "${cached_tgz}" ] && cp -f "${cached_tgz}" "${DEPS_TGZ_OUT}"; then
    echo "[build] 复用构建机缓存的 cline bridge 依赖：${cached_tgz}"
  else
    NPM_BIN="${AUTONOMY_NPM_BIN:-npm}"
    echo "[build] 安装 cline bridge 依赖（${NPM_BIN} ci --omit=dev；装好后进构建机缓存）"
    if (cd "${CLINE_BRIDGE}" && "${NPM_BIN}" ci --omit=dev --no-audit --no-fund) &&
      tar czf "${DEPS_TGZ_OUT}" -C "${CLINE_BRIDGE}" node_modules; then
      mkdir -p "${DEPS_CACHE}"
      cp -f "${DEPS_TGZ_OUT}" "${cached_tgz}" 2>/dev/null || true
    fi
  fi
  if [ -f "${DEPS_TGZ_OUT}" ]; then
    echo "[build] cline bridge 随包发出：$(du -h "${DEPS_TGZ_OUT}" | cut -f1) 依赖包 + $(ls "${OUT}/src/clinesdk/bridge"/*.mjs | wc -l | tr -d ' ') 个脚本"
  elif [ "${AUTONOMY_ALLOW_MISSING_CLINE_BRIDGE:-0}" = "1" ]; then
    echo "[build][警告] 发版包不带 cline bridge 依赖（AUTONOMY_ALLOW_MISSING_CLINE_BRIDGE=1 显式接受）；" >&2
    echo "[build][警告] 部署上的 cline 后端要自己指一个装好 @cline/sdk 的桥（AUTONOMY_CLINE_BRIDGE_SCRIPT）。" >&2
  else
    echo "[build][错误] 拿不到 cline bridge 依赖（${CLINE_BRIDGE}/node_modules、${cached_tgz}、${NPM_BIN} ci 都不成）：" >&2
    echo "[build][错误] 发版包会缺桥，部署时 scripts/start.sh 的自检会判这次部署失败。" >&2
    echo "[build][错误] 解决：在构建机上跑一次 scripts/install-cline-bridge.sh（或用 AUTONOMY_NPM_BIN 指定 npm 路径）；" >&2
    echo "[build][错误] 或者显式接受缺桥：AUTONOMY_ALLOW_MISSING_CLINE_BRIDGE=1。" >&2
    exit 1
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
