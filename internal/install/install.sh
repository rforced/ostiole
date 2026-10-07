#!/bin/sh
# Turns a Linux host into an Ostiole router: installs what a router needs,
# places the signed binary, runs `ostiole install`, then removes the
# firewalls, network managers and updaters it replaces. Usage:
#   curl -fsSL https://github.com/rforced/ostiole/releases/latest/download/install.sh | sudo sh
#   curl -fsSL … | sudo sh -s -- --yes      (agree to the plan in advance)
#
# Flags: --yes, --dry-run, --with-tailscale, --with-wireless, --with-proxy,
#        --keep <package> (repeatable), --no-verify.
# Anything else is passed to `ostiole install` (--listen, --timezone).
#
# Environment: OSTIOLE_VERSION pins a version; OSTIOLE_BASE_URL downloads
# from somewhere other than the GitHub release; OSTIOLE_NO_INSTALL=1 only
# places the binary; OSTIOLE_NO_DOWNLOAD=1 keeps the binary already in
# place (`ostiole repair`); OSTIOLE_IGNORE_KERNEL=1; OSTIOLE_UPNP_FORCE=1.
set -eu

# The rest of the script is one function, called on the last line, so a
# shell fed it through a pipe has read all of it before anything runs.
# Otherwise a package manager that reads stdin eats the rest of the file
# and the shell resumes parsing wherever it left off.
main() {
REPO="${OSTIOLE_REPO:-rforced/ostiole}"
BIN_DIR="${OSTIOLE_BIN_DIR:-/usr/local/bin}"
# Enterprise Linux packages no miniupnpd and EPEL has no branch for it.
# The Fedora build runs there unchanged; this is where it comes from.
FEDORA_RELEASE=44
# Arch packages miniupnpd in the AUR only, and this is the nftables build.
AUR_UPNP=miniupnpd-nft
# The miniupnpd-nft this release reviewed: the AUR commit, the sha256 of
# every file in it, and the version and paths its package holds.
# scripts/update-aur.sh writes this block.
# BEGIN reviewed miniupnpd-nft
AUR_UPNP_COMMIT=144b6607bcd310e820148263e270aee293ad4205
AUR_UPNP_SUMS='282b459df9d07ae7b93df43f3e6b3c4fe66ffd62a43ff7082884192877f31543  .SRCINFO
488a47ffb9d44284183593573b00878e23ca642ddee0f86839965e4115e694ec  PKGBUILD'
AUR_UPNP_VERSION=2.3.11-1
AUR_UPNP_PATHS='.BUILDINFO
.MTREE
.PKGINFO
etc/
etc/init.d/
etc/init.d/miniupnpd
etc/miniupnpd/
etc/miniupnpd/miniupnpd.conf
etc/miniupnpd/nft_delete_chain.sh
etc/miniupnpd/nft_flush.sh
etc/miniupnpd/nft_init.sh
etc/miniupnpd/nft_removeall.sh
usr/
usr/bin/
usr/bin/miniupnpd
usr/lib/
usr/lib/systemd/
usr/lib/systemd/system/
usr/lib/systemd/system/miniupnpd.service
usr/share/
usr/share/licenses/
usr/share/licenses/miniupnpd/
usr/share/licenses/miniupnpd/LICENSE
usr/share/man/
usr/share/man/man8/
usr/share/man/man8/miniupnpd.8.gz'
# END reviewed miniupnpd-nft
# Set when a toolchain went on to build it, so the sweep runs afterwards
# and takes it away again.
BUILD_TOOLS=0

YES=0
DRY_RUN=0
TAILSCALE=0
WIRELESS=0
PROXY=0
NO_VERIFY=0
KEEP=""
INSTALL_ARGS=""
while [ $# -gt 0 ]; do
	case "$1" in
	-y | --yes) YES=1 ;;
	--dry-run) DRY_RUN=1 ;;
	--with-tailscale) TAILSCALE=1 ;;
	--with-wireless) WIRELESS=1 ;;
	--with-proxy) PROXY=1 ;;
	--no-verify) NO_VERIFY=1 ;;
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

# Ostiole writes and drives systemd units, so a router without systemd is
# refused here rather than most of the way through an install it cannot
# finish. A dry run still prints its plan, and OSTIOLE_NO_INSTALL=1 only
# ever promised a binary, which is what the container tests place.
HAS_SYSTEMD=1
command -v systemctl >/dev/null 2>&1 || HAS_SYSTEMD=0
if [ "$HAS_SYSTEMD" -eq 0 ] && [ "$DRY_RUN" -ne 1 ] && [ "${OSTIOLE_NO_INSTALL:-}" != "1" ]; then
	echo "no systemd on this router; Ostiole writes and drives systemd units" >&2
	exit 1
fi

# Ostiole supports Linux 6.12 and newer: Debian 13, Enterprise Linux 10,
# and every current Ubuntu, Fedora, and Arch. A release string this cannot
# parse is allowed through rather than blocking the install.
kernel_too_old() {
	rel="$1"
	major="${rel%%.*}"
	rest="${rel#*.}"
	if [ "$rest" = "$rel" ]; then return 1; fi
	minor="${rest%%[!0-9]*}"
	case "$major" in '' | *[!0-9]*) return 1 ;; esac
	case "$minor" in '' | *[!0-9]*) return 1 ;; esac
	if [ "$major" -lt 6 ]; then return 0; fi
	if [ "$major" -eq 6 ] && [ "$minor" -lt 12 ]; then return 0; fi
	return 1
}
if kernel_too_old "$(uname -r)"; then
	echo "unsupported kernel: $(uname -r) (Ostiole needs Linux 6.12 or newer)" >&2
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
for m in apt-get dnf pacman; do
	if command -v "$m" >/dev/null 2>&1; then
		MANAGER="$m"
		break
	fi
