#!/bin/sh
# Installs Ostiole for real, the way the documentation says to, inside a
# container that is running systemd, and then asks the router whether
# every promise the installer printed came true.
#
#   install-systemd-test.sh <base-url> <version>
#
# The three things being tested are the three the installer does and a
# unit test cannot: that the packages a router needs go on, that the
# ones it has no use for come off, and that what is left is a running
# firewall with its ruleset in the kernel.
set -eu
# shellcheck source=scripts/ci/lib.sh
. "$(dirname "$0")/lib.sh"

BASE="${1:?base url}"
VERSION="${2:?version}"

# What this distribution calls the firewall Ostiole replaces, and an
# extra that should not survive the install either.
case "$(manager)" in
apt-get)
	competitor=ufw
	competitor_unit=ufw.service
	extra=unattended-upgrades
	tc_package=iproute2
	;;
dnf)
	competitor=firewalld
	competitor_unit=firewalld.service
	extra=PackageKit
	tc_package=iproute-tc
	;;
pacman)
	competitor=ufw
	competitor_unit=ufw.service
	extra=""
	tc_package=iproute2
	;;
*) fail "no package manager, or one with no systemd" ;;
esac

step "a router with $competitor on it"
systemctl enable "$competitor_unit" >/dev/null 2>&1 ||
	fail "could not enable $competitor_unit; the installer would have nothing to retire"
# Starting it is allowed to fail: some of these images cannot open a
# netlink socket for it, and enabled is enough to be a competitor.
systemctl start "$competitor_unit" >/dev/null 2>&1 || true
pkg_present "$competitor" || fail "$competitor is not installed to begin with"
[ -z "$extra" ] || pkg_present "$extra" || fail "$extra is not installed to begin with"

step "the documented install, agreed to in advance"
# Piped, with the answer passed through `sh -s --`, because that is the
# published one-liner and because a pipe leaves stdin useless for the
# question the installer asks. Fetched to a file first and checked: the
# status of `curl | sh` is the shell's, and a shell given nothing to
# read exits 0, which would pass this step without installing anything.
curl -fsSL "$BASE/install.sh" >/tmp/install.sh
[ -s /tmp/install.sh ] || fail "$BASE/install.sh served nothing"
# shellcheck disable=SC2002 # the pipe is what is being tested
cat /tmp/install.sh | OSTIOLE_BASE_URL="$BASE" OSTIOLE_VERSION="$VERSION" sh -s -- --yes

step "the units are running"
for unit in ostiole-firewall.service ostiole.service; do
	state=$(systemctl is-enabled "$unit" 2>&1 || true)
	[ "$state" = enabled ] || fail "$unit is $state, not enabled"
done
systemctl is-active ostiole.service >/dev/null ||
	fail "ostiole.service is $(systemctl is-active ostiole.service 2>&1): $(systemctl status ostiole.service --no-pager -l | tail -20)"

step "the packages a router needs went on"
for pkg in nftables dnsmasq unbound "$tc_package"; do
	pkg_present "$pkg" || fail "$pkg was not installed"
done
for cmd in nft dnsmasq unbound tc; do
	command -v "$cmd" >/dev/null 2>&1 || PATH="$PATH:/usr/sbin:/sbin" command -v "$cmd" >/dev/null 2>&1 ||
		fail "$cmd is not on this router"
done

step "the packages a router has no use for came off"
! pkg_present "$competitor" || fail "$competitor is still installed"
[ -z "$extra" ] || ! pkg_present "$extra" || fail "$extra is still installed"
# Masked as well as removed: a package that comes back must not start.
state=$(systemctl is-enabled "$competitor_unit" 2>&1 || true)
case "$state" in
masked | not-found) ;;
*) fail "$competitor_unit is $state, want masked" ;;
esac

step "the bootstrap ruleset is in the kernel"
PATH="$PATH:/usr/sbin:/sbin"
nft list table inet ostiole >/tmp/ruleset.txt ||
	fail "Ostiole's table is not loaded"
grep -q "bootstrap:management" /tmp/ruleset.txt ||
	{ cat /tmp/ruleset.txt; fail "the loaded table is not the bootstrap ruleset"; }
grep -q "policy drop" /tmp/ruleset.txt ||
	{ cat /tmp/ruleset.txt; fail "the bootstrap input chain does not drop"; }

step "the router was persisted"
[ -f /etc/sysctl.d/99-ostiole.conf ] || fail "no sysctl file"
grep -q "net/ipv4/ip_forward\|net.ipv4.ip_forward" /etc/sysctl.d/99-ostiole.conf ||
	fail "the sysctl file does not turn forwarding on"
[ -f /etc/systemd/journald.conf.d/ostiole.conf ] || fail "the journal was not bounded"

step "the web UI answers"
for _ in 1 2 3 4 5 6 7 8 9 10; do
	if curl -fsSk https://127.0.0.1/api/v1/health >/tmp/health.txt 2>/dev/null; then
		break
	fi
	sleep 1
done
grep -q "ok\|status" /tmp/health.txt 2>/dev/null ||
	fail "the UI did not answer on 443: $(cat /tmp/health.txt 2>/dev/null)"

step "what the router says about itself"
ostiole host || true

printf '\nthe install did what it said it would\n'
