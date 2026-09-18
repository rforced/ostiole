# shellcheck shell=sh
# Shared helpers for the container tests: one package manager out of
# four, and the few questions these tests put to it. Sourced, not run.

# manager names the package manager this image has.
manager() {
	for m in apt-get dnf apk pacman; do
		if command -v "$m" >/dev/null 2>&1; then
			echo "$m"
			return 0
		fi
	done
	echo ""
}

pkg_refresh() {
	case "$(manager)" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get update -qq ;;
	dnf) dnf makecache -q >/dev/null 2>&1 || true ;;
	apk) apk update >/dev/null ;;
	pacman) pacman -Sy --noconfirm >/dev/null ;;
	esac
}

pkg_install() {
	case "$(manager)" in
	apt-get) DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" >/dev/null ;;
	dnf) dnf install -y -q "$@" >/dev/null ;;
	apk) apk add --no-cache "$@" >/dev/null ;;
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
	apk) apk info -e "$1" >/dev/null 2>&1 ;;
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
