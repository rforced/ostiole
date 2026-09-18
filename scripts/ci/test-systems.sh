#!/usr/bin/env bash
# Runs on a workstation what CI runs on every test system, so a failure
# there can be looked at here instead of through a log.
#
#   scripts/ci/test-systems.sh [image...]
#
# With no arguments it does every system in .github/test-systems.json.
# It needs goreleaser and podman, builds a snapshot, serves it over HTTP
# the way a release is served, and puts each image through the same
# three tests CI does: the distribution package, the install script, and
# a real install under systemd.
#
# PORT overrides the port; CONTAINER=docker uses docker.
set -euo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
CONTAINER=${CONTAINER:-podman}
PORT=${PORT:-18099}
SYSTEMS=$REPO/.github/test-systems.json

cd "$REPO"
images=("$@")
if [ ${#images[@]} -eq 0 ]; then
  mapfile -t images < <(jq -r '.[].image' "$SYSTEMS")
fi

if [ ! -f internal/web/dist/index.html ]; then
  echo "internal/web/dist holds no build; run 'task web:build' first" >&2
  exit 1
fi

echo "building a snapshot"
goreleaser release --snapshot --clean --skip=sign,publish,sbom,before >/dev/null

SERVE=$(mktemp -d)
server=
cleanup() {
  [ -n "$server" ] && kill "$server" 2>/dev/null
  rm -rf "$SERVE"
}
trap cleanup EXIT

VERSION=$("$REPO/scripts/ci/serve-tree.sh" dist "$SERVE")
python3 -m http.server "$PORT" --bind 0.0.0.0 --directory "$SERVE" >/dev/null 2>&1 &
server=$!

# Proven to answer before anything is run against it. A server that
# never came up — the port still held by a run that leaked one, most
# likely — makes every check below pass for the wrong reason.
for _ in $(seq 1 20); do
  curl -fsS "http://127.0.0.1:$PORT/install.sh" >/dev/null 2>&1 && break
  sleep 0.5
done
if ! curl -fsS "http://127.0.0.1:$PORT/install.sh" >/dev/null 2>&1; then
  echo "nothing is serving the build on :$PORT (is the port already taken?)" >&2
  exit 1
fi
echo "serving $VERSION on :$PORT"

failed=()
for image in "${images[@]}"; do
  echo
  echo "################ $image"
  ok=yes
  "$CONTAINER" run --rm \
    -v "$REPO/dist:/dist:ro" -v "$REPO/scripts/ci:/ci:ro" \
    -v "$REPO/internal/nft/testdata:/testdata:ro" \
    "$image" sh /ci/install-package.sh || ok=no
  "$CONTAINER" run --rm --add-host=host.test:host-gateway \
    -v "$REPO/scripts/ci:/ci:ro" \
    "$image" sh /ci/install-script-test.sh "http://host.test:$PORT" "$VERSION" || ok=no
  CONTAINER=$CONTAINER "$REPO/scripts/ci/run-systemd-test.sh" \
    "$image" "http://host.test:$PORT" "$VERSION" || ok=no
  [ "$ok" = yes ] || failed+=("$image")
done

echo
if [ ${#failed[@]} -gt 0 ]; then
  echo "failed: ${failed[*]}" >&2
  exit 1
fi
echo "every test system passed"
