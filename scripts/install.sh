#!/bin/sh
# Installs the latest Ostiole release into /usr/local/bin and runs
# `ostiole install`. Usage:
#   curl -fsSL https://github.com/rforced/ostiole/releases/latest/download/install.sh | sudo sh
#   curl -fsSL … | sudo sh -s -- --yes      (agree to the plan in advance)
# Environment: OSTIOLE_VERSION=v0.1.0 pins a version; OSTIOLE_NO_INSTALL=1 only places the binary.
set -eu

REPO="${OSTIOLE_REPO:-rforced/ostiole}"
BIN_DIR="${OSTIOLE_BIN_DIR:-/usr/local/bin}"

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root (sudo sh)" >&2
  exit 1
fi
for tool in curl tar; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required" >&2; exit 1; }
done
command -v nft >/dev/null 2>&1 || echo "warning: nft not found; install the nftables package before running ostiole install" >&2

# Ostiole supports Linux 5.14 and newer, which is RHEL 9 and every current
# Debian, Ubuntu, Fedora, Alpine, and Arch. A release string this cannot
# parse is allowed through rather than blocking the install.
kernel_too_old() {
  rel="$1"
  major="${rel%%.*}"
  rest="${rel#*.}"
  if [ "$rest" = "$rel" ]; then return 1; fi
  minor="${rest%%[!0-9]*}"
  case "$major" in '' | *[!0-9]*) return 1 ;; esac
  case "$minor" in '' | *[!0-9]*) return 1 ;; esac
  if [ "$major" -lt 5 ]; then return 0; fi
  if [ "$major" -eq 5 ] && [ "$minor" -lt 14 ]; then return 0; fi
  return 1
}
if kernel_too_old "$(uname -r)"; then
  echo "unsupported kernel: $(uname -r) (Ostiole needs Linux 5.14 or newer)" >&2
  if [ "${OSTIOLE_IGNORE_KERNEL:-0}" != "1" ]; then
    echo "set OSTIOLE_IGNORE_KERNEL=1 to install anyway" >&2
    exit 1
  fi
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

VERSION="${OSTIOLE_VERSION:-}"
if [ -z "$VERSION" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$VERSION" ] || { echo "could not determine the latest release" >&2; exit 1; }
fi
PLAIN="${VERSION#v}"
BASE="https://github.com/$REPO/releases/download/$VERSION"
TARBALL="ostiole_${PLAIN}_linux_${ARCH}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
echo "downloading ostiole $VERSION for linux/$ARCH"
curl -fsSL -o "$TMP/$TARBALL" "$BASE/$TARBALL"
curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"
WANT="$(grep " $TARBALL\$" "$TMP/checksums.txt" | awk '{print $1}')"
[ -n "$WANT" ] || { echo "$TARBALL missing from checksums.txt" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  GOT="$(sha256sum "$TMP/$TARBALL" | awk '{print $1}')"
else
  GOT="$(shasum -a 256 "$TMP/$TARBALL" | awk '{print $1}')"
fi
[ "$GOT" = "$WANT" ] || { echo "checksum mismatch for $TARBALL" >&2; exit 1; }

# The checksums file is signed with the release ed25519 key, the same one
# the built-in updater trusts. Verifying it here means a tampered
# checksums.txt cannot hand you a tampered tarball. Rotation: add the new
# key to the list, keep the old one for a release.
OSTIOLE_RELEASE_KEYS="MCowBQYDK2VwAyEA4a3rf0bCdQNTKO3KODxqMrdT1+T1nq9t+KNN2f9DJ0U=" # gitleaks:allow (public key)
verify_signature() {
  if ! command -v openssl >/dev/null 2>&1 || ! command -v base64 >/dev/null 2>&1; then
    echo "note: openssl or base64 missing, skipping the signature check" >&2
    return 0
  fi
  if ! curl -fsSL -o "$TMP/checksums.txt.sig" "$BASE/checksums.txt.sig"; then
    echo "warning: this release publishes no signature" >&2
    return 0
  fi
  base64 -d < "$TMP/checksums.txt.sig" > "$TMP/sig.bin" 2>/dev/null ||
    { echo "malformed signature" >&2; exit 1; }
  for key in $OSTIOLE_RELEASE_KEYS; do
    {
      echo "-----BEGIN PUBLIC KEY-----"
      echo "$key"
      echo "-----END PUBLIC KEY-----"
    } > "$TMP/pub.pem"
    if openssl pkeyutl -verify -pubin -inkey "$TMP/pub.pem" -rawin \
        -in "$TMP/checksums.txt" -sigfile "$TMP/sig.bin" >"$TMP/openssl.out" 2>&1; then
      echo "signature verified"
      return 0
    fi
    # An openssl too old for ed25519 cannot tell us anything either way.
    if grep -qiE "unknown option|unsupported|not supported|usage" "$TMP/openssl.out"; then
      echo "note: this openssl cannot check ed25519, skipping the signature check" >&2
      return 0
    fi
  done
  echo "signature check failed for checksums.txt" >&2
  exit 1
}
verify_signature

tar -xzf "$TMP/$TARBALL" -C "$TMP" ostiole
install -m 0755 "$TMP/ostiole" "$BIN_DIR/ostiole"
echo "installed $BIN_DIR/ostiole ($("$BIN_DIR/ostiole" version))"

if [ "${OSTIOLE_NO_INSTALL:-}" = "1" ]; then
  echo "skipping 'ostiole install' (OSTIOLE_NO_INSTALL=1)"
  exit 0
fi
# Arguments after `sh -s --` are the install command's, so the plan can be
# agreed to in advance from a script: `… | sudo sh -s -- --yes`. Asked
# nothing, ostiole install puts its question to /dev/tty, because stdin
# here is the pipe this script came down.
exec "$BIN_DIR/ostiole" install "$@"
