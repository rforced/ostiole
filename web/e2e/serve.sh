#!/bin/sh
# Starts the real ostiole binary against a throwaway config directory for
# Playwright. Set OSTIOLE_BIN to reuse a prebuilt binary (CI does).
set -eu
cd "$(dirname "$0")/.."
BIN="${OSTIOLE_BIN:-../bin/ostiole}"
if [ ! -x "$BIN" ]; then
  echo "building $BIN" >&2
  (cd .. && go build -o bin/ostiole ./cmd/ostiole)
fi
DIR="$(mktemp -d)"
trap 'rm -rf "$DIR"' EXIT INT TERM
# A named package manager rather than whatever the host happens to have,
# so the updates card looks the same everywhere. The server is not root,
# so nothing is ever run through it.
exec "$BIN" --config-dir "$DIR" --nft "$PWD/e2e/nft-stub.sh" --network-backend none \
  --package-manager dnf \
  serve --listen 127.0.0.1:18090 --log-level warn
