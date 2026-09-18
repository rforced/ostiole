#!/bin/sh
# Turns a Linux host into an Ostiole router: installs what a router needs,
# places the signed binary, runs `ostiole install`, then removes the
# firewalls, network managers and updaters it replaces. Usage:
#   curl -fsSL https://github.com/rforced/ostiole/releases/latest/download/install.sh | sudo sh
#   curl -fsSL … | sudo sh -s -- --yes      (agree to the plan in advance)
#
# Flags: --yes, --dry-run, --keep <package> (repeatable). Anything else is
# passed to `ostiole install` (--listen, --timezone).
#
# Environment: OSTIOLE_VERSION pins a version; OSTIOLE_BASE_URL downloads
# from somewhere other than the GitHub release; OSTIOLE_NO_INSTALL=1 only
# places the binary; OSTIOLE_NO_DOWNLOAD=1 keeps the binary already in
# place (`ostiole repair`); OSTIOLE_IGNORE_KERNEL=1; OSTIOLE_UPNP_FORCE=1.
set -eu

REPO="${OSTIOLE_REPO:-rforced/ostiole}"
BIN_DIR="${OSTIOLE_BIN_DIR:-/usr/local/bin}"
# Enterprise Linux packages no miniupnpd and EPEL has no branch for it.
# The Fedora build runs there unchanged; this is where it comes from.
FEDORA_RELEASE=44
# Arch packages miniupnpd in the AUR only, and this is the nftables build.
AUR_UPNP=miniupnpd-nft
# Set when a toolchain went on to build it, so the sweep runs afterwards
# and takes it away again.
BUILD_TOOLS=0

YES=0
DRY_RUN=0
KEEP=""
INSTALL_ARGS=""
while [ $# -gt 0 ]; do
	case "$1" in
	-y | --yes) YES=1 ;;
	--dry-run) DRY_RUN=1 ;;
	--keep)
		shift
		[ $# -gt 0 ] || { echo "--keep needs a package name" >&2; exit 1; }
		KEEP="$KEEP $1"
		;;
	--keep=*) KEEP="$KEEP ${1#--keep=}" ;;
	*) INSTALL_ARGS="$INSTALL_ARGS $1" ;;
	esac
	shift
done

### preflight

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root (sudo sh)" >&2
	exit 1
fi
for tool in curl tar; do
	command -v "$tool" >/dev/null 2>&1 || { echo "$tool is required" >&2; exit 1; }
done

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
x86_64 | amd64) ARCH=amd64; RPM_ARCH=x86_64 ;;
aarch64 | arm64) ARCH=arm64; RPM_ARCH=aarch64 ;;
*) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

MANAGER=""
for m in apt-get dnf pacman apk zypper; do
	if command -v "$m" >/dev/null 2>&1; then
		MANAGER="$m"
		break
	fi
done
[ -n "$MANAGER" ] || { echo "no supported package manager (apt-get, dnf, pacman, apk, zypper)" >&2; exit 1; }

# Enterprise Linux differs from Fedora in three places: EPEL, miniupnpd,
# and systemd-networkd being a package of its own.
# shellcheck source=/dev/null
OS_ID="$(. /etc/os-release 2>/dev/null; echo "${ID:-}")"
# shellcheck source=/dev/null
OS_LIKE="$(. /etc/os-release 2>/dev/null; echo "${ID_LIKE:-}")"
# shellcheck source=/dev/null
OS_VERSION="$(. /etc/os-release 2>/dev/null; echo "${VERSION_ID:-}")"
EL=0
case "$OS_ID" in rocky | rhel | almalinux | centos) EL=1 ;; esac
case " $OS_LIKE " in *rhel*) EL=1 ;; esac
# Which Fedora build of miniupnpd this Enterprise Linux can run. Its glibc
# decides: EL 10 is 2.39 and takes the current one; EL 9 is 2.34, and
# Fedora 38 is the newest with both an old enough glibc and the nftables
# backend — 36 and older are the iptables build, which would write
# mappings into tables Ostiole's chains never see.
FEDORA_UPNP=0
if [ "$EL" -eq 1 ]; then
	case "${OS_VERSION%%.*}" in
	9) FEDORA_UPNP=38 ;;
	'' | *[!0-9]*) ;;
	*) [ "${OS_VERSION%%.*}" -lt 9 ] || FEDORA_UPNP="$FEDORA_RELEASE" ;;
	esac
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

