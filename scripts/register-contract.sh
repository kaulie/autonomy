#!/usr/bin/env bash
#
# 契约自动登记（autonomy → 服务中心）：
#
#   代码里的 swag 注解 ──swag init──▶ docs/swagger.json ──register.sh──▶ 注册中心
#         唯一真源                       本步骤产物                    幂等：没变化就不写库
#
# 注解写在 cmd/autonomyd/main.go（General API Info）与 src/http_server.go（每个
# handler 一段），所以**接口改了注解跟着改，下一次发布契约自动刷新**，没有人需要手工
# 维护 OpenAPI 文件。src/contract_test.go 保证注解与路由表一一对应。
#
# 脚本本体在服务中心仓库 `client/ci/register-go-service.sh`（公开仓库，幂等）。本包装
# 脚本负责：① 找到那个脚本（环境变量 / 本机检出 / 仓库内 vendored 副本 / 浅克隆兜底）；
# ② 带上本服务的服务名、部门、实例等参数把它跑起来。
#
# 用法：
#   scripts/register-contract.sh                       # 发版 / CI / 本机都能跑，幂等
#   VERSION=1.2.3 scripts/register-contract.sh          # 指定版本（默认 APP_VERSION 或 git describe）
#   INSTANCES=127.0.0.1:4300,10.0.0.9:4300 scripts/register-contract.sh   # 多实例
#   REGISTRY_CLIENT_CI=/path/to/register-go-service.sh scripts/register-contract.sh
#
# 环境变量（★ 可覆盖默认值）：
#   ★ SERVICE_NAME  服务名（默认 autonomy）
#   ★ DEPARTMENT_ID 归属部门（默认 D0005 AI研发部；可用 `curl $REGISTRY_URL/v1/departments` 查）
#   ★ INSTANCES     实例地址，逗号分隔（默认 127.0.0.1:4300 —— 本服务在部署平台的契约端口）
#   ★ REGISTRY_URL  注册中心地址（默认 http://127.0.0.1:4240，只绑本机）
#   ★ REGISTRY_NS   命名空间（默认 default）；REGISTRY_TOKEN 写令牌（写接口收紧后才需要）
#   ★ VERSION / OWNER / HEALTH_PATH / TAGS / GIT_REPO / SWAG_MAIN / SWAG_ARGS
#   ★ REGISTRY_CLIENT_CI    直接指定 register-go-service.sh 的路径（跳过自动查找）
#   ★ SERVICE_REGISTRY_DIR  服务中心仓库本地检出目录（找它的 client/ci/）
#   ★ REGISTRY_CLIENT_CACHE 找不到本地检出时的浅克隆目录（默认 $TMPDIR/autonomy-registry-client）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

log() { echo "[register-contract] $*"; }
die() { echo "[register-contract][错误] $*" >&2; exit 1; }

SERVICE_NAME="${SERVICE_NAME:-autonomy}"
DEPARTMENT_ID="${DEPARTMENT_ID:-D0005}"
INSTANCES="${INSTANCES:-127.0.0.1:4300}"
REGISTRY_URL="${REGISTRY_URL:-http://127.0.0.1:4240}"
REGISTRY_NS="${REGISTRY_NS:-default}"
OWNER="${OWNER:-kaulie}"
HEALTH_PATH="${HEALTH_PATH:-/health}"
TAGS="${TAGS:-tasks}"
GIT_REPO="${GIT_REPO:-https://github.com/kaulie/autonomy}"
# 入口就是 swag 读 General API Info 的地方。本服务的 HTTP 服务在 cmd/autonomyd，而
# cmd/autonomy 是它的客户端（自动探测会选中它），所以显式指定。
SWAG_MAIN="${SWAG_MAIN:-cmd/autonomyd/main.go}"
SWAG_OUT="${SWAG_OUT:-docs}"
# 只产 swagger.json：不生成 docs.go（那会把 swaggo 拖成运行期依赖），产物也不提交（见 .gitignore）。
SWAG_ARGS="${SWAG_ARGS:---outputTypes json}"
REGISTRY_REPO="${REGISTRY_REPO:-https://github.com/kaulie/service-registry}"

if [ -z "${VERSION:-}" ]; then
  VERSION="${APP_VERSION:-}"
fi
if [ -z "${VERSION}" ]; then
  VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
fi

# swag 通常是 `go install` 装到 GOPATH/bin；非交互式 shell（CI / 发版脚本）里那个目录
# 不保证在 PATH 上，这里补一下，免得跑去联网重装。
if ! command -v swag >/dev/null 2>&1; then
  GOBIN_DIR="$(go env GOPATH 2>/dev/null)/bin"
  if [ -x "${GOBIN_DIR}/swag" ]; then
    PATH="${GOBIN_DIR}:${PATH}"
    export PATH
  fi
fi

# ---- 找到服务中心的 CI 登记脚本 ----
# 顺序：显式指定 → 服务中心本地检出 → 本仓库 vendored 副本 → 浅克隆兜底。
find_client_ci() {
  local cand
  for cand in \
    "${REGISTRY_CLIENT_CI:-}" \
    "${SERVICE_REGISTRY_DIR:+${SERVICE_REGISTRY_DIR}/client/ci/register-go-service.sh}" \
    "${ROOT}/client/ci/register-go-service.sh" \
    "${HOME}/Projects/service-registry/client/ci/register-go-service.sh" \
    ${HOME}/agent-workspace/service-registry/*/client/ci/register-go-service.sh
  do
    [ -n "${cand}" ] && [ -f "${cand}" ] && { echo "${cand}"; return 0; }
  done
  return 1
}

CLIENT_CI=""
if ! CLIENT_CI="$(find_client_ci)"; then
  CACHE="${REGISTRY_CLIENT_CACHE:-${TMPDIR:-/tmp}/autonomy-registry-client}"
  if [ -f "${CACHE}/client/ci/register-go-service.sh" ]; then
    log "复用缓存的服务中心脚本：${CACHE}"
  else
    log "本机没有服务中心客户端脚本，浅克隆 ${REGISTRY_REPO} → ${CACHE}"
    rm -rf "${CACHE}"
    git clone --depth 1 "${REGISTRY_REPO}" "${CACHE}" >/dev/null 2>&1 ||
      die "克隆失败：${REGISTRY_REPO}（可用 REGISTRY_CLIENT_CI 直接指定脚本路径）"
  fi
  CLIENT_CI="${CACHE}/client/ci/register-go-service.sh"
fi
log "使用服务中心脚本：${CLIENT_CI}"

log "登记 ${SERVICE_NAME}：部门=${DEPARTMENT_ID} 实例=${INSTANCES} 版本=${VERSION} → ${REGISTRY_URL}"
# 环境变量一路透传给 register-go-service.sh：swag init（读注解）→ register.sh（幂等上报）。
SERVICE_NAME="${SERVICE_NAME}" \
DEPARTMENT_ID="${DEPARTMENT_ID}" \
INSTANCES="${INSTANCES}" \
REGISTRY_URL="${REGISTRY_URL}" \
REGISTRY_NS="${REGISTRY_NS}" \
VERSION="${VERSION}" \
OWNER="${OWNER}" \
HEALTH_PATH="${HEALTH_PATH}" \
TAGS="${TAGS}" \
GIT_REPO="${GIT_REPO}" \
SWAG_MAIN="${SWAG_MAIN}" \
SWAG_OUT="${SWAG_OUT}" \
SWAG_ARGS="${SWAG_ARGS}" \
  bash "${CLIENT_CI}"
