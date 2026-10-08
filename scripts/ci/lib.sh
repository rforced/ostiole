# shellcheck shell=sh
# Shared helpers for the container tests: one package manager out of
# three, and the few questions these tests put to it. Sourced, not run.

# manager names the package manager this image has.
manager() {
	for m in apt-get dnf pacman; do
		if command -v "$m" >/dev/null 2>&1; then
			echo "$m"
			return 0
		fi
	done
	echo ""
}

# dnf_ci is dnf that leaves a mirror for the next once it has sent under
# 500 kB/s for 10 s, the wait for an answer included. dnf's own limit is
# 1 kB/s for 30 s, and the mirror list comes shuffled: with a Rocky mirror
# that takes 20 s to answer each request first, systemd-boot.sh took ten
# minutes, where run-systemd-test.sh waits four.
dnf_ci() { dnf --setopt=timeout=10 --setopt=minrate=500k "$@"; }

# UBUNTU_MIRROR is a list, tried in order: apt moves to the next on an error.
ubuntu_mirror() {
	sources=/etc/apt/sources.list.d/ubuntu.sources
	[ -n "${UBUNTU_MIRROR:-}" ] && [ -f "$sources" ] || return 0
	n=0
	for uri in $UBUNTU_MIRROR; do
		n=$((n + 1))
		printf '%s\tpriority:%s\n' "$uri" "$n"
	done >/etc/apt/ci-mirrors.txt
	sed -i -E "s#http://(archive|security)\.ubuntu\.com/ubuntu/?#mirror+file:/etc/apt/ci-mirrors.txt#" "$sources"
}

# apt waits minutes on a mirror that takes a request and sends nothing.
apt_timeout() {
	printf 'Acquire::http::Timeout "10";\nAcquire::Retries "3";\n' >/etc/apt/apt.conf.d/99ci-timeout
}

pkg_refresh() {
	case "$(manager)" in
	apt-get)
		ubuntu_mirror
		apt_timeout
		DEBIAN_FRONTEND=noninteractive apt-get update -qq
		;;
	# Nothing: an install fetches the metadata it is missing, and dnf 4's
	# makecache would fetch every repository's again, however fresh.
	dnf) ;;
	# Arch upgrades everything or nothing. The image trails the
	# repositories, and one package taken from a newer database breaks
	# whatever is pinned to the old one: systemd 262 on its own is refused
	# because systemd-sysvcompat wants the systemd the image came with.
	pacman) pacman -Syu --noconfirm >/dev/null ;;
	esac
}

pkg_install() {
	case "$(manager)" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" >/dev/null ;;
	dnf) dnf_ci install -y -q "$@" >/dev/null ;;
	pacman) pacman -S --noconfirm --needed "$@" >/dev/null ;;
	*) return 1 ;;
	esac
}

# pkg_present asks the package database, which is the question these
# tests mean: a command on PATH can come from somewhere else, and a
# package that was removed can leave its configuration behind.
pkg_present() {
	case "$(manager)" in
	apt-get) dpkg-query -s "$1" 2>/dev/null | grep -q "^Status:.*install ok installed" ;;
	dnf) rpm -q "$1" >/dev/null 2>&1 ;;
	pacman) pacman -Q "$1" >/dev/null 2>&1 ;;
	*) return 1 ;;
	esac
}

# ensure_tools installs a command only when it is missing. Asking for
# what is already there is not harmless: on Fedora `dnf install curl`
# fails against the curl-minimal in the image.
ensure_tools() {
	missing=""
	for cmd in "$@"; do
		command -v "$cmd" >/dev/null 2>&1 || missing="$missing $cmd"
	done
	[ -n "$missing" ] || return 0
	pkg_refresh
	# shellcheck disable=SC2086 # the list is built to be split
	pkg_install $missing
}

# distro and distro_id are what the image calls itself: the first for a
# human reading the log, the second for the handful of decisions that
# turn on which enterprise distribution this is.
distro() {
	# shellcheck source=/dev/null
	[ -r /etc/os-release ] && . /etc/os-release
	echo "${PRETTY_NAME:-unknown}"
}

distro_id() {
	# shellcheck source=/dev/null
	[ -r /etc/os-release ] && . /etc/os-release
	echo "${ID:-unknown}"
}


step() { printf '\n=== %s\n' "$1"; }
fail() {
	printf 'FAIL: %s\n' "$1" >&2
	exit 1
}