### the package tables

case "$MANAGER" in
apt-get)
	WANT="nftables dnsmasq unbound miniupnpd-nftables ppp iproute2"
	# Newer Debian and Ubuntu split networkd out of the systemd package.
	if apt-cache show systemd-networkd >/dev/null 2>&1; then
		WANT="$WANT systemd-networkd"
	fi
	UPNP_NOTE="miniupnpd-nftables"
	;;
dnf)
	if [ "$EL" -eq 1 ]; then
		WANT="systemd-networkd nftables dnsmasq unbound ppp iproute-tc cpio"
		UPNP_NOTE="miniupnpd unpacked from Fedora $FEDORA_UPNP"
		if [ "$FEDORA_UPNP" -eq 0 ]; then
			UPNP_NOTE="no UPnP: no Fedora build runs on this release"
		fi
	else
		WANT="nftables dnsmasq unbound miniupnpd ppp iproute-tc"
		UPNP_NOTE="miniupnpd"
	fi
	;;
pacman)
	WANT="nftables dnsmasq unbound ppp iproute2"
	UPNP_NOTE="miniupnpd-nft built from the AUR (base-devel goes on to build it)"
	;;
apk)
	WANT="nftables dnsmasq unbound miniupnpd-nftables ppp-daemon ppp-pppoe iproute2-tc"
	UPNP_NOTE="miniupnpd-nftables"
	;;
zypper)
	WANT="nftables dnsmasq unbound miniupnpd ppp iproute2"
	UPNP_NOTE="miniupnpd"
	;;
esac

# What a router has no use for, by every name these carry. A pattern is
# matched against the package database, so the names that do not exist on
# this distribution cost nothing.
UNWANTED="firewalld ufw iptables-services iptables-persistent netfilter-persistent shorewall shorewall6
NetworkManager network-manager networkmanager cockpit* netplan.io dhcpcd dhcpcd-base connman wicked wicked-service ifupdown
unattended-upgrades dnf-automatic yum-cron PackageKit packagekit
snapd ModemManager modemmanager udisks2 upower fwupd multipath-tools device-mapper-multipath lxd-installer"

# The package behind these is the package manager itself, so the timer is
# masked and nothing is removed.
case "$MANAGER" in
apt-get) MASK_ONLY="apt-daily.timer apt-daily-upgrade.timer" ;;
dnf) MASK_ONLY="dnf-makecache.timer" ;;
*) MASK_ONLY="" ;;
esac

# units_for names what systemd calls a package, for the case where the
# manager refuses to remove it and masking is all that is left.
units_for() {
	case "$1" in
	firewalld) echo "firewalld.service" ;;
	ufw) echo "ufw.service" ;;
	iptables-services) echo "iptables.service ip6tables.service" ;;
	iptables-persistent | netfilter-persistent) echo "netfilter-persistent.service" ;;
	shorewall) echo "shorewall.service" ;;
	shorewall6) echo "shorewall6.service" ;;
	NetworkManager | network-manager | networkmanager) echo "NetworkManager.service NetworkManager-wait-online.service" ;;
	cockpit*) echo "cockpit.socket" ;;
	dhcpcd | dhcpcd-base) echo "dhcpcd.service" ;;
	connman) echo "connman.service" ;;
	wicked*) echo "wicked.service" ;;
	ifupdown) echo "" ;;
	netplan.io) echo "" ;;
	unattended-upgrades) echo "unattended-upgrades.service" ;;
	dnf-automatic) echo "dnf-automatic.timer dnf-automatic-install.timer" ;;
	yum-cron) echo "yum-cron.service" ;;
	PackageKit | packagekit) echo "packagekit.service packagekit-offline-update.service" ;;
	snapd) echo "snapd.service snapd.socket" ;;
	ModemManager | modemmanager) echo "ModemManager.service" ;;
	udisks2) echo "udisks2.service" ;;
	upower) echo "upower.service" ;;
	fwupd) echo "fwupd.service fwupd-refresh.timer" ;;
	multipath-tools | device-mapper-multipath) echo "multipathd.service multipathd.socket" ;;
	lxd-installer) echo "lxd-installer.socket" ;;
	*) echo "" ;;
	esac
}

