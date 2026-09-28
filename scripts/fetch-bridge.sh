#!/usr/bin/env bash
# Download a pinned cursor-sdk-bridge standalone archive.
#
#   scripts/fetch-bridge.sh                # into third_party/ (the checkout's own copy)
#   scripts/fetch-bridge.sh --dest DIR     # into DIR instead (build.sh caches it that way)
#   scripts/fetch-bridge.sh --print-version
#   scripts/fetch-bridge.sh --print-platform   # <os>-<arch> of the target platform
#
# The version is pinned here and nowhere else: build.sh asks for it with
# --print-version rather than knowing it, so a bump cannot end up in two places.
# Same for the platform name it downloads ([--]print-platform), which build.sh uses as the
# cache key — the normalisation from Go's GOOS/GOARCH and from uname lives only here.
#
# Target platform = GOOS/GOARCH when the caller sets them (cross-platform packaging: the
# control plane builds for the deploy machine, see DEPLOY_MACHINES), otherwise the build
# machine's own platform. The released archives are per-platform, so a build *for Linux*
# must fetch the Linux one — the build machine's copy is a different executable format and
# would only fail later, on the target machine.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${CURSOR_SDK_BRIDGE_VERSION:-1.0.31}"

DEST="$ROOT/third_party"
PRINT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --print-version) echo "$VERSION"; exit 0 ;;
    --print-platform) PRINT=platform; shift ;;
    --dest)
      DEST="${2:?--dest needs a directory}"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

OS="$(echo "${GOOS:-$(uname -s)}" | tr '[:upper:]' '[:lower:]')"
ARCH="${GOARCH:-$(uname -m)}"
case "$OS" in
  darwin) OS=darwin ;;
  linux) OS=linux ;;
  *) echo "unsupported os: $OS" >&2; exit 1 ;;
esac
case "$ARCH" in
  x86_64|amd64) ARCH=x64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

if [ "$PRINT" = platform ]; then echo "${OS}-${ARCH}"; exit 0; fi

URL="https://github.com/cursor/sdk-bridge/releases/download/v${VERSION}/cursor-sdk-bridge-standalone-${OS}-${ARCH}.tar.gz"
mkdir -p "$DEST"
TMP="$(mktemp)"
echo "fetching $URL"
curl -fsSL --retry 2 --retry-delay 1 "$URL" -o "$TMP"
tar -xzf "$TMP" -C "$DEST"
rm -f "$TMP"
chmod +x "$DEST/bin/cursor-sdk-bridge"
# Which pin — and which platform — this directory holds, so a cache directory can say whether
# it is the one asked for rather than looking like a copy of whatever happened to be there.
printf '%s\n' "$VERSION" > "$DEST/.version"
printf '%s-%s\n' "$OS" "$ARCH" > "$DEST/.platform"
echo "installed $DEST/bin/cursor-sdk-bridge (v$VERSION, ${OS}-${ARCH})"
