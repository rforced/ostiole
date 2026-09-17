// Package kernel reports the running Linux version and the floor Ostiole
// supports.
package kernel

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Minimum is the oldest kernel Ostiole supports.
//
// The floor is a support decision, not a technical one: the rendered
// ruleset uses nothing newer than counters, log prefixes, and comments,
// all of which predate it by years. 5.14 is what RHEL 9 and its rebuilds
// ship, so the line sits at the oldest enterprise distribution still in
// support and takes every current Debian, Ubuntu, Fedora, Alpine, and Arch
// with it. Anything older is RHEL 8, Debian 11, or Ubuntu 20.04, which
// Ostiole does not test against.
var Minimum = Version{Major: 5, Minor: 14}

// ReleaseFile is where the kernel publishes its version.
const ReleaseFile = "/proc/sys/kernel/osrelease"

// Version is a kernel's major and minor number. The patch level and the
// distribution's own suffix are dropped; nothing Ostiole gates on is that
// fine-grained, and enterprise kernels backport so heavily that the patch
// level says little anyway.
type Version struct {
	Major, Minor int
}

// String renders the version as "5.14".
func (v Version) String() string { return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) }

// Before reports whether v is older than o.
func (v Version) Before(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	return v.Minor < o.Minor
}

// Supported reports whether v meets the floor.
func (v Version) Supported() bool { return !v.Before(Minimum) }

// Parse reads the major and minor out of a release string such as
// "5.14.0-503.el9.x86_64", "6.8.0-45-generic", or "6.12".
func Parse(release string) (Version, error) {
	s := strings.TrimSpace(release)
	majorStr, rest, ok := strings.Cut(s, ".")
	if !ok {
		return Version{}, fmt.Errorf("kernel release %q has no minor version", release)
	}
	// The minor number runs to the next separator, whatever that turns out
	// to be: a dot on most kernels, a dash on one built without a patch
	// level.
	minorStr := rest
	if i := strings.IndexFunc(rest, notDigit); i >= 0 {
		minorStr = rest[:i]
	}
	major, err := strconv.Atoi(majorStr)
	if err != nil {
		return Version{}, fmt.Errorf("kernel release %q: bad major version: %w", release, err)
	}
	minor, err := strconv.Atoi(minorStr)
	if err != nil {
		return Version{}, fmt.Errorf("kernel release %q: bad minor version: %w", release, err)
	}
	return Version{Major: major, Minor: minor}, nil
}

func notDigit(r rune) bool { return r < '0' || r > '9' }

// Current reads the running kernel's version.
func Current() (Version, error) { return read(ReleaseFile) }

func read(path string) (Version, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Version{}, err
	}
	return Parse(string(raw))
}

// Check reports an error when the running kernel is older than Minimum.
//
// A version it cannot read or parse is not an error. Ostiole is a firewall:
// refusing to work because an unusual kernel spells its version oddly would
// be worse than running on it.
func Check() error { return check(ReleaseFile) }

func check(path string) error {
	v, err := read(path)
	if err != nil {
		return nil //nolint:nilerr // an unreadable version is not a reason to refuse
	}
	if !v.Supported() {
		return fmt.Errorf("this router runs Linux %s; Ostiole needs %s or newer", v, Minimum)
	}
	return nil
}
