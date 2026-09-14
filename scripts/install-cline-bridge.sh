#!/usr/bin/env bash
# Install the Node dependencies for the Cline bridge (src/clinesdk/bridge).
#
# The bridge wraps the Cline SDK (@cline/sdk), which is TypeScript-only, so the
# Go runtime drives it through a Node child process. Run this once per checkout;
# node_modules is gitignored.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BRIDGE="$ROOT/src/clinesdk/bridge"

if ! command -v npm >/dev/null 2>&1; then
  echo "npm is required to install the Cline bridge (Node >= 22)" >&2
  exit 1
fi

cd "$BRIDGE"
echo "installing Cline bridge dependencies in $BRIDGE"
if [ -f package-lock.json ]; then
  npm ci --no-audit --no-fund
else
  npm install --no-audit --no-fund
fi
echo "installed @cline/sdk into $BRIDGE/node_modules"
