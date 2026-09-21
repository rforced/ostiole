#!/bin/sh
# Installs Ostiole for real, the way the documentation says to, into a
# stock image running systemd, and then asks the router whether every
# promise the installer printed came true.
#
#   install-systemd-test.sh <base-url> <version>
#
# Nothing is preinstalled: the test is the official image, the documented
# one-liner, and the end state. It runs twice, because `ostiole repair`
# has to reach that same state on a router somebody has since changed.
set -eu
# shellcheck source=scripts/ci/lib.sh
. "$(dirname "$0")/lib.sh"

BASE="${1:?base url}"
VERSION="${2:?version}"

# upnp_package is what miniupnpd is packaged as here, empty when it does
# not come from the package manager at all.
case "$(manager)" in
apt-get)
	tc_package=iproute2
	upnp_package=miniupnpd-nftables
	;;
dnf)
	tc_package=iproute-tc
	upnp_package=miniupnpd
	case "$(distro_id)" in
	# Unpacked from a Fedora rpm rather than installed.
	rocky | rhel | almalinux | centos) upnp_package="" ;;
	esac
	;;
pacman)
	tc_package=iproute2
	upnp_package=miniupnpd-nft # built from the AUR, but pacman knows it
	;;
*) fail "no package manager, or one with no systemd" ;;
esac

# assert_router is the whole end state, asked twice: once of the install
# and once of the repair that has to reproduce it.
assert_router() {
	step "$1: the units are running"
	for unit in ostiole-firewall.service ostiole.service; do
		state=$(systemctl is-enabled "$unit" 2>&1 || true)
		[ "$state" = enabled ] || fail "$unit is $state, not enabled"
	done
	systemctl is-active ostiole.service >/dev/null ||
		fail "ostiole.service is $(systemctl is-active ostiole.service 2>&1): $(systemctl status ostiole.service --no-pager -l | tail -20)"

	step "$1: networkd has the network and NetworkManager cannot start"
	# Enabled and active is as far as a container goes: the address on
	# eth0 belongs to the container runtime, so networkd never takes it
	# over in here. That addresses survive the handover is checked on the
	# VM, where the router owns its own interface.
	systemctl is-enabled systemd-networkd.service >/dev/null 2>&1 ||
		fail "systemd-networkd is $(systemctl is-enabled systemd-networkd.service 2>&1), not enabled"
	systemctl is-active systemd-networkd.service >/dev/null 2>&1 ||
		fail "systemd-networkd is $(systemctl is-active systemd-networkd.service 2>&1), not active"
	# Older systemd says "No such file or directory" where newer says
	# not-found; both mean the unit is gone.
	state=$(systemctl is-enabled NetworkManager.service 2>&1 || true)
	case "$state" in
	masked | not-found | *"No such file"*) ;;
	*) fail "NetworkManager.service is $state, want masked or gone" ;;
	esac

	step "$1: the packages a router needs went on"
	for pkg in nftables dnsmasq unbound ppp "$tc_package" $upnp_package; do
		pkg_present "$pkg" || fail "$pkg was not installed"
	done
	# However it got here, it has to be a binary this router can run and
	# the nftables build: the iptables one would write mappings into
	# tables Ostiole's chains never see.
	bin=$(command -v miniupnpd || PATH="$PATH:/usr/sbin:/sbin" command -v miniupnpd) ||
		fail "miniupnpd is not on this router"
	! ldd "$bin" 2>&1 | grep -q "not found" ||
		fail "miniupnpd is missing libraries: $(ldd "$bin" 2>&1 | grep 'not found')"
	ldd "$bin" 2>&1 | grep -q libnftnl ||
		fail "miniupnpd is not the nftables build: $(ldd "$bin" 2>&1)"
	for cmd in nft dnsmasq unbound pppd tc; do
		command -v "$cmd" >/dev/null 2>&1 || PATH="$PATH:/usr/sbin:/sbin" command -v "$cmd" >/dev/null 2>&1 ||
			fail "$cmd is not on this router"
	done

	step "$1: the service units were written"
	units="ostiole-dnsmasq.service ostiole-unbound.service ostiole-pppoe@.service ostiole-miniupnpd.service"
	for unit in $units; do
		systemctl cat "$unit" >/dev/null 2>&1 || fail "$unit was not written"
	done

	step "$1: the packages a router has no use for are not there"
	for pkg in bluez firewalld ufw NetworkManager network-manager networkmanager unattended-upgrades PackageKit packagekit cockpit cockpit-ws; do
		! pkg_present "$pkg" || fail "$pkg is installed"
	done

	step "$1: Bluetooth is blocked"
	[ -f /etc/modprobe.d/ostiole-bluetooth.conf ] || fail "the Bluetooth block was not written"

	step "$1: the bootstrap ruleset is in the kernel"
	PATH="$PATH:/usr/sbin:/sbin"
	nft list table inet ostiole >/tmp/ruleset.txt ||
		fail "Ostiole's table is not loaded"
	grep -q "bootstrap:management" /tmp/ruleset.txt ||
		{ cat /tmp/ruleset.txt; fail "the loaded table is not the bootstrap ruleset"; }
	grep -q "policy drop" /tmp/ruleset.txt ||
		{ cat /tmp/ruleset.txt; fail "the bootstrap input chain does not drop"; }

	step "$1: the router was persisted"
	[ -f /etc/sysctl.d/99-ostiole.conf ] || fail "no sysctl file"
	grep -q "net/ipv4/ip_forward\|net.ipv4.ip_forward" /etc/sysctl.d/99-ostiole.conf ||
		fail "the sysctl file does not turn forwarding on"
	[ -f /etc/systemd/journald.conf.d/ostiole.conf ] || fail "the journal was not bounded"

	step "$1: the web UI answers"
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		if curl -fsSk https://127.0.0.1/api/v1/health >/tmp/health.txt 2>/dev/null; then
			break
		fi
		sleep 1
	done
	grep -q "ok\|status" /tmp/health.txt 2>/dev/null ||
		fail "the UI did not answer on 443: $(cat /tmp/health.txt 2>/dev/null)"
}

