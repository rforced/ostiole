#!/bin/sh
#   scripts/update-aur.sh [commit]
# Moves install.sh's pin of miniupnpd-nft to a commit of the AUR's, its
# head by default. It shows what changed since the pinned commit, asks
# before running the PKGBUILD, builds it in an Arch container with the key
# install.sh carries, and writes the commit, the file hashes and what the
# package holds. A PKGBUILD naming another signing key stops it: that key
# is checked and put in install.sh by hand.
set -eu

pkg=miniupnpd-nft
script=$(cd "$(dirname "$0")/.." && pwd)/internal/install/install.sh
container=${CONTAINER:-podman}
image=docker.io/library/archlinux:latest

pinned=$(sed -n 's/^AUR_UPNP_COMMIT=//p' "$script")
fingerprint=$(sed -n 's/^AUR_UPNP_KEY_FPR=\([0-9A-F]*\).*/\1/p' "$script")
[ "$(grep -cx '# \(BEGIN\|END\) reviewed miniupnpd-nft' "$script")" = 2 ] ||
	{ echo "install.sh has no reviewed miniupnpd-nft block" >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
repo=$work/$pkg
git clone -q "https://aur.archlinux.org/$pkg.git" "$repo"
target=$(git -C "$repo" rev-parse --verify "${1:-HEAD}^{commit}")

if [ "$target" = "$pinned" ]; then
	echo "install.sh pins $target already; building it again to record its package"
elif [ -n "$pinned" ] && git -C "$repo" cat-file -e "$pinned^{commit}" 2>/dev/null; then
	git -C "$repo" log --format='%h %ad %an %s' --date=short "$pinned..$target"
	git -C "$repo" diff "$pinned" "$target"
else
	echo "the AUR has no $pinned; every file at $target:"
	git -C "$repo" ls-tree -r --name-only "$target" | while read -r f; do
		echo "== $f"
		git -C "$repo" show "$target:$f"
	done
fi

# Plain files with plain names, which is all a PKGBUILD repository needs
# and all install.sh's checks read.
git -C "$repo" ls-tree -r "$target" >"$work/tree"
if grep -qv '^100644 blob [0-9a-f]*	[A-Za-z0-9._+-]*$' "$work/tree"; then
	echo "$target holds something other than plain files:" >&2
	cat "$work/tree" >&2
	exit 1
fi
awk -F'\t' '{ print $2 }' "$work/tree" | LC_ALL=C sort | while read -r f; do
	printf '%s  %s\n' "$(git -C "$repo" show "$target:$f" | sha256sum | cut -d' ' -f1)" "$f"
done >"$work/sums"
named=$(git -C "$repo" show "$target:PKGBUILD" | sed -n "s/^validpgpkeys=('\([0-9A-Fa-f]*\)').*/\1/p")
if [ "$named" != "$fingerprint" ]; then
	echo "the PKGBUILD names the signing key '$named', install.sh carries $fingerprint" >&2
	exit 1
fi

sed -n "/^AUR_UPNP_KEY='/,/^-----END PGP PUBLIC KEY BLOCK-----'\$/p" "$script" |
	sed "s/^AUR_UPNP_KEY='//; s/'\$//" >"$work/key.asc"
export GNUPGHOME="$work/gnupg"
mkdir -m 700 "$GNUPGHOME"
if [ "$(gpg --batch --show-keys --with-colons "$work/key.asc" | awk -F: '$1 == "fpr" { print $10; exit }')" != "$fingerprint" ]; then
	echo "AUR_UPNP_KEY in install.sh is not $fingerprint" >&2
	exit 1
fi
expires=$(gpg --batch --show-keys --with-colons "$work/key.asc" | awk -F: '$1 == "pub" { print $7; exit }')
if [ -n "$expires" ]; then
	echo "miniupnp's key expires $(date -u -d "@$expires" +%F)"
	if [ "$expires" -lt $(($(date +%s) + 365 * 86400)) ]; then
		echo "warning: within a year; fetch its extended copy into install.sh" >&2
	fi
fi

printf 'Run the PKGBUILD at %.7s in %s? [y/N] ' "$target" "$image"
read -r answer
[ "$answer" = y ] || exit 1

git -C "$repo" checkout -q "$target"
rm -rf "$repo/.git"
cat >"$work/build.sh" <<'EOF'
pacman -Syu --noconfirm --needed base-devel lsb-release libcap-ng procps-ng util-linux >/dev/null
mkdir /build
cp -r "/work/$PKG" /work/key.asc /build/
chown -R nobody:nobody /build
HOME=/build setpriv --reuid=nobody --regid=nobody --clear-groups sh -euc \
	"gpg --batch --quiet --import /build/key.asc && cd /build/$PKG && makepkg --noconfirm --nodeps"
p=$(find "/build/$PKG" -maxdepth 1 -name "$PKG-*.pkg.tar.*" ! -name '*.sig' ! -name "$PKG-debug-*" | head -1)
bsdtar -xOf "$p" .PKGINFO | sed -n 's/^pkgver = //p' >/work/version
bsdtar -tf "$p" | LC_ALL=C sort >/work/paths
EOF
"$container" run --rm -e PKG="$pkg" -v "$work:/work" "$image" sh -eu /work/build.sh
if grep -q "'" "$work/paths"; then
	echo "a path in the package has a quote in it" >&2
	exit 1
fi

{
	echo "AUR_UPNP_COMMIT=$target"
	printf "AUR_UPNP_SUMS='"
	sed '$ s/$/'"'"'/' "$work/sums"
	echo "AUR_UPNP_VERSION=$(cat "$work/version")"
	printf "AUR_UPNP_PATHS='"
	sed '$ s/$/'"'"'/' "$work/paths"
} >"$work/pin"
awk -v pin="$work/pin" '
	/^# END reviewed miniupnpd-nft$/ { while ((getline l < pin) > 0) print l; skip = 0 }
	!skip { print }
	/^# BEGIN reviewed miniupnpd-nft$/ { skip = 1 }
' "$script" >"$work/install.sh"
cat "$work/install.sh" >"$script"
echo "pinned $pkg $(cat "$work/version") at $(printf %.7s "$target")"
