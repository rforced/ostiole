#!/bin/sh
# The daemon must never link Caddy: a router that asked for no proxy
# carries none of that code, and its CVEs are then not its problem.
set -eu

if go list -deps ./cmd/ostiole | grep -q github.com/caddyserver; then
	echo "cmd/ostiole imports Caddy; it belongs in cmd/ostiole-proxy only" >&2
	go list -deps ./cmd/ostiole | grep github.com/caddyserver >&2
	exit 1
fi
