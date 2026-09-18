#!/bin/sh
# PID 1 for the install test's container: bring this image up to what a
# freshly imaged router looks like, then hand over to systemd for real.
#
# That means systemd itself, the two commands install.sh needs, and the
# things a distribution image arrives with that Ostiole exists to take
# away: a competing firewall, and an updater with a schedule of its own.
# Without them the installer would have nothing to retire, and the half
# of it that removes packages would go untested.
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
	pkg_install systemd systemd-sysv dbus ufw unattended-upgrades
	;;
dnf)
	# Enterprise Linux keeps systemd-networkd in EPEL and its images do
	# not enable it. A router that cannot reach EPEL is one Ostiole
	# refuses to finish installing on, and says so; the point here is to
	# test the router that can.
	case "$(distro_id)" in
	rocky | rhel | almalinux | centos) pkg_install epel-release ;;
	esac
	pkg_install systemd firewalld PackageKit
	;;
pacman)
	pkg_install systemd ufw
	;;
*)
	echo "this image has no systemd; it belongs in the script test only" >&2
	exit 1
	;;
esac
ensure_tools curl tar openssl

exec /sbin/init
