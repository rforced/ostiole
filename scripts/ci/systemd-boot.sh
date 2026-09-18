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

exec /sbin/init