done
[ -n "$MANAGER" ] || { echo "no supported package manager (apt-get, dnf, pacman)" >&2; exit 1; }

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
# decides: EL 10 is 2.39 and takes the current one. An older release gets
# no UPnP.
FEDORA_UPNP=0
if [ "$EL" -eq 1 ]; then
	case "${OS_VERSION%%.*}" in
	'' | *[!0-9]*) ;;
	*) [ "${OS_VERSION%%.*}" -lt 10 ] || FEDORA_UPNP="$FEDORA_RELEASE" ;;
	esac
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

### the package tables

# Where Tailscale comes from when it is asked for. Debian, Ubuntu and
# Enterprise Linux need Tailscale's own repository; the rest package it.
case "$MANAGER" in
apt-get) TAILSCALE_NOTE="tailscale from pkgs.tailscale.com" ;;
dnf) [ "$EL" -eq 1 ] && TAILSCALE_NOTE="tailscale from pkgs.tailscale.com" || TAILSCALE_NOTE="tailscale" ;;
*) TAILSCALE_NOTE="tailscale" ;;
esac

# Everything the router runs or reads is named, even what the image came
# with: somebody may have taken it away, and only what is missing goes to
# the package manager.
case "$MANAGER" in
apt-get)
	# unbound-anchor is a package of its own here, and without it nothing
	# writes the DNSSEC root key unless the distribution's unbound ran once.
	WANT="nftables dnsmasq unbound unbound-anchor miniupnpd-nftables ppp iproute2 smartmontools openssl chrony ca-certificates tzdata curl tar kmod dbus"
	# Newer Debian and Ubuntu split networkd out of the systemd package.
	if apt-cache show systemd-networkd >/dev/null 2>&1; then
		WANT="$WANT systemd-networkd"
	fi
	UPNP_NOTE="miniupnpd-nftables"
	;;
dnf)
	# curl is whichever build the image has: curl and curl-minimal conflict,
	# and the script came down one of them.
	if [ "$EL" -eq 1 ]; then
		WANT="systemd-networkd nftables dnsmasq unbound ppp iproute iproute-tc cpio smartmontools openssl chrony ca-certificates tzdata tar kmod dbus dnf-plugins-core"
		UPNP_NOTE="miniupnpd unpacked from Fedora $FEDORA_UPNP"
		if [ "$FEDORA_UPNP" -eq 0 ]; then
			UPNP_NOTE="no UPnP: no Fedora build runs on this release"
		fi
	else
		# Fedora splits networkd out of systemd and only recommends it, so a
		# router installed with NetworkManager has nothing to hand
		# addressing to once that goes.
		WANT="systemd-networkd nftables dnsmasq unbound miniupnpd ppp iproute iproute-tc smartmontools openssl chrony ca-certificates tzdata tar kmod dbus dnf5-plugins"
		UPNP_NOTE="miniupnpd"
	fi
	;;
pacman)
	WANT="nftables dnsmasq unbound ppp iproute2 smartmontools openssl chrony ca-certificates tzdata curl tar kmod dbus"
	UPNP_NOTE="miniupnpd-nft $AUR_UPNP_VERSION built from the AUR at $(printf %.7s "$AUR_UPNP_COMMIT") (base-devel goes on to build it)"
	;;
esac

# What a router has no use for, by every name these carry. A pattern is
# matched against the package database, so the names that do not exist on
# this distribution cost nothing.
UNWANTED="bluez firewalld ufw iptables-services iptables-persistent netfilter-persistent shorewall shorewall6
NetworkManager network-manager networkmanager cockpit* netplan.io dhcpcd dhcpcd-base connman ifupdown
unattended-upgrades dnf-automatic yum-cron PackageKit packagekit
snapd ModemManager modemmanager udisks2 upower fwupd multipath-tools device-mapper-multipath lxd-installer
rsyslog syslog-ng apport whoopsie popularity-contest abrt* avahi avahi-daemon geoclue geoclue2 geoclue-2.0 reportbug
ntp ntpsec openntpd ntpd-rs"

# Units of packages a router keeps for something else, so they are masked
# and nothing is removed: the package manager's own timers, and the drive
# monitor, which is wanted for its smartctl command. Ostiole polls the
# drives itself and its warnings go to the dashboard; smartd's would go to
# a mailbox this router has not got. Debian calls the unit
# smartmontools.service with smartd.service as an alias, and masking an
# alias would leave the real one running, so the name is per manager.
case "$MANAGER" in
apt-get) MASK_ONLY="apt-daily.timer apt-daily-upgrade.timer motd-news.timer ua-timer.timer apt-news.service esm-cache.service ubuntu-advantage.service smartmontools.service" ;;
dnf) MASK_ONLY="dnf-makecache.timer smartd.service" ;;
*) MASK_ONLY="smartd.service" ;;
esac

# network_manager says whether a package hands out this router's
# addresses, which a handover not yet confirmed brings back.
network_manager() {
	case "$1" in
	NetworkManager | network-manager | networkmanager | netplan.io | dhcpcd | dhcpcd-base | connman | ifupdown) return 0 ;;
	esac
	return 1
}

