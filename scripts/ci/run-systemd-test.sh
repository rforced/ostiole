#!/usr/bin/env bash
# Boots an image with systemd as PID 1 and installs Ostiole into it for
# real. Everything that matters happens in the two scripts this drives;
# what is here is the part that differs between a container runtime and
# a router, which is getting systemd to be PID 1 at all.
#
#   run-systemd-test.sh <image> <base-url> <version>
#
# CONTAINER=podman runs it against podman instead of docker, which is
# how this is tried on a workstation before CI ever sees it.
set -euo pipefail

IMAGE=${1:?image}
BASE=${2:?base url}
VERSION=${3:?version}
CONTAINER=${CONTAINER:-docker}
CI_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
NAME=ostiole-router

cleanup() { "$CONTAINER" rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

# The cgroup mount and a host cgroup namespace are what systemd needs to
# manage units from inside a container; the tmpfs mounts are what it
# needs to have a /run of its own. --privileged is for the firewall:
# loading a table wants NET_ADMIN, and it is the container's own network
# namespace that gets it, not the host's.
#
# /tmp is spelled out because docker mounts a --tmpfs noexec and podman
# does not, and a workstation run that differs from CI in what it will
# run is a workstation run that proves nothing.
"$CONTAINER" run -d --name "$NAME" \
  --privileged --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  --tmpfs /run --tmpfs /run/lock --tmpfs /tmp:rw,nosuid,nodev,noexec \
  --add-host=host.test:host-gateway \
  -v "$CI_DIR:/ci:ro" \
  "$IMAGE" /ci/systemd-boot.sh >/dev/null

# systemd-boot.sh installs packages before it execs systemd, so how long
# this takes is a package manager's business. "degraded" is the ordinary
# answer in a container: some units have nothing to do here.
state=
for _ in $(seq 1 120); do
  state=$("$CONTAINER" exec "$NAME" systemctl is-system-running 2>/dev/null || true)
  case "$state" in running | degraded) break ;; esac
  sleep 2
done
case "$state" in
running | degraded) ;;
*)
  echo "systemd never came up in $IMAGE (last said: ${state:-nothing})" >&2
  "$CONTAINER" logs "$NAME" 2>&1 | tail -40 >&2
  exit 1
  ;;
esac

if ! "$CONTAINER" exec "$NAME" sh /ci/install-systemd-test.sh "$BASE" "$VERSION"; then
  echo "--- journal ---" >&2
  "$CONTAINER" exec "$NAME" journalctl -u ostiole.service -u ostiole-firewall.service --no-pager -n 50 >&2 || true
  exit 1
fi
