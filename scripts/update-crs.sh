#!/bin/sh
#   scripts/update-crs.sh 4.29.0
set -eu

version=${1:?usage: scripts/update-crs.sh <version>, e.g. 4.29.0}
# The fingerprint the CRS SECURITY.md gives for its release key.
fingerprint=36006F0E0BA167832158821138EEACA1AB8A6E72
tarball=coreruleset-$version-minimal.tar.gz
base=https://github.com/coreruleset/coreruleset/releases/download/v$version
dest=$(dirname "$0")/../third_party/coreruleset

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "$work/$tarball" "$base/$tarball"
curl -fsSL -o "$work/$tarball.asc" "$base/$tarball.asc"
curl -fsSL -o "$work/key.asc" https://coreruleset.org/security.asc

export GNUPGHOME="$work/gnupg"
mkdir -m 700 "$GNUPGHOME"
gpg --batch --quiet --import "$work/key.asc" 2>/dev/null
if ! gpg --batch --status-fd 1 --verify "$work/$tarball.asc" "$work/$tarball" 2>/dev/null |
	grep -Eq "^\[GNUPG:\] VALIDSIG .* $fingerprint\$"; then
	echo "$tarball is not signed by the CRS release key" >&2
	exit 1
fi

tar -xzf "$work/$tarball" -C "$work"
src=$work/coreruleset-$version
# The daemon writes SecDefaultAction for phases 3 and 4, as every daemon
# before CRS 4.29 did, and Coraza refuses a second one for a phase, so
# crs-setup's come out. A daemon and a proxy one release apart then load
# each other's files. Phases 1, 2 and 5 stay crs-setup's. A crs-setup
# without these two lines as written here stops the update for a look.
for phase in 3 4; do
	if ! grep -qx "SecDefaultAction \"phase:$phase,log,auditlog,pass\"" "$src/crs-setup.conf.example"; then
		echo "crs-setup.conf.example has no SecDefaultAction \"phase:$phase,log,auditlog,pass\"" >&2
		exit 1
	fi
done
rm -rf "$dest/rules"
mkdir -p "$dest/rules/@owasp_crs"
cp "$src/LICENSE" "$dest/rules/LICENSE"
grep -vx 'SecDefaultAction "phase:[34],log,auditlog,pass"' "$src/crs-setup.conf.example" >"$dest/rules/@crs-setup.conf.example"
for f in "$src"/rules/*; do
	case $f in *.example) continue ;; esac
	awk 'NR <= 9 { print; next }
		{ t = $0; sub(/^[[:space:]]+/, "", t) }
		t == "" || t ~ /^#/ { next }
		{ print }' "$f" >"$dest/rules/@owasp_crs/${f##*/}"
done

cat >"$dest/VERSION" <<EOF
# The Core Rule Set release in rules/, written by scripts/update-crs.sh,
# and the sha256 of the signed tarball it came from. The script takes
# crs-setup's SecDefaultAction for phases 3 and 4 out, since the daemon
# writes those. coreruleset.go and LICENSE are coraza-coreruleset
# v4.25.0's, unchanged.
v$version $(sha256sum "$work/$tarball" | cut -d' ' -f1)
EOF
