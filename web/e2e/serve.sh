#!/bin/sh
# Starts the real ostiole binary against a throwaway config directory for
# Playwright. Set OSTIOLE_BIN to reuse a prebuilt binary (CI does),
# E2E_PORT to listen elsewhere than 18090, E2E_SEED to start from a copy of
# a directory, and E2E_DIR to use a directory and keep it.
set -eu
cd "$(dirname "$0")/.."
BIN="${OSTIOLE_BIN:-../bin/ostiole}"
if [ ! -x "$BIN" ]; then
  echo "building $BIN" >&2
  (cd .. && go build -o bin/ostiole ./cmd/ostiole)
fi
DIR="${E2E_DIR:-$(mktemp -d)}"
if [ -n "${E2E_SEED:-}" ]; then
  cp -a "$E2E_SEED/." "$DIR"
fi
# A named package manager rather than whatever the host happens to have,
# so the updates card looks the same everywhere. The server is not root,
# so nothing is ever run through it. Releases are asked of the stand-in
# 28-updates.spec.js runs, never of GitHub, and dynamic DNS writes to the
# one 29-ddns.spec.js runs, never to Cloudflare. Backups and log files go
# beside the configuration, since the server may not write /var.
"$BIN" --config-dir "$DIR" --nft "$PWD/e2e/nft-stub.sh" --tc "$PWD/e2e/tc-stub.sh" \
  --smartctl "$PWD/e2e/smartctl-stub.sh" \
  --network-backend none \
  --package-manager dnf \
  --update-api http://127.0.0.1:18096 \
  --cloudflare-api http://127.0.0.1:18097 \
  --backup-dir "$DIR/backups" \
  --log-dir "$DIR/log" \
  serve --listen "127.0.0.1:${E2E_PORT:-18090}" --log-level warn &
pid=$!
# The e2e fixtures stop the server with SIGTERM. The shell stays to remove
# the directory once the server has gone, which an exec would not; it
# passes the signal on, since a background child ignores SIGINT.
trap 'kill "$pid" 2>/dev/null || true' INT TERM
wait "$pid" || true
# A trapped signal ends the first wait at once; this one waits for the
# server to exit.
wait "$pid" 2>/dev/null || true
if [ -z "${E2E_DIR:-}" ]; then
  rm -rf "$DIR"
fi
