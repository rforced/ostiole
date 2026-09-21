#!/usr/bin/env bash
# Lays out what a GitHub release looks like over HTTP, so the install
# script can be tested against the build in hand instead of against
# whatever was published last.
#
#   serve-tree.sh <dist-dir> <out-dir>
#
# It prints the version it found on stdout, which is what the tests have
# to be told to ask for, and everything else on stderr.
#
# Three trees come out of it: the release itself, a tampered/ whose
# tarball no longer matches its checksums, and a badsig/ whose
# checksums.txt is signed by a key that is not the release key. The two
# poisoned ones are how the tests know the script's refusals work, which
# is the half of it nobody notices until it matters.
set -euo pipefail

DIST=${1:?dist dir}
OUT=${2:?out dir}
REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

shopt -s nullglob
found=("$DIST"/ostiole_*_linux_amd64.tar.gz)
if [ ${#found[@]} -eq 0 ]; then
  echo "no linux amd64 tarball in $DIST; build a snapshot first" >&2
  exit 1
fi
tarball=$(basename "${found[0]}")
# ostiole_0.8.3-next_linux_amd64.tar.gz is the version and nothing else.
version=${tarball#ostiole_}
version=${version%_linux_amd64.tar.gz}

rm -rf "$OUT"
mkdir -p "$OUT" "$OUT/tampered" "$OUT/badsig"

# The sidecar is a second tarball of the same release, so --with-proxy
# has something to fetch.
proxy=("$DIST"/ostiole-proxy_*_linux_amd64.tar.gz)

cp "$REPO/internal/install/install.sh" "$OUT/install.sh"
for tree in "$OUT" "$OUT/tampered" "$OUT/badsig"; do
  cp "$DIST/$tarball" "$DIST/checksums.txt" "$tree/"
  if [ ${#proxy[@]} -gt 0 ]; then
    cp "${proxy[0]}" "$tree/"
  fi
  if [ -f "$DIST/checksums.txt.sig" ]; then
    cp "$DIST/checksums.txt.sig" "$tree/"
  fi
done

# A tarball with a byte on the end of it: the same name, the same
# checksums.txt, and a sha256 that no longer matches.
printf 'x' >>"$OUT/tampered/$tarball"

# A signature that is the right shape and the wrong key. ed25519
# signatures are 64 bytes, and any 64 bytes will do to be refused.
openssl rand 64 | base64 | tr -d '\n' >"$OUT/badsig/checksums.txt.sig"

echo "laid out $tarball under $OUT" >&2
echo "$version"
