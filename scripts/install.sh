#!/bin/sh
# Installs the latest Ostiole release into /usr/local/bin and runs
# `ostiole install`. Usage:
#   curl -fsSL https://github.com/rforced/ostiole/releases/latest/download/install.sh | sudo sh
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

tar -xzf "$TMP/$TARBALL" -C "$TMP" ostiole
install -m 0755 "$TMP/ostiole" "$BIN_DIR/ostiole"
echo "installed $BIN_DIR/ostiole ($("$BIN_DIR/ostiole" version))"

if [ "${OSTIOLE_NO_INSTALL:-}" = "1" ]; then
  echo "skipping 'ostiole install' (OSTIOLE_NO_INSTALL=1)"
  exit 0
fi
exec "$BIN_DIR/ostiole" install