# units_for names what systemd calls a package, for the case where the
# manager refuses to remove it and masking is all that is left.
units_for() {
	case "$1" in
	bluez) echo "bluetooth.service" ;;
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
	rsyslog) echo "rsyslog.service" ;;
	syslog-ng) echo "syslog-ng.service syslog-ng@default.service" ;;
	apport) echo "apport.service" ;;
	whoopsie) echo "whoopsie.service whoopsie.path" ;;
	abrt*) echo "abrtd.service abrt-journal-core.service abrt-oops.service abrt-xorg.service abrt-vmcore.service" ;;
	avahi | avahi-daemon) echo "avahi-daemon.service avahi-daemon.socket" ;;
	geoclue | geoclue2 | geoclue-2.0) echo "geoclue.service" ;;
	popularity-contest | reportbug) echo "" ;;
	ntp | ntpsec) echo "ntpd.service ntpsec.service" ;;
	openntpd) echo "openntpd.service" ;;
	ntpd-rs) echo "ntpd-rs.service" ;;
	*) echo "" ;;
	esac
}

# mask_units masks whatever of a package's units systemd knows.
mask_units() {
	for unit in $(units_for "$1"); do
		systemctl mask --now "$unit" >/dev/null 2>&1 || true
	done
}

### the package manager

# pkg_dnf is dnf as every call here runs it. countme=0: the counter is a
# weekly ping that tells the mirror how many routers of this age exist,
# and a router that tells nobody it is here tells nobody that either.
# timeout=10: a mirror that sends under 1 kB/s for 10 s is left for the
# next, where dnf waits 30. The mirror list comes shuffled, and one Rocky
# mirror takes 20 s to answer each request, which kept makecache at it
# for nine minutes.
pkg_dnf() { dnf '--setopt=*.countme=0' --setopt=timeout=10 "$@"; }

# On dnf a refresh is only noted, and the next install makes it. dnf 4's
# makecache fetches every repository's metadata again however fresh it
# is, and Rocky's mirrors do not all serve the same revision, so a repair
# with nothing to install could spend minutes downloading all of it. A
# run that installs nothing asks no mirror anything.
DNF_REFRESH=0

pkg_refresh() {
	case "$MANAGER" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get update -qq ;;
	dnf) DNF_REFRESH=1 ;;
	pacman) pacman -Sy --noconfirm >/dev/null ;;
	esac
}

pkg_install() {
	case "$MANAGER" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get install -y -q "$@" ;;
	dnf)
		if [ "$DNF_REFRESH" -eq 1 ]; then
			DNF_REFRESH=0
			pkg_dnf -q makecache >/dev/null 2>&1 || true
		fi
		pkg_dnf -y install "$@"
		;;
	pacman) pacman -S --noconfirm --needed "$@" ;;
	esac
}

pkg_remove() {
	case "$MANAGER" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get remove --purge -y -q "$@" ;;
	dnf) pkg_dnf -y remove "$@" ;;
	pacman) pacman -Rns --noconfirm "$@" ;;
	esac
}

# pkg_explicit marks the router's own packages as wanted in their own
# right, so the autoremove that follows the removals cannot take one that
# only arrived as somebody else's dependency.
#
# Every one of these answers for itself and reads nothing: dnf5 prints a
# transaction for a reason it is about to change and asks to confirm it,
# and a question asked with the output on /dev/null is an install that
# hangs with nothing on the screen.
pkg_explicit() {
	case "$MANAGER" in
	apt-get) apt-mark manual "$@" </dev/null >/dev/null 2>&1 || true ;;
	dnf) pkg_dnf -y mark user "$@" </dev/null >/dev/null 2>&1 || pkg_dnf -y mark install "$@" </dev/null >/dev/null 2>&1 || true ;;
	pacman) pacman -D --asexplicit "$@" </dev/null >/dev/null 2>&1 || true ;;
	esac
}

pkg_autoremove() {
	case "$MANAGER" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get autoremove --purge -y -q && apt-get clean ;;
	dnf) pkg_dnf -y autoremove && pkg_dnf clean all >/dev/null ;;
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
dnf) rpm -qa --qf '%{NAME}\n' 2>/dev/null >"$TMP/installed" ;;
pacman) pacman -Qq 2>/dev/null >"$TMP/installed" ;;
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

# remove_packages takes the packages off in one transaction, which is what
# keeps a dependency from being taken out and put back. A single refusal
# fails all of it, as RHEL 10 will not let shim's fwupd go, so then the
# refusals are found one at a time and masked instead.
remove_packages() {
	pkg_remove "$@" >"$TMP/remove.log" 2>&1 && return 0
	for pkg in "$@"; do
		if ! pkg_remove "$pkg" >"$TMP/remove.log" 2>&1; then
			echo "warning: $MANAGER will not remove $pkg; masking its units instead" >&2
			mask_units "$pkg"
		fi
	done
}

# A router that already has Tailscale keeps it through a plain repair, so
# its repository is left alone and the flag stays the only way in.
[ -z "$(matching tailscale)" ] || TAILSCALE=1

# The proxy is sticky the same way, on the binary rather than a package:
# nothing packages this combination, so we are the only ones who put it
# there.
command -v ostiole-proxy >/dev/null 2>&1 && PROXY=1
[ ! -x "$BIN_DIR/ostiole-proxy" ] || PROXY=1
[ ! -x /usr/local/sbin/ostiole-proxy ] || PROXY=1

