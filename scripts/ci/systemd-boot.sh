#!/bin/sh
# PID 1 for the install test's container: put systemd and the two commands
# the one-liner presupposes into a stock image, then hand over to systemd
# for real.
#
# Nothing else goes in. The test is the documented install against the
# official image, so anything added here would be testing a router nobody
# has.
#
# It execs systemd at the end, so the container's PID 1 ends up being
# systemd however long the preparation took.
set -eu
# shellcheck source=scripts/ci/lib.sh
. "$(dirname "$0")/lib.sh"

pkg_refresh
case "$(manager)" in
apt-get)
	# systemd-sysv is what puts systemd at /sbin/init on Debian and
	# Ubuntu; the image has neither.
	pkg_install systemd systemd-sysv dbus
	;;
dnf)
	pkg_install systemd
	;;
pacman)
	pkg_install systemd
	;;
*)
	echo "this image has no systemd; it belongs in the script test only" >&2
	exit 1
	;;
esac
ensure_tools curl tar openssl

# A privileged container sees the host's sysfs, so the installer's wifi
# detection finds whatever card the workstation running this has and asks
# for its firmware. CI runners have no radio, so the same test would take
# a different path here than there — and on Debian it fails outright,
# because the firmware packages are in non-free-firmware and the image
# does not enable it. Empty is what a router with no card looks like.
# Never fatal: a mask that will not mount is not worth a failed boot.
mount -t tmpfs tmpfs /sys/bus/pci/devices 2>/dev/null || true
mount -t tmpfs tmpfs /sys/class/ieee80211 2>/dev/null || true

exec /sbin/init