### the package manager

pkg_refresh() {
	case "$MANAGER" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get update -qq ;;
	dnf) dnf -q makecache >/dev/null 2>&1 || true ;;
	pacman) pacman -Sy --noconfirm >/dev/null ;;
	apk) apk update >/dev/null ;;
	zypper) zypper --non-interactive refresh >/dev/null ;;
	esac
}

pkg_install() {
	case "$MANAGER" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get install -y -q "$@" ;;
	dnf) dnf -y install "$@" ;;
	pacman) pacman -S --noconfirm --needed "$@" ;;
	apk) apk add --no-cache "$@" ;;
	zypper) zypper --non-interactive install "$@" ;;
	esac
}

pkg_remove() {
	case "$MANAGER" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get remove --purge -y -q "$@" ;;
	dnf) dnf -y remove "$@" ;;
	pacman) pacman -Rns --noconfirm "$@" ;;
	apk) apk del "$@" ;;
	zypper) zypper --non-interactive remove "$@" ;;
	esac
}

# pkg_explicit marks the router's own packages as wanted in their own
# right, so the autoremove that follows the removals cannot take one that
# only arrived as somebody else's dependency.
pkg_explicit() {
	case "$MANAGER" in
	apt-get) apt-mark manual "$@" >/dev/null 2>&1 || true ;;
	dnf) dnf mark user "$@" >/dev/null 2>&1 || dnf mark install "$@" >/dev/null 2>&1 || true ;;
	pacman) pacman -D --asexplicit "$@" >/dev/null 2>&1 || true ;;
	esac
}

pkg_autoremove() {
	case "$MANAGER" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get autoremove --purge -y -q && apt-get clean ;;
	dnf) dnf -y autoremove && dnf clean all >/dev/null ;;
	pacman)
		orphans=""
		for p in $(pacman -Qtdq 2>/dev/null || true); do
			protected "$p" && continue
			orphans="$orphans $p"
		done
		# shellcheck disable=SC2086 # the list is built to be split
		[ -z "$orphans" ] || pacman -Rns --noconfirm $orphans >/dev/null
		pacman -Sc --noconfirm >/dev/null
		;;
	esac
}

# installed_names is asked once; every "is this on the router" question
# below is a match against the answer.
case "$MANAGER" in
apt-get) dpkg-query -W -f '${Package} ${Status}\n' 2>/dev/null |
	awk '$2=="install" && $3=="ok" && $4=="installed" {print $1}' >"$TMP/installed" ;;
dnf | zypper) rpm -qa --qf '%{NAME}\n' 2>/dev/null >"$TMP/installed" ;;
pacman) pacman -Qq 2>/dev/null >"$TMP/installed" ;;
apk) apk info 2>/dev/null >"$TMP/installed" ;;
esac

# matching lists the installed packages a pattern names.
matching() {
	while IFS= read -r p; do
		# shellcheck disable=SC2254 # the pattern is meant to glob
		case "$p" in $1) echo "$p" ;; esac
	done <"$TMP/installed"
}