# A card the kernel already drives, a PCI device that says network
# controller (0x028000), or hostapd already here: any of them means this
# router serves wifi, so a plain repair keeps it.
[ ! -d /sys/class/ieee80211 ] || [ -z "$(ls -A /sys/class/ieee80211 2>/dev/null)" ] || WIRELESS=1
[ -z "$(matching hostapd)" ] || WIRELESS=1
WIFI_VENDORS=""
for class in /sys/bus/pci/devices/*/class; do
	[ -r "$class" ] || continue
	[ "$(cat "$class")" = "0x028000" ] || continue
	WIRELESS=1
	vendor="$(cat "${class%class}vendor" 2>/dev/null)" || continue
	case " $WIFI_VENDORS " in *" ${vendor#0x} "*) ;; *) WIFI_VENDORS="$WIFI_VENDORS ${vendor#0x}" ;; esac
done
WIFI_VENDORS="${WIFI_VENDORS# }"

# wifi_firmware names the packages a distribution splits a vendor's
# firmware into. An unknown vendor gets the whole blob.
wifi_firmware() {
	case "$MANAGER" in
	dnf)
		# Enterprise Linux 10 splits by vendor as Fedora does, and its blob
		# only recommends the other vendors' packages.
		case "$1" in
		# Fedora keeps the newest Intel cards' firmware apart, in a package
		# Enterprise Linux 10 does not have.
		8086) if [ "$EL" -eq 1 ]; then echo iwlwifi-mvm-firmware; else echo iwlwifi-mvm-firmware iwlwifi-mld-firmware; fi ;;
		168c | 17cb) echo atheros-firmware ;;
		14c3) echo mt7xxx-firmware ;;
		10ec) echo realtek-firmware ;;
		14e4) echo brcmfmac-firmware ;;
		*) echo linux-firmware ;;
		esac
		;;
	apt-get)
		[ "$OS_ID" = debian ] || { echo linux-firmware; return; }
		case "$1" in
		8086) echo firmware-iwlwifi ;;
		168c | 17cb) echo firmware-atheros ;;
		14c3) echo firmware-mediatek ;;
		10ec) echo firmware-realtek ;;
		14e4) echo firmware-brcm80211 ;;
		*) echo firmware-misc-nonfree ;;
		esac
		;;
	*) echo linux-firmware ;;
	esac
}

# The firmware is kept out of WANT: it may live in a component this
# router has not enabled, and a card nobody can drive is a warning, not a
# reason to leave the install half done.
WIRELESS_FW=""
if [ "$WIRELESS" -eq 1 ]; then
	WANT="$WANT hostapd iw wireless-regdb"
	for v in $WIFI_VENDORS; do
		for fw in $(wifi_firmware "$v"); do
			case " $WIRELESS_FW " in *" $fw "*) ;; *) WIRELESS_FW="$WIRELESS_FW $fw" ;; esac
		done
	done
	WIRELESS_FW="${WIRELESS_FW# }"
fi

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

# What the install left while a network handover could bring it back:
# `ostiole takeover --network --confirm` runs this part alone, with those
# packages in OSTIOLE_REMOVE, once nothing can.
if [ -n "${OSTIOLE_REMOVE:-}" ]; then
	LEFT=""
	for pkg in $OSTIOLE_REMOVE; do
		if grep -qxF "$pkg" "$TMP/installed"; then
			LEFT="$LEFT $pkg"
		fi
	done
	if [ -n "$LEFT" ]; then
		echo "removing what the install left until the network handover was confirmed:$LEFT"
		# shellcheck disable=SC2086 # the list is built to be split
		remove_packages $LEFT
		pkg_autoremove >/dev/null 2>&1 || true
	fi
	exit 0
fi

### the plan

echo "Ostiole will:"
echo "  install:  $WANT"
case "$UPNP_NOTE" in
"no UPnP"*) echo "  $UPNP_NOTE" ;;
*) echo "  UPnP:     $UPNP_NOTE" ;;
esac
[ "$TAILSCALE" -eq 0 ] || echo "  tailscale: $TAILSCALE_NOTE"
[ "$WIRELESS" -eq 0 ] || echo "  wireless: hostapd iw wireless-regdb${WIRELESS_FW:+ $WIRELESS_FW}"
[ "$PROXY" -eq 0 ] || echo "  proxy:    ostiole-proxy${OSTIOLE_VERSION:+ $OSTIOLE_VERSION} beside the binary"
echo "  place:    $BIN_DIR/ostiole, write the units, and start the web UI (on 9443 unless it runs already)"
[ "$NO_VERIFY" -eq 0 ] || echo "  skip:     the release signature check (--no-verify)"
case "$REMOVE" in
*rsyslog* | *syslog-ng*) echo "  remove:   $REMOVE (and the /var/log files it wrote)" ;;
*) echo "  remove:   ${REMOVE:-nothing this router has that it has no use for}" ;;
esac
[ -z "$MASK_ONLY" ] || echo "  mask:     $MASK_ONLY"
echo "  hand addresses to systemd-networkd, keeping the ones this router has now"
echo "  keep the time with chrony in place of the distribution's time service"
echo "  bootstrap ruleset until the wizard: nothing is forwarded"
[ "$HAS_SYSTEMD" -eq 1 ] ||
	echo "  none of it here: this router has no systemd, and an install would be refused"
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
	for fw in $WIRELESS_FW; do
		grep -qxF "$fw" "$TMP/installed" && continue
		if pkg_install "$fw"; then
			pkg_explicit "$fw"
		else
			echo "warning: $fw is not available here; a card that needs it will not come up" >&2
		fi
	done
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
# mirrors while it is still supported, and the archive once it is not.
fedora_dirs() {
	for repo in "updates-released-f$1" "fedora-$1"; do
		curl -fsSL "https://mirrors.fedoraproject.org/mirrorlist?repo=$repo&arch=$RPM_ARCH" 2>/dev/null |
			sed -n 's|^\(https\?://[^ ]*\)$|\1Packages/m/|p' | head -5
	done
	archive="https://archives.fedoraproject.org/pub/archive/fedora/linux"
	echo "$archive/updates/$1/Everything/$RPM_ARCH/Packages/m/"
	echo "$archive/releases/$1/Everything/$RPM_ARCH/os/Packages/m/"
}

# miniupnpd for Arch, which packages it in the AUR only. Nothing is taken
# on the AUR's word: the build uses the commit this release reviewed and
# stops if a file in it differs, the source must carry miniupnp's
# signature, and the package goes on only if it holds what the review
# built. makepkg refuses to run as root, so the build runs as an account
# made for it and removed after: nothing else runs as that account, so
# nothing can reach into the build between the review and makepkg, as
# anything running as nobody could into a build run as nobody.
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
	if ! aur_account; then
		echo "warning: could not make an account to build miniupnpd as; no UPnP" >&2
		return 0
	fi
	build="$(build_dir)" || {
		aur_done
		echo "warning: nowhere to build miniupnpd that allows running a configure script; no UPnP" >&2
		return 0
	}
	AUR_HOME="$build"
	chmod 0755 "$build"
	chown "$AUR_UID:$AUR_GID" "$build"
	if ! aur_fetch "https://aur.archlinux.org/$AUR_UPNP.git"; then
		echo "note: could not fetch $AUR_UPNP from the AUR; trying Arch's mirror of it on GitHub"
		if ! aur_fetch https://github.com/archlinux/aur.git; then
			tail -5 "$TMP/aur.log" >&2
			aur_done
			echo "warning: could not fetch $AUR_UPNP from the AUR or its GitHub mirror; no UPnP" >&2
			return 0
		fi
	fi
	if ! aur_reviewed "$build/$AUR_UPNP"; then
		aur_done
		echo "warning: $AUR_UPNP at $(printf %.7s "$AUR_UPNP_COMMIT") is not the one this release reviewed; no UPnP" >&2
		return 0
	fi
	# The key goes into the build's own keyring, where makepkg checks the
	# tarball's signature against the key the PKGBUILD names.
	if ! printf '%s\n' "$AUR_UPNP_KEY" | as_builder "$build" "gpg --batch --import && gpg --batch --list-keys $AUR_UPNP_KEY_FPR"; then
		tail -5 "$TMP/aur.log" >&2
		aur_done
		echo "warning: could not load miniupnp's signing key; no UPnP" >&2
		return 0
	fi
	if ! as_builder "$build/$AUR_UPNP" "makepkg --noconfirm --nodeps"; then
		tail -5 "$TMP/aur.log" >&2
		aur_done
		echo "warning: $AUR_UPNP did not build; no UPnP" >&2
		return 0
	fi
	pkg=$(find "$build/$AUR_UPNP" -maxdepth 1 -name "$AUR_UPNP-*.pkg.tar.*" \
		! -name '*.sig' ! -name "$AUR_UPNP-debug-*" | head -1)
	if [ -z "$pkg" ]; then
		aur_done
		echo "warning: the AUR build left nothing to install; no UPnP" >&2
		return 0
	fi
	# pacman runs a package's install script as root, so a path the review
	# did not see, .INSTALL above all, stops it here.
	bsdtar -tf "$pkg" | LC_ALL=C sort >"$TMP/aur.paths"
	bsdtar -xOf "$pkg" .PKGINFO >"$TMP/aur.pkginfo" 2>/dev/null || true
	if ! grep -qx "pkgname = $AUR_UPNP" "$TMP/aur.pkginfo" ||
		! grep -qx "pkgver = $AUR_UPNP_VERSION" "$TMP/aur.pkginfo" ||
		[ "$(cat "$TMP/aur.paths")" != "$AUR_UPNP_PATHS" ]; then
		printf '%s\n' "$AUR_UPNP_PATHS" | LC_ALL=C comm -3 - "$TMP/aur.paths" >&2
		aur_done
		echo "warning: the $AUR_UPNP package is not the one this release reviewed; no UPnP" >&2
		return 0
	fi
	if ! pacman -U --noconfirm "$pkg" >/dev/null 2>&1; then
		aur_done
		echo "warning: pacman would not install the $AUR_UPNP it built; no UPnP" >&2
		return 0
	fi
	aur_done
	echo "built and installed $AUR_UPNP $AUR_UPNP_VERSION from the AUR"
}

# aur_account makes the account the build runs as, fresh: one left by a
# build that died goes first, along with anything still running as it.
AUR_USER=ostiole-build
aur_account() {
	aur_gone
	useradd --system --no-create-home --home-dir / --shell /usr/bin/nologin --user-group "$AUR_USER" >>"$TMP/aur.log" 2>&1 ||
		return 1
	AUR_UID="$(id -u "$AUR_USER")" && AUR_GID="$(id -g "$AUR_USER")"
}

# aur_done removes the build and the account it ran as.
aur_done() {
	rm -rf "${build:?}"
	aur_gone
}

# aur_gone stops whatever runs as the build account, gpg's agent above
# all, and removes the account with its group.
aur_gone() {
	id -u "$AUR_USER" >/dev/null 2>&1 || return 0
	pkill -KILL -u "$AUR_USER" 2>/dev/null || true
	userdel "$AUR_USER" >>"$TMP/aur.log" 2>&1 || true
}

# miniupnp's release key, which signs every miniupnpd tarball. It travels
# here so no keyserver is asked; a signature by any other key, or none,
# stops the build.
AUR_UPNP_KEY_FPR=751E9FF6944A3B36A5432216DB511043A31ACAAF # gitleaks:allow (key fingerprint)
AUR_UPNP_KEY='-----BEGIN PGP PUBLIC KEY BLOCK-----

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
QP3d
=bCg1
-----END PGP PUBLIC KEY BLOCK-----'

# build_dir prints a temporary directory a build can run binaries in, and
# proves it rather than assuming: /tmp is noexec on a hardened router and
# on any container whose runtime mounts it that way, and a configure
# script has to be executable where it was unpacked.
build_dir() {
	for base in /var/tmp /tmp; do
		[ -d "$base" ] || continue
		dir="$(mktemp -d "$base/ostiole-aur.XXXXXX" 2>/dev/null)" || continue
		printf '#!/bin/sh\n' >"$dir/probe"
		chmod 0755 "$dir/probe"
		if "$dir/probe" 2>/dev/null; then
			rm -f "$dir/probe"
			echo "$dir"
			return 0
		fi
		rm -rf "$dir"
	done
	return 1
}

# as_builder runs a command in a directory as the build account, which
# makepkg insists is not root. setpriv rather than su, which will not have
# an account with no shell. One HOME for the whole build, so the key gpg
# imported is the keyring makepkg then reads.
as_builder() {
	HOME="$AUR_HOME" setpriv --reuid="$AUR_UID" --regid="$AUR_GID" --clear-groups \
		sh -c "cd '$1' && $2" >>"$TMP/aur.log" 2>&1
}

# aur_fetch fetches the reviewed commit into the build directory by its
# hash, so the AUR moving on changes nothing here. Arch's GitHub mirror
# keeps each package on a branch of its own, with the same commits. A
# failed try leaves a repository behind, so an earlier one goes first.
aur_fetch() {
	rm -rf "${build:?}/$AUR_UPNP"
	as_builder "$build" "git init -q $AUR_UPNP && cd $AUR_UPNP && git fetch -q --depth 1 $1 $AUR_UPNP_COMMIT && git checkout -q FETCH_HEAD"
}

# aur_reviewed says whether a checkout holds exactly the files the review
# hashed, no more and no fewer.
aur_reviewed() {
	(
		cd "$1" || exit 1
		[ "$(find . -path ./.git -prune -o ! -type d -print | sed 's|^\./||' | LC_ALL=C sort)" = \
			"$(printf '%s\n' "$AUR_UPNP_SUMS" | sed 's/^[0-9a-f]*  //' | LC_ALL=C sort)" ] &&
			printf '%s\n' "$AUR_UPNP_SUMS" | sha256sum -c --quiet --strict - >/dev/null 2>&1
	)
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
# binary and holds only the releases still supported, and rpm reads
# neither. dist-git is the key's home and serves 500 for hours at a
# time, so rpm's own copy of the same file is the second try.
fedora_key() {
	for url in \
		"https://src.fedoraproject.org/rpms/fedora-repos/raw/rawhide/f/RPM-GPG-KEY-fedora-$1-primary" \
		"https://raw.githubusercontent.com/rpm-software-management/distribution-gpg-keys/main/keys/fedora/RPM-GPG-KEY-fedora-$1-primary"; do
		curl -fsSL --max-time 30 -o "$TMP/fedora-$1.key" "$url" 2>/dev/null || continue
		rpm --import "$TMP/fedora-$1.key" 2>/dev/null && return 0
	done
	return 1
}
# Tailscale, only when it was asked for: a router that never wanted a
# tailnet has no reason to poll a third-party repository on every update
# check. Nothing here is fatal; a router without it still routes.
tailscale_install() {
	command -v tailscaled >/dev/null 2>&1 && return 0
	case "$MANAGER" in
	apt-get)
		distro="$OS_ID"
		case "$distro" in
		ubuntu | debian) ;;
		*)
			distro=""
			for d in $OS_LIKE; do
				case "$d" in ubuntu | debian) distro="$d"; break ;; esac
			done
			;;
		esac
		[ -n "$distro" ] || { echo "warning: no Tailscale repository for $OS_ID; no Tailscale" >&2; return 0; }
		# shellcheck source=/dev/null
		codename="$(. /etc/os-release 2>/dev/null; echo "${VERSION_CODENAME:-}")"
		[ -n "$codename" ] || codename="$(tailscale_fallback "$distro")"
		if ! tailscale_apt_repo "$distro" "$codename"; then
			codename="$(tailscale_fallback "$distro")"
			echo "note: pkgs.tailscale.com serves no $OS_ID release by that name; using $codename, which carries the same packages"
			tailscale_apt_repo "$distro" "$codename" ||
				{ echo "warning: could not add Tailscale's repository; no Tailscale" >&2; return 0; }
		fi
		pkg_refresh
		pkg_install tailscale || { echo "warning: tailscale did not install; no Tailscale" >&2; return 0; }
		;;
	dnf)
		if [ "$EL" -eq 1 ]; then
			# The repository file carries gpgkey=, and dnf imports it.
			curl -fsSL -o /etc/yum.repos.d/tailscale.repo \
				"https://pkgs.tailscale.com/stable/rhel/${OS_VERSION%%.*}/tailscale.repo" ||
				{ echo "warning: could not fetch Tailscale's repository; no Tailscale" >&2; return 0; }
		fi
		pkg_install tailscale || { echo "warning: tailscale did not install; no Tailscale" >&2; return 0; }
		;;
	*)
		pkg_install tailscale || { echo "warning: tailscale did not install; no Tailscale" >&2; return 0; }
		;;
	esac
	pkg_explicit tailscale
}

# tailscale_apt_repo places the keyring and the source list for one
# release, and fails without leaving either behind when there is no such
# release upstream.
tailscale_apt_repo() {
	base="https://pkgs.tailscale.com/stable/$1"
	curl -fsSL -o "$TMP/tailscale.gpg" "$base/$2.noarmor.gpg" || return 1
	curl -fsSL -o "$TMP/tailscale.list" "$base/$2.tailscale-keyring.list" || return 1
	install -m 0644 "$TMP/tailscale.gpg" /usr/share/keyrings/tailscale-archive-keyring.gpg
	install -m 0644 "$TMP/tailscale.list" /etc/apt/sources.list.d/tailscale.list
}

# The release to fall back on when this one has no repository of its own.
# They carry the same packages.
tailscale_fallback() {
	case "$1" in
	debian) echo bookworm ;;
	*) echo noble ;;
	esac
}

if [ "${OSTIOLE_NO_INSTALL:-}" != "1" ]; then
	if [ "$FEDORA_UPNP" -ne 0 ]; then
		fedora_miniupnpd
	elif [ "$MANAGER" = pacman ]; then
		aur_miniupnpd
	fi
	[ "$TAILSCALE" -eq 0 ] || tailscale_install
fi

### the binary

# The release everything here is fetched from. A repair has the binary
# already and passes its own version in, so the sidecar can still be
# fetched to match it; only an install with nothing to go on asks GitHub.
VERSION="${OSTIOLE_VERSION:-}"
if [ -z "$VERSION" ] && [ "${OSTIOLE_NO_DOWNLOAD:-}" != "1" ]; then
	VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
	[ -n "$VERSION" ] || { echo "could not determine the latest release" >&2; exit 1; }
fi
# A release is tagged v0.14.0 and a release binary says 0.14.0; either
# arrives here, and the download URL wants the tag.
[ -z "$VERSION" ] || VERSION="v${VERSION#v}"
PLAIN="${VERSION#v}"
# OSTIOLE_BASE_URL points the download somewhere else: a mirror, an
# air-gapped file server, or the build CI is testing. It does not weaken
# anything — the checksum is still checked, and the signature is still
# checked against the release key built in below.
BASE="${OSTIOLE_BASE_URL:-https://github.com/$REPO/releases/download/$VERSION}"
# The checksums file is signed with the release ed25519 key, the same one
# the built-in updater trusts. Verifying it here means a tampered
# checksums.txt cannot hand you a tampered tarball. Whatever stops the
# check stops the install: deleting the signature is the first thing
# somebody able to change the release assets would do. --no-verify skips
# it. Rotation: add the new key to the list, keep the old one for a
# release.
OSTIOLE_RELEASE_KEYS="MCowBQYDK2VwAyEA4a3rf0bCdQNTKO3KODxqMrdT1+T1nq9t+KNN2f9DJ0U=" # gitleaks:allow (public key)
verify_signature() {
	if [ "$NO_VERIFY" -eq 1 ]; then
		echo "note: --no-verify: the signature is not checked, the checksum still is" >&2
		return 0
	fi
	if ! command -v openssl >/dev/null 2>&1 || ! command -v base64 >/dev/null 2>&1; then
		echo "openssl and base64 are needed to check the release signature; install them or pass --no-verify" >&2
		exit 1
	fi
	# Quiet, because curl's account of a 404 says less than this does.
	if ! curl -fsSL -o "$TMP/checksums.txt.sig" "$BASE/checksums.txt.sig" 2>/dev/null; then
		echo "no signature at $BASE/checksums.txt.sig; refusing the release (--no-verify installs it anyway)" >&2
		exit 1
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
			echo "this openssl cannot check an ed25519 signature; install OpenSSL 3 or pass --no-verify" >&2
			exit 1
		fi
	done
	echo "signature check failed for checksums.txt" >&2
	exit 1
}

# One verified checksums.txt covers every tarball of the release, so
# it is fetched once and each download is checked against it.
verify_release_checksums() {
	[ ! -f "$TMP/checksums.txt" ] || return 0
	# Not a missing signature, and not a reason to turn the check off.
	if ! curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"; then
		rm -f "$TMP/checksums.txt"
		echo "could not fetch $BASE/checksums.txt" >&2
		return 1
	fi
	verify_signature
}

# fetch_release_binary <name> downloads that tarball, checks its sum
# against the verified list, and installs the one member it carries.
fetch_release_binary() {
	name="$1"
	tarball="${name}_${PLAIN}_linux_${ARCH}.tar.gz"
	verify_release_checksums || return 1
	want="$(grep " $tarball\$" "$TMP/checksums.txt" | awk '{print $1}')"
	[ -n "$want" ] || { echo "$tarball missing from checksums.txt" >&2; return 1; }
	curl -fsSL -o "$TMP/$tarball" "$BASE/$tarball" || return 1
	if command -v sha256sum >/dev/null 2>&1; then
		got="$(sha256sum "$TMP/$tarball" | awk '{print $1}')"
	else
		got="$(shasum -a 256 "$TMP/$tarball" | awk '{print $1}')"
	fi
	[ "$got" = "$want" ] || { echo "checksum mismatch for $tarball" >&2; exit 1; }
	tar -xzf "$TMP/$tarball" -C "$TMP" "$name"
	install -m 0755 "$TMP/$name" "$BIN_DIR/$name"
}

if [ "${OSTIOLE_NO_DOWNLOAD:-}" != "1" ]; then
	echo "downloading ostiole $VERSION for linux/$ARCH"
	fetch_release_binary ostiole || exit 1
	echo "installed $BIN_DIR/ostiole ($("$BIN_DIR/ostiole" version))"
fi

# proxy_install places the reverse proxy sidecar beside the binary. It is
# opt-in, and a release without the asset is a warning rather than a
# failed install: the rest of the router is fine without it.
proxy_install() {
	[ "$PROXY" -eq 1 ] || return 0
	if [ -z "$VERSION" ]; then
		echo "note: no release to fetch ostiole-proxy from; build it and put it in $BIN_DIR" >&2
		return 0
	fi
	have="$("$BIN_DIR/ostiole-proxy" release 2>/dev/null | tail -n 1)"
	if [ "${have#v}" = "$PLAIN" ]; then
		echo "ostiole-proxy $VERSION already in place"
		return 0
	fi
	echo "downloading ostiole-proxy $VERSION for linux/$ARCH"
	if fetch_release_binary ostiole-proxy; then
		echo "installed $BIN_DIR/ostiole-proxy"
	else
		echo "note: no ostiole-proxy in $VERSION; build it and put it in $BIN_DIR/ostiole-proxy" >&2
	fi
}
proxy_install

if [ "${OSTIOLE_NO_INSTALL:-}" = "1" ]; then
	echo "skipping the rest (OSTIOLE_NO_INSTALL=1)"
	exit 0
fi
### units and ruleset

# The network is handed over at the very end, below.
# shellcheck disable=SC2086 # the extra arguments are meant to be split
"$BIN_DIR/ostiole" install --yes --network-later $INSTALL_ARGS

### what a router has no use for

for unit in $MASK_ONLY; do
	systemctl mask --now "$unit" >/dev/null 2>&1 || true
done
# Ubuntu's message of the day fetches a news feed over the network on
# every login. The timer is masked above; this stops the script the
# package still runs from asking.
if [ -f /etc/default/motd-news ]; then
	sed -i "s/^ENABLED=.*/ENABLED=0/" /etc/default/motd-news 2>/dev/null || true
