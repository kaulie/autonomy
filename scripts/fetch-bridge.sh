#!/usr/bin/env bash
# Download a pinned cursor-sdk-bridge standalone archive.
#
#   scripts/fetch-bridge.sh                # into third_party/ (the checkout's own copy)
#   scripts/fetch-bridge.sh --dest DIR     # into DIR instead (build.sh caches it that way)
#   scripts/fetch-bridge.sh --print-version
#
# The version is pinned here and nowhere else: build.sh asks for it with
# --print-version rather than knowing it, so a bump cannot end up in two places.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${CURSOR_SDK_BRIDGE_VERSION:-1.0.31}"

DEST="$ROOT/third_party"
while [ $# -gt 0 ]; do
  case "$1" in
    --print-version) echo "$VERSION"; exit 0 ;;
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

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
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
URL="https://github.com/cursor/sdk-bridge/releases/download/v${VERSION}/cursor-sdk-bridge-standalone-${OS}-${ARCH}.tar.gz"
mkdir -p "$DEST"
TMP="$(mktemp)"
echo "fetching $URL"
curl -fsSL --retry 2 --retry-delay 1 "$URL" -o "$TMP"
tar -xzf "$TMP" -C "$DEST"
rm -f "$TMP"
chmod +x "$DEST/bin/cursor-sdk-bridge"
# Which pin this directory holds, so a cache directory can say whether it is the one asked
# for rather than looking like a copy of whatever version happened to be there.
printf '%s\n' "$VERSION" > "$DEST/.version"
echo "installed $DEST/bin/cursor-sdk-bridge (v$VERSION)"
