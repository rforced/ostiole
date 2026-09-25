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
# A named package manager rather than whatever the host happens to have,
# so the updates card looks the same everywhere. The server is not root,
# so nothing is ever run through it.
"$BIN" --config-dir "$DIR" --nft "$PWD/e2e/nft-stub.sh" --tc "$PWD/e2e/tc-stub.sh" \
  --smartctl "$PWD/e2e/smartctl-stub.sh" \
  --network-backend none \
  --package-manager dnf \
  serve --listen 127.0.0.1:18090 --log-level warn &
pid=$!
# Playwright stops the server with SIGTERM (gracefulShutdown in the
# config). The shell stays to remove the directory once the server has
# gone, which an exec would not; it passes the signal on, since a
# background child ignores SIGINT.
trap 'kill "$pid" 2>/dev/null || true' INT TERM
wait "$pid" || true
# A trapped signal ends the first wait at once; this one waits for the
# server to exit.
wait "$pid" 2>/dev/null || true
rm -rf "$DIR"