# Ostiole's own packages and the ones a router cannot be repaired without
# are never candidates, whatever a pattern above happens to match.
protected() {
	case "$1" in
	nftables | iproute* | systemd* | openssh-server | sudo) return 0 ;;
	esac
	return 1
}

kept() {
	for k in $KEEP; do
		[ "$k" != "$1" ] || return 0
	done
	return 1
}

# MISSING is what actually goes to the package manager. Asking it to
# install what is already there is not harmless: pacman warns about every
# one, and a repair should be quiet on a router with nothing wrong.
MISSING=""
for pkg in $WANT; do
	grep -qxF "$pkg" "$TMP/installed" || MISSING="$MISSING $pkg"
done
MISSING="${MISSING# }"

REMOVE=""
for pattern in $UNWANTED; do
	for pkg in $(matching "$pattern"); do
		protected "$pkg" && continue
		kept "$pkg" && continue
		REMOVE="$REMOVE $pkg"
	done
done
REMOVE="${REMOVE# }"

### the plan

echo "Ostiole will:"
echo "  install:  $WANT"
case "$UPNP_NOTE" in
"no UPnP"*) echo "  $UPNP_NOTE" ;;
*) echo "  UPnP:     $UPNP_NOTE" ;;
esac
if [ "$MANAGER" = apk ]; then
	echo "  place:    $BIN_DIR/ostiole (no systemd here, so no units and no removals)"
else
	echo "  place:    $BIN_DIR/ostiole, write the units, and start the web UI on 443"
	echo "  remove:   ${REMOVE:-nothing this router has that it has no use for}"
	[ -z "$MASK_ONLY" ] || echo "  mask:     $MASK_ONLY"
	echo "  hand addresses to systemd-networkd, keeping the ones this router has now"
	echo "  bootstrap ruleset until the wizard: nothing is forwarded"
fi
if [ "$DRY_RUN" -eq 1 ]; then
	exit 0
fi
if [ "$YES" -ne 1 ]; then
	# stdin is the pipe this script came down, so the question goes to the
	# terminal directly. A router with no terminal has to say --yes.
	if [ -r /dev/tty ]; then
		printf 'proceed? [y/N] '
		read -r answer </dev/tty || answer=""
	else
		echo "not interactive; pass --yes to proceed (curl … | sudo sh -s -- --yes)" >&2
		exit 1
	fi
	case "$answer" in
	y | Y | yes | YES) ;;
	*) echo "aborted" >&2; exit 1 ;;
	esac
fi

### what a router needs

if [ "${OSTIOLE_NO_INSTALL:-}" != "1" ]; then
	echo "installing what a router needs"
	pkg_refresh
	# EPEL carries systemd-networkd on Enterprise Linux, and nothing else
	# in WANT resolves without it.
	if [ "$MANAGER" = dnf ] && [ "$EL" -eq 1 ] && ! grep -qxF epel-release "$TMP/installed"; then
		pkg_install epel-release
	fi
	if [ -n "$MISSING" ]; then
		# shellcheck disable=SC2086 # the list is built to be split
		pkg_install $MISSING
	fi
	# shellcheck disable=SC2086 # the list is built to be split
	pkg_explicit $WANT
fi

