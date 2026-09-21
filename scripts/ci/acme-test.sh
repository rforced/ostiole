#!/usr/bin/env bash
# Issues real certificates from a real ACME server, so the client is
# tested against a CA rather than against a mock of one.
#
#   scripts/ci/acme-test.sh
#
# It needs podman and runs pebble (the Let's Encrypt test CA) and
# pebble-challtestsrv on the host network: pebble reaches the solver on
# port 5002 and the challenge server answers every name with 127.0.0.1.
#
# CONTAINER=docker uses docker.
set -euo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
CONTAINER=${CONTAINER:-podman}
PEBBLE_IMAGE=${PEBBLE_IMAGE:-ghcr.io/letsencrypt/pebble:latest}
CHALLTESTSRV_IMAGE=${CHALLTESTSRV_IMAGE:-ghcr.io/letsencrypt/pebble-challtestsrv:latest}
WORK=$(mktemp -d)

containers() { $CONTAINER rm -f ostiole-pebble ostiole-challtestsrv >/dev/null 2>&1 || true; }
cleanup() {
  containers
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

containers
# The challenge server answers DNS only: our own solver wants port 5002.
$CONTAINER run -d --name ostiole-challtestsrv --network host "$CHALLTESTSRV_IMAGE" \
  -dnsserver :8053 -management :8055 -http01 '' -https01 '' -tlsalpn01 '' -doh '' >/dev/null
$CONTAINER run -d --name ostiole-pebble --network host -e PEBBLE_VA_NOSLEEP=1 \
  "$PEBBLE_IMAGE" -dnsserver 127.0.0.1:8053 >/dev/null

# Pebble's root, which nothing else trusts. Through a tar stream, so the
# container runtime does not have to see the same /tmp as this script.
$CONTAINER cp ostiole-pebble:/test/certs/pebble.minica.pem - | tar -xO > "$WORK/ca.pem"

for _ in $(seq 60); do
  if curl -sk --max-time 2 https://127.0.0.1:14000/dir >/dev/null; then break; fi
  sleep 0.5
done
curl -sk --max-time 2 https://127.0.0.1:14000/dir >/dev/null || {
  echo "pebble did not come up" >&2
  $CONTAINER logs ostiole-pebble >&2 || true
  exit 1
}

cd "$REPO"
PEBBLE_DIRECTORY=https://127.0.0.1:14000/dir \
PEBBLE_CA="$WORK/ca.pem" \
CHALLTESTSRV=http://127.0.0.1:8055 \
  go test -tags acme -count 1 -v ./internal/acme/ -run TestPebble