# assert_tailscale is the end state of the opt-in daemon: on the router,
# with a unit of ours, and nothing started it.
assert_tailscale() {
	step "$1: tailscaled is on this router"
	command -v tailscaled >/dev/null 2>&1 || PATH="$PATH:/usr/sbin:/sbin" command -v tailscaled >/dev/null 2>&1 ||
		fail "tailscaled is not on this router"
	systemctl cat ostiole-tailscaled.service >/dev/null 2>&1 ||
		fail "ostiole-tailscaled.service was not written"
	state=$(systemctl is-enabled tailscaled.service 2>&1 || true)
	case "$state" in
	masked | not-found | *"No such file"*) ;;
	*) fail "tailscaled.service is $state, want masked or gone" ;;
	esac
	# Joining a tailnet is an apply, not an install: nothing here starts it.
	! systemctl is-active ostiole-tailscaled.service >/dev/null 2>&1 ||
		fail "ostiole-tailscaled.service is running, and nothing asked it to"

	step "$1: the repository it came from is in place"
	case "$(manager)" in
	apt-get)
		[ -f /etc/apt/sources.list.d/tailscale.list ] || fail "no Tailscale apt source"
		;;
	dnf)
		case "$(distro_id)" in
		rocky | rhel | almalinux | centos)
			[ -f /etc/yum.repos.d/tailscale.repo ] || fail "no Tailscale yum repository"
			;;
		esac
		;;
	esac
}

# assert_wireless is the end state of the opt-in radio packages: on the
# router, with a unit of ours, and nothing started it.
assert_wireless() {
	step "$1: hostapd and iw are on this router"
	for cmd in hostapd iw; do
		command -v "$cmd" >/dev/null 2>&1 || PATH="$PATH:/usr/sbin:/sbin" command -v "$cmd" >/dev/null 2>&1 ||
			fail "$cmd is not on this router"
	done
	[ -f /lib/firmware/regulatory.db ] || [ -f /usr/lib/firmware/regulatory.db ] ||
		fail "no regulatory database; every radio would stay on the lowest power"
	systemctl cat ostiole-hostapd@.service >/dev/null 2>&1 ||
		fail "ostiole-hostapd@.service was not written"
	state=$(systemctl is-enabled hostapd.service 2>&1 || true)
	case "$state" in
	masked | not-found | disabled | *"No such file"*) ;;
	*) fail "hostapd.service is $state, want masked or gone" ;;
	esac
	# Serving a network is an apply, not an install: nothing here starts one.
	! systemctl list-units --state=active 'ostiole-hostapd@*' 2>/dev/null | grep -q ostiole-hostapd ||
		fail "an ostiole-hostapd instance is running, and nothing asked it to"
}