# miniupnpd for Enterprise Linux, from Fedora. The RPM's only Fedora-only
# dependency is on the filesystem package, so the binary itself runs
# unchanged; it is unpacked rather than installed for that reason.
fedora_miniupnpd() {
	upnp_path >/dev/null && [ "${OSTIOLE_UPNP_FORCE:-0}" != "1" ] && return 0
	fedora_key "$FEDORA_UPNP" ||
		{ echo "warning: could not import the Fedora $FEDORA_UPNP signing key; no UPnP" >&2; return 0; }
	fedora_dirs "$FEDORA_UPNP" >"$TMP/dirs"
	while IFS= read -r dir; do
		rpm=$(curl -fsSL "$dir" 2>/dev/null |
			sed -n 's/.*href="\(miniupnpd-[^"]*\.'"$RPM_ARCH"'\.rpm\)".*/\1/p' |
			sort -V | tail -1)
		[ -n "$rpm" ] || continue
		curl -fsSL -o "$TMP/miniupnpd.rpm" "$dir$rpm" || continue
		rpm -K "$TMP/miniupnpd.rpm" >/dev/null 2>&1 ||
			{ echo "$rpm from $dir does not verify" >&2; return 1; }
		(cd / && rpm2cpio "$TMP/miniupnpd.rpm" | cpio -idm --quiet)
		bin="$(upnp_path)" || { echo "the Fedora rpm unpacked but left no miniupnpd" >&2; return 1; }
		# A binary that will not link is worse than none: the unit would be
		# written for it and fail on every start.
		if ldd "$bin" 2>&1 | grep -q "not found"; then
			rm -f "$bin"
			echo "warning: $rpm needs libraries this router does not have; no UPnP" >&2
			return 0
		fi
		echo "unpacked $rpm to $bin"
		return 0
	done <"$TMP/dirs"
	echo "warning: nothing served a Fedora $FEDORA_UPNP miniupnpd; no UPnP" >&2
	return 0
}

# fedora_dirs prints the package directories to try for a release: the
# mirrors while it is still supported, and the archive once it is not,
# which is where the older build an Enterprise Linux 9 needs lives.
fedora_dirs() {
	for repo in "updates-released-f$1" "fedora-$1"; do
		curl -fsSL "https://mirrors.fedoraproject.org/mirrorlist?repo=$repo&arch=$RPM_ARCH" 2>/dev/null |
			sed -n 's|^\(https\?://[^ ]*\)$|\1Packages/m/|p' | head -5
	done
	archive="https://archives.fedoraproject.org/pub/archive/fedora/linux"
	echo "$archive/updates/$1/Everything/$RPM_ARCH/Packages/m/"
	echo "$archive/releases/$1/Everything/$RPM_ARCH/os/Packages/m/"
}

# miniupnpd for Arch, which packages it in the AUR only. The PKGBUILD
# comes over TLS, pins its tarball by sha256 and builds the nftables
# backend; makepkg refuses to run as root, so the build runs as nobody.
# Nothing here is fatal: a router without port mapping still routes.
aur_miniupnpd() {
	upnp_path >/dev/null && [ "${OSTIOLE_UPNP_FORCE:-0}" != "1" ] && return 0
	# The toolchain and what this PKGBUILD configures against. They go on
	# as dependencies, so the orphan sweep after the removals takes them.
	if ! pacman -S --noconfirm --needed --asdeps base-devel git lsb-release libcap-ng procps-ng util-linux >/dev/null 2>&1; then
		echo "warning: could not install the tools to build miniupnpd; no UPnP" >&2
		return 0
	fi
	BUILD_TOOLS=1
	AUR_UID="$(id -u nobody 2>/dev/null || true)"
	AUR_GID="$(id -g nobody 2>/dev/null || true)"
	if [ -z "$AUR_UID" ] || [ -z "$AUR_GID" ]; then
		echo "warning: this router has no nobody account to build as; no UPnP" >&2
		return 0
	fi
	build="$(mktemp -d /tmp/ostiole-aur.XXXXXX)"
	AUR_HOME="$build"
	chmod 0755 "$build"
	chown "$AUR_UID:$AUR_GID" "$build"
	if ! as_nobody "$build" "git clone -q --depth 1 https://aur.archlinux.org/$AUR_UPNP.git" ||
		! [ -f "$build/$AUR_UPNP/PKGBUILD" ]; then
		rm -rf "$build"
		echo "warning: could not fetch $AUR_UPNP from the AUR; no UPnP" >&2
		return 0
	fi
	# The PKGBUILD checks upstream's signature as well as the sha256. The
	# key comes from a keyserver; when none answers, the sha256 still pins
	# the source and the build goes on without it.
	skip=""
	key=$(sed -n "s/^validpgpkeys=('\([0-9A-Fa-f]*\)').*/\1/p" "$build/$AUR_UPNP/PKGBUILD")
	if [ -n "$key" ] && ! as_nobody "$build" "gpg --batch --keyserver keyserver.ubuntu.com --recv-keys $key"; then
		skip="--skippgpcheck"
		echo "note: no keyserver answered for the miniupnpd signing key; the sha256 in the PKGBUILD still pins the source"
	fi
	if ! as_nobody "$build/$AUR_UPNP" "makepkg --noconfirm --nodeps $skip"; then
		tail -5 "$TMP/aur.log" >&2
		rm -rf "$build"
		echo "warning: $AUR_UPNP did not build; no UPnP" >&2
		return 0
	fi
	pkg=$(find "$build/$AUR_UPNP" -maxdepth 1 -name "$AUR_UPNP-*.pkg.tar.*" \
		! -name '*.sig' ! -name "$AUR_UPNP-debug-*" | head -1)
	if [ -z "$pkg" ] || ! pacman -U --noconfirm "$pkg" >/dev/null 2>&1; then
		rm -rf "$build"
		echo "warning: the AUR build left nothing to install; no UPnP" >&2
		return 0
	fi
	rm -rf "$build"
	echo "built and installed $AUR_UPNP from the AUR"
}

