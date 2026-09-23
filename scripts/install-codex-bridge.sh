#!/usr/bin/env bash
# Install the Node dependencies for the Codex bridge (src/codexsdk/bridge).
#
# The bridge wraps the OpenAI Codex SDK (@openai/codex-sdk), which is Node (and drives the
# `codex` CLI), so the Go runtime drives it through a Node child process. Run this once per checkout;
# node_modules is gitignored.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BRIDGE="$ROOT/src/codexsdk/bridge"

if ! command -v npm >/dev/null 2>&1; then
  echo "npm is required to install the Codex bridge (Node >= 18)" >&2
  exit 1
fi

cd "$BRIDGE"
echo "installing Codex bridge dependencies in $BRIDGE"
if [ -f package-lock.json ]; then
  npm ci --no-audit --no-fund
else
  npm install --no-audit --no-fund
fi
echo "installed @openai/codex-sdk into $BRIDGE/node_modules"