# assert_proxy is the end state of the opt-in sidecar: on the router,
# with a unit and an account of ours, and nothing started it.
assert_proxy() {
	step "$1: ostiole-proxy is on this router"
	command -v ostiole-proxy >/dev/null 2>&1 || PATH="$PATH:/usr/sbin:/sbin" command -v ostiole-proxy >/dev/null 2>&1 ||
		fail "ostiole-proxy is not on this router"
	systemctl cat ostiole-proxy.service >/dev/null 2>&1 ||
		fail "ostiole-proxy.service was not written"
	getent passwd ostiole-proxy >/dev/null 2>&1 ||
		fail "the ostiole-proxy account was not created"
	# Publishing a site is an apply, not an install: nothing here starts it.
	! systemctl is-active ostiole-proxy.service >/dev/null 2>&1 ||
		fail "ostiole-proxy.service is running, and nothing asked it to"
}

# no_warnings holds the installer to its own output: a line that starts
# with "warning:" is something it could not do, and on a stock image
# there is nothing it should not be able to do.
no_warnings() {
	! grep -q '^warning:' "$1" ||
		{ grep '^warning:' "$1"; fail "the install warned about something"; }
}

step "the documented install, agreed to in advance"
# Piped, with the answer passed through `sh -s --`, because that is the
# published one-liner and because a pipe leaves stdin useless for the
# question the installer asks. Fetched to a file first and checked: the
# status of `curl | sh` is the shell's, and a shell given nothing to
# read exits 0, which would pass this step without installing anything.
curl -fsSL "$BASE/install.sh" >/tmp/install.sh
[ -s /tmp/install.sh ] || fail "$BASE/install.sh served nothing"
# shellcheck disable=SC2002 # the pipe is what is being tested
cat /tmp/install.sh | OSTIOLE_BASE_URL="$BASE" OSTIOLE_VERSION="$VERSION" sh -s -- --yes >/tmp/install.log 2>&1 ||
	{ cat /tmp/install.log; fail "the documented install failed"; }
cat /tmp/install.log
no_warnings /tmp/install.log

assert_router "install"

step "ostiole repair puts it all back"
ostiole repair --yes >/tmp/repair.log 2>&1 ||
	{ cat /tmp/repair.log; fail "ostiole repair failed"; }
no_warnings /tmp/repair.log

assert_router "repair"

step "ostiole repair --tailscale adds the tailnet daemon"
ostiole repair --yes --tailscale >/tmp/tailscale.log 2>&1 ||
	{ cat /tmp/tailscale.log; fail "ostiole repair --tailscale failed"; }
no_warnings /tmp/tailscale.log

assert_tailscale "tailscale"
assert_router "tailscale"

step "a plain repair keeps Tailscale and its repository"
ostiole repair --yes >/tmp/repair2.log 2>&1 ||
	{ cat /tmp/repair2.log; fail "the repair after --tailscale failed"; }
no_warnings /tmp/repair2.log

assert_tailscale "plain repair"
assert_router "plain repair"

step "ostiole repair --wireless adds what a radio needs"
ostiole repair --yes --wireless >/tmp/wireless.log 2>&1 ||
	{ cat /tmp/wireless.log; fail "ostiole repair --wireless failed"; }
no_warnings /tmp/wireless.log

assert_wireless "wireless"
assert_router "wireless"

step "a plain repair keeps the wireless packages"
ostiole repair --yes >/tmp/repair3.log 2>&1 ||
	{ cat /tmp/repair3.log; fail "the repair after --wireless failed"; }
no_warnings /tmp/repair3.log

assert_wireless "plain repair"

step "ostiole repair --proxy adds the reverse proxy"
OSTIOLE_BASE_URL="$BASE" ostiole repair --yes --proxy >/tmp/proxy.log 2>&1 ||
	{ cat /tmp/proxy.log; fail "ostiole repair --proxy failed"; }
no_warnings /tmp/proxy.log

assert_proxy "proxy"
assert_router "proxy"

step "a plain repair keeps the reverse proxy"
OSTIOLE_BASE_URL="$BASE" ostiole repair --yes >/tmp/repair4.log 2>&1 ||
	{ cat /tmp/repair4.log; fail "the repair after --proxy failed"; }
no_warnings /tmp/repair4.log

assert_proxy "plain repair"

step "what the router says about itself"
ostiole host || true

printf '\nthe install did what it said it would\n'
