#!/bin/sh
# Exercises internal/install/install.sh inside a container of some
# distribution, against a build served over HTTP rather than a GitHub
# release.
#
# What is tested here is the script itself: that it finds the tools it
# needs and says so when they are missing, that it plans the right
# packages for this distribution, that it refuses a tarball whose
# checksum does not match and a signature that does not verify, and that
# it puts a working binary where it says it did. Installing proper needs
# systemd and has a script of its own.
#
#   install-script-test.sh <base-url> <version>
#
# The server behind <base-url> holds the build, a tampered/ tree whose
# tarball does not match its checksums, and a badsig/ tree signed by the
# wrong key. Run as root in a throwaway container: it installs packages
# and moves commands around.
set -eu
# shellcheck source=scripts/ci/lib.sh
. "$(dirname "$0")/lib.sh"

BASE="${1:?base url}"
VERSION="${2:?version}"
export OSTIOLE_VERSION="$VERSION"

# hide_curl and show_curl move the command aside rather than removing the
# package, because on some of these distributions curl is what the
# package manager fetches with.
hide_curl() {
	for dir in /usr/bin /bin /usr/local/bin; do
		if [ -e "$dir/curl" ]; then mv "$dir/curl" "$dir/curl.hidden"; fi
	done
}
show_curl() {
	for dir in /usr/bin /bin /usr/local/bin; do
		if [ -e "$dir/curl.hidden" ]; then mv "$dir/curl.hidden" "$dir/curl"; fi
	done
}

step "preparing $(distro)"
# Only what install.sh asks a router to have already, and only when it
# is missing: the images differ over which of these they arrive with,
# and asking for one that is there is its own kind of failure.
ensure_tools curl tar openssl

# Fetched once and checked, rather than curled into sh at each step. In
# `curl … | sh` the exit status is the shell's, and a shell handed
# nothing to read exits 0: a test written the documented way passes
# every check the moment the server behind it stops answering.
step "fetching the install script"
curl -fsSL "$BASE/install.sh" >/tmp/install.sh
[ -s /tmp/install.sh ] || fail "$BASE/install.sh served nothing"

step "the plan names this distribution's packages"
# --dry-run is the whole per-distribution path: find the package manager,
# work out what this router needs and what it has no use for, say so.
if ! OSTIOLE_BASE_URL="$BASE" sh /tmp/install.sh --dry-run >/tmp/plan.txt 2>&1; then
	cat /tmp/plan.txt
	fail "install.sh --dry-run failed"
fi
cat /tmp/plan.txt
grep -q "nftables" /tmp/plan.txt || fail "the plan does not install nftables"
grep -qi "miniupnpd\|no UPnP" /tmp/plan.txt || fail "the plan says nothing about UPnP"
[ ! -e /usr/local/bin/ostiole ] || fail "--dry-run installed a binary"
! grep -q "tailscale" /tmp/plan.txt || fail "the plan offers Tailscale to a router that did not ask"

step "the plan names Tailscale when it is asked for"
if ! OSTIOLE_BASE_URL="$BASE" sh /tmp/install.sh --dry-run --with-tailscale >/tmp/plan-ts.txt 2>&1; then
	cat /tmp/plan-ts.txt
	fail "install.sh --dry-run --with-tailscale failed"
fi
grep -q "^  tailscale: " /tmp/plan-ts.txt ||
	{ cat /tmp/plan-ts.txt; fail "the plan does not say where Tailscale comes from"; }

# pipe_install runs it the way the documentation says to, against the
# tree named. The pipe is the point: it is what leaves stdin useless
# for the question the installer asks.
pipe_install() {
	# shellcheck disable=SC2002 # the pipe is what is being tested
	cat /tmp/install.sh | OSTIOLE_BASE_URL="$1" OSTIOLE_NO_INSTALL=1 sh -s -- --yes
}

step "curl | sh places the binary"
pipe_install "$BASE"
[ -x /usr/local/bin/ostiole ] || fail "no binary at /usr/local/bin/ostiole"
/usr/local/bin/ostiole version | grep -q "$VERSION" ||
	fail "the binary is not $VERSION: $(/usr/local/bin/ostiole version)"

# What follows must leave nothing behind, so each starts from nothing.
rm -f /usr/local/bin/ostiole

step "a tampered tarball is refused"
if pipe_install "$BASE/tampered" 2>/tmp/err.txt; then
	fail "a tarball that does not match checksums.txt was installed"
fi
grep -qi "checksum mismatch" /tmp/err.txt ||
	{ cat /tmp/err.txt; fail "the refusal does not name the checksum"; }
[ ! -e /usr/local/bin/ostiole ] || fail "a refused download still left a binary"

step "a signature that does not verify is refused"
if pipe_install "$BASE/badsig" 2>/tmp/err.txt; then
	fail "a checksums.txt signed by the wrong key was installed"
fi
grep -qi "signature check failed" /tmp/err.txt ||
	{ cat /tmp/err.txt; fail "the refusal does not name the signature"; }
[ ! -e /usr/local/bin/ostiole ] || fail "a refused download still left a binary"

step "a router without curl is told so"
hide_curl
if OSTIOLE_BASE_URL="$BASE" sh /tmp/install.sh --yes 2>/tmp/err.txt; then
	show_curl
	fail "a router with no curl installed something"
fi
show_curl
grep -q "curl is required" /tmp/err.txt ||
	{ cat /tmp/err.txt; fail "the refusal does not name curl"; }

printf '\nall install.sh checks passed\n'
