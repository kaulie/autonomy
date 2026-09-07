#!/usr/bin/env bash
# Download a pinned cursor-sdk-bridge standalone archive into third_party/.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${CURSOR_SDK_BRIDGE_VERSION:-1.0.31}"
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
DEST="$ROOT/third_party"
mkdir -p "$DEST"
TMP="$(mktemp)"
echo "fetching $URL"
curl -fsSL "$URL" -o "$TMP"
tar -xzf "$TMP" -C "$DEST"
rm -f "$TMP"
chmod +x "$DEST/bin/cursor-sdk-bridge"
echo "installed $DEST/bin/cursor-sdk-bridge"
