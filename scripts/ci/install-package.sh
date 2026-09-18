#!/bin/sh
# Installs the package built for this distribution out of /dist and
# checks that what comes out of it runs. Whichever package manager the
# image has decides which package is meant for it.
set -eu
# shellcheck source=scripts/ci/lib.sh
. "$(dirname "$0")/lib.sh"

step "installing the package for $(distro)"
case "$(manager)" in
apt-get)
	pkg_refresh
	# Not pkg_install: apt takes a path to a file rather than a name.
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq /dist/*_linux_amd64.deb >/dev/null
	;;
dnf) dnf install -y -q /dist/*_linux_amd64.rpm >/dev/null ;;
pacman)
	pacman -Sy --noconfirm >/dev/null
	pacman -U --noconfirm /dist/*_linux_amd64.pkg.tar.zst >/dev/null
	;;
*) fail "no package manager on this image" ;;
esac

step "the binary in it runs"
ostiole version
ostiole render --file /testdata/full.json | head -3
# check wants an nft to hand the ruleset to; /bin/true stands in for one
# on an image that has no nftables, and a refusal is an answer too.
ostiole check --file /testdata/full.json --nft /bin/true 2>&1 | head -1 || true
