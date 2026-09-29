#!/usr/bin/env bash
# Issues real certificates from a real ACME server, so the client is
# tested against a CA rather than against a mock of one.
#
#   scripts/ci/acme-test.sh
#
# It needs podman and runs pebble (the Let's Encrypt test CA) and
# pebble-challtestsrv on the host network: pebble reaches the solver on
# port 5002 and the challenge server answers every name with 127.0.0.1.
# A second pebble on port 14001 wants an external account binding. BIND 9
# on port 8054 takes the RFC 2136 provider's updates.
#
# CONTAINER=docker uses docker.
set -euo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
CONTAINER=${CONTAINER:-podman}
PEBBLE_IMAGE=${PEBBLE_IMAGE:-ghcr.io/letsencrypt/pebble:latest}
CHALLTESTSRV_IMAGE=${CHALLTESTSRV_IMAGE:-ghcr.io/letsencrypt/pebble-challtestsrv:latest}
BIND_IMAGE=${BIND_IMAGE:-docker.io/internetsystemsconsortium/bind9:9.20}
WORK=$(mktemp -d)

containers() { $CONTAINER rm -f ostiole-pebble ostiole-pebble-eab ostiole-challtestsrv ostiole-bind >/dev/null 2>&1 || true; }
cleanup() {
  containers
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

containers
# The challenge server answers DNS only: our own solver wants port 5002.
$CONTAINER run -d --name ostiole-challtestsrv --network host "$CHALLTESTSRV_IMAGE" \
  -dnsserver :8053 -management :8055 -http01 '' -https01 '' -tlsalpn01 '' -doh '' >/dev/null
# A third of nonces refused, so every test rides out badNonce.
$CONTAINER run -d --name ostiole-pebble --network host -e PEBBLE_VA_NOSLEEP=1 -e PEBBLE_WFE_NONCEREJECT=33 \
  "$PEBBLE_IMAGE" -dnsserver 127.0.0.1:8053 >/dev/null
# pebble's own EAB configuration, moved to the next ports.
mkdir "$WORK/pebble"
cat > "$WORK/pebble/eab.json" <<'EOF'
{
  "pebble": {
    "listenAddress": "0.0.0.0:14001",
    "managementListenAddress": "0.0.0.0:15001",
    "certificate": "test/certs/localhost/cert.pem",
    "privateKey": "test/certs/localhost/key.pem",
    "httpPort": 5002,
    "tlsPort": 5001,
    "ocspResponderURL": "",
    "externalAccountBindingRequired": true,
    "externalAccountMACKeys": {
      "kid-1": "zWNDZM6eQGHWpSRTPal5eIUYFTu7EajVIoguysqZ9wG44nMEtx3MUAsUDkMTQ12W"
    }
  }
}
EOF
chmod 755 "$WORK/pebble"
chmod 644 "$WORK/pebble/eab.json"
$CONTAINER run -d --name ostiole-pebble-eab --network host -e PEBBLE_VA_NOSLEEP=1 -v "$WORK/pebble:/config:ro" \
  "$PEBBLE_IMAGE" -config /config/eab.json -dnsserver 127.0.0.1:8053 >/dev/null

# A primary for example.test that takes updates signed with a key made
# for this run. named reads its configuration before it drops to its own
# user; the zone is copied where that user can write its journal.
BIND_SECRET=$(head -c 32 /dev/urandom | base64)
mkdir "$WORK/bind"
cat > "$WORK/bind/named.conf" <<EOF
options {
  directory "/var/cache/bind";
  listen-on port 8054 { 127.0.0.1; };
  listen-on-v6 { none; };
  recursion no;
  dnssec-validation no;
};
controls { };
key "ostiole-test" {
  algorithm hmac-sha256;
  secret "$BIND_SECRET";
};
zone "example.test" {
  type primary;
  file "/var/lib/bind/example.test.zone";
  allow-update { key "ostiole-test"; };
};
EOF
cat > "$WORK/bind/example.test.zone" <<'EOF'
$TTL 60
@   IN SOA ns1.example.test. hostmaster.example.test. 1 3600 600 86400 60
@   IN NS  ns1.example.test.
ns1 IN A   127.0.0.1
EOF
chmod 755 "$WORK/bind"
chmod 644 "$WORK/bind/named.conf" "$WORK/bind/example.test.zone"
$CONTAINER run -d --name ostiole-bind --network host -v "$WORK/bind:/config:ro" --entrypoint /bin/sh "$BIND_IMAGE" \
  -c 'cp /config/example.test.zone /var/lib/bind/ && chown bind /var/lib/bind/example.test.zone && exec named -u bind -g -c /config/named.conf' >/dev/null

# Pebble's root, which nothing else trusts. Through a tar stream, so the
# container runtime does not have to see the same /tmp as this script.
$CONTAINER cp ostiole-pebble:/test/certs/pebble.minica.pem - | tar -xO > "$WORK/ca.pem"

for port in 14000 14001; do
  for _ in $(seq 60); do
    if curl -sk --max-time 2 "https://127.0.0.1:$port/dir" >/dev/null; then break; fi
    sleep 0.5
  done
  curl -sk --max-time 2 "https://127.0.0.1:$port/dir" >/dev/null || {
    echo "pebble on $port did not come up" >&2
    $CONTAINER logs ostiole-pebble >&2 || true
    $CONTAINER logs ostiole-pebble-eab >&2 || true
    exit 1
  }
done
for _ in $(seq 60); do
  if $CONTAINER logs ostiole-bind 2>&1 | grep -q 'running$'; then break; fi
  sleep 0.5
done
$CONTAINER logs ostiole-bind 2>&1 | grep -q 'running$' || {
  echo "BIND did not come up" >&2
  $CONTAINER logs ostiole-bind >&2 || true
  exit 1
}

cd "$REPO"
PEBBLE_DIRECTORY=https://127.0.0.1:14000/dir \
PEBBLE_EAB_DIRECTORY=https://127.0.0.1:14001/dir \
PEBBLE_CA="$WORK/ca.pem" \
CHALLTESTSRV=http://127.0.0.1:8055 \
  go test -tags acme -count 1 -v ./internal/acme/ -run TestPebble
BIND_SERVER=127.0.0.1:8054 \
BIND_SECRET="$BIND_SECRET" \
  go test -tags acme -count 1 -v ./internal/dnsprovider/ -run TestBIND