# as_nobody runs a command in a directory as the unprivileged user makepkg
# insists on. setpriv rather than su, because Arch ships nobody with an
# expired account and su will not have it. One HOME for the whole build,
# so the key gpg imported is the keyring makepkg then reads.
as_nobody() {
	HOME="$AUR_HOME" setpriv --reuid="$AUR_UID" --regid="$AUR_GID" --clear-groups \
		sh -c "cd '$1' && $2" >>"$TMP/aur.log" 2>&1
}

# upnp_path prints where miniupnpd is. Fedora 42 merged /usr/sbin into
# /usr/bin, so the rpm's own path is not where Enterprise Linux keeps its
# daemons.
upnp_path() {
	for p in /usr/sbin/miniupnpd /usr/bin/miniupnpd /usr/local/sbin/miniupnpd; do
		if [ -x "$p" ]; then
			echo "$p"
			return 0
		fi
	done
	return 1
}

# fedora_key imports the release's signing key. The armored per-release
# key is what rpm takes: the bundle at fedoraproject.org/fedora.gpg is
# binary and holds several keys, and rpm reads neither.
fedora_key() {
	url="https://src.fedoraproject.org/rpms/fedora-repos/raw/rawhide/f/RPM-GPG-KEY-fedora-$1-primary"
	curl -fsSL -o "$TMP/fedora-$1.key" "$url" 2>/dev/null || return 1
	rpm --import "$TMP/fedora-$1.key" 2>/dev/null || return 1
}
if [ "${OSTIOLE_NO_INSTALL:-}" != "1" ]; then
	if [ "$FEDORA_UPNP" -ne 0 ]; then
		fedora_miniupnpd
	elif [ "$MANAGER" = pacman ]; then
		aur_miniupnpd
	fi
fi

### the binary