fi
if command -v pro >/dev/null 2>&1; then
	pro config set apt_news=false >/dev/null 2>&1 || true
fi

# The old network managers stay for the handover at the end: until it
# holds, a revert needs one to bring back.
NETWORK_PKGS=""
REST=""
for pkg in $REMOVE; do
	if network_manager "$pkg"; then
		NETWORK_PKGS="$NETWORK_PKGS $pkg"
	else
		REST="$REST $pkg"
	fi
done
REMOVE="${REST# }"
NETWORK_PKGS="${NETWORK_PKGS# }"
if [ -n "$REMOVE" ]; then
	echo "removing what a router has no use for: $REMOVE"
	# shellcheck disable=SC2086 # the list is built to be split
	remove_packages $REMOVE
fi
# The journal is the only log this router keeps, so the files the second
# copy left behind go with the package that wrote them. Only when it
# really went: a removal the manager refused was masked instead, and the
# daemon would write them again. Nothing else in /var/log is touched.
case "$REMOVE" in
*rsyslog* | *syslog-ng*)
	if ! command -v rsyslogd >/dev/null 2>&1 && ! command -v syslog-ng >/dev/null 2>&1; then
		for f in syslog auth.log kern.log daemon.log user.log messages debug \
			mail.log mail.info mail.warn mail.err secure maillog cron spooler boot.log; do
			rm -f "/var/log/$f" "/var/log/$f".* "/var/log/$f"-* 2>/dev/null || true
		done
	fi
	;;
esac
# Unconditional when a toolchain went on for the AUR build: a router has
# no use for a compiler either.
if [ -n "$REMOVE" ] || [ "$BUILD_TOOLS" -eq 1 ]; then
	pkg_autoremove >/dev/null 2>&1 || true
fi
# ufw leaves its own empty iptables-nft tables behind when it goes.
"$BIN_DIR/ostiole" host flush --yes >/dev/null 2>&1 || true

### the network, last

# The handover is the one step that can cut this session off, so nothing
# else waits on it. Once it holds, it removes the old network managers:
# by itself when every address comes back, or when the next session
# confirms it.
echo "handing the network to systemd-networkd; if this session drops, reconnect within three minutes and run \`ostiole takeover --network --confirm\`, or the previous manager comes back on its own"
if [ -n "$NETWORK_PKGS" ]; then
	"$BIN_DIR/ostiole" takeover --network --for-install --remove-on-confirm "$(printf '%s' "$NETWORK_PKGS" | tr ' ' ',')"
else
	"$BIN_DIR/ostiole" takeover --network --for-install
fi
}

main "$@"