if [ "${OSTIOLE_NO_DOWNLOAD:-}" != "1" ]; then
	VERSION="${OSTIOLE_VERSION:-}"
	if [ -z "$VERSION" ]; then
		VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
		[ -n "$VERSION" ] || { echo "could not determine the latest release" >&2; exit 1; }
	fi
	PLAIN="${VERSION#v}"
	# OSTIOLE_BASE_URL points the download somewhere else: a mirror, an
	# air-gapped file server, or the build CI is testing. It does not weaken
	# anything — the checksum is still checked, and the signature is still
	# checked against the release key built in below.
	BASE="${OSTIOLE_BASE_URL:-https://github.com/$REPO/releases/download/$VERSION}"
	TARBALL="ostiole_${PLAIN}_linux_${ARCH}.tar.gz"

	echo "downloading ostiole $VERSION for linux/$ARCH"
	curl -fsSL -o "$TMP/$TARBALL" "$BASE/$TARBALL"
	curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"
	WANT_SUM="$(grep " $TARBALL\$" "$TMP/checksums.txt" | awk '{print $1}')"
	[ -n "$WANT_SUM" ] || { echo "$TARBALL missing from checksums.txt" >&2; exit 1; }
	if command -v sha256sum >/dev/null 2>&1; then
		GOT="$(sha256sum "$TMP/$TARBALL" | awk '{print $1}')"
	else
		GOT="$(shasum -a 256 "$TMP/$TARBALL" | awk '{print $1}')"
	fi
	[ "$GOT" = "$WANT_SUM" ] || { echo "checksum mismatch for $TARBALL" >&2; exit 1; }

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
		# Quiet, because the usual reason this fails is a 404 and curl's own
		# account of that reads like a broken install rather than a release
		# with nothing to check.
		if ! curl -fsSL -o "$TMP/checksums.txt.sig" "$BASE/checksums.txt.sig" 2>/dev/null; then
			echo "note: no signature published for this release; the checksum was still checked" >&2
			return 0
		fi
		base64 -d <"$TMP/checksums.txt.sig" >"$TMP/sig.bin" 2>/dev/null ||
			{ echo "malformed signature" >&2; exit 1; }
		for key in $OSTIOLE_RELEASE_KEYS; do
			{
				echo "-----BEGIN PUBLIC KEY-----"
				echo "$key"
				echo "-----END PUBLIC KEY-----"
			} >"$TMP/pub.pem"
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
fi

if [ "${OSTIOLE_NO_INSTALL:-}" = "1" ]; then
	echo "skipping the rest (OSTIOLE_NO_INSTALL=1)"
	exit 0
fi
if [ "$MANAGER" = apk ]; then
	echo "no systemd on this router; the binary is in place and no units were written"
	exit 0
fi

### units, ruleset and the network

# shellcheck disable=SC2086 # the extra arguments are meant to be split
"$BIN_DIR/ostiole" install --yes $INSTALL_ARGS

### what a router has no use for

# mask_units masks whatever of a package's units systemd knows.
mask_units() {
	for unit in $(units_for "$1"); do
		systemctl mask --now "$unit" >/dev/null 2>&1 || true
	done
}

for unit in $MASK_ONLY; do
	systemctl mask --now "$unit" >/dev/null 2>&1 || true
done

if [ -n "$REMOVE" ]; then
	echo "removing what a router has no use for: $REMOVE"
	# shellcheck disable=SC2086 # the list is built to be split
	if ! pkg_remove $REMOVE >"$TMP/remove.log" 2>&1; then
		# One transaction is what keeps a dependency from being taken out
		# and put back, but a single refusal — RHEL 10 will not let shim's
		# fwupd go — fails all of it, so the refusals are found one at a
		# time and masked instead.
		for pkg in $REMOVE; do
			if ! pkg_remove "$pkg" >"$TMP/remove.log" 2>&1; then
				echo "warning: $MANAGER will not remove $pkg; masking its units instead" >&2
				mask_units "$pkg"
			fi
		done
	fi
fi
# Unconditional when a toolchain went on for the AUR build: a router has
# no use for a compiler either.
if [ -n "$REMOVE" ] || [ "$BUILD_TOOLS" -eq 1 ]; then
	pkg_autoremove >/dev/null 2>&1 || true
fi
# ufw leaves its own empty iptables-nft tables behind when it goes.
"$BIN_DIR/ostiole" host flush --yes >/dev/null 2>&1 || true

echo "if this session dropped during the handover, reconnect and run: ostiole repair"
