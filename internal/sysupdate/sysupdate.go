// Package sysupdate keeps the Linux underneath Ostiole patched. It drives
// the distro's own package manager rather than reimplementing any part of
// it, for the same reason DHCP is dnsmasq and the firewall is nft: the
// tool that owns the package database should be the one that changes it.
//
// Each supported manager has a driver that knows three things — how to
// list what is waiting, how to install it, and whether the box wants a
// reboot afterwards. Nothing here decides *when* to run; that is the
// update mode in the configuration and the cron runner.
package sysupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Swappable so tests can describe a box they are not running on.
var (
	environ  = os.Environ
	lookPath = exec.LookPath
	// moduleDir holds one directory per installed kernel, which is how
	// the reboot hint knows a kernel arrived that nothing has booted.
	moduleDir = "/lib/modules"
)

// Package is one pending upgrade.
type Package struct {
	Name string `json:"name"`
	// From is the installed version, where the manager tells us; some
	// only report what is coming.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Repo is where it comes from, which is often the most useful column
	// on the page.
	Repo string `json:"repo,omitempty"`
	// Security marks an upgrade the publisher flagged as a security fix.
	Security bool `json:"security,omitempty"`
}

// Pending is everything a check found.
type Pending struct {
	Packages []Package `json:"packages"`
	// Security is how many security fixes are waiting. It is usually the
	// number of packages marked Security, but on a manager that groups
	// fixes into patches it counts those instead.
	Security int `json:"security"`
	// Note explains a distro quirk worth showing beside the numbers.
	Note string `json:"note,omitempty"`
}

// Runner runs a command and returns its combined output; swapped for a
// fake in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real commands, with a predictable locale so the output
// this package parses is the output it was written against.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = commandEnv()
	return cmd.CombinedOutput()
}

// commandEnv is the environment every package manager command runs with:
// C locale so the output is parseable, and no prompts for apt.
func commandEnv() []string {
	return append(environ(), "LC_ALL=C", "LANG=C", "DEBIAN_FRONTEND=noninteractive")
}

// Driver is one package manager.
type Driver interface {
	// Name is the command, e.g. "dnf".
	Name() string
	// SecurityCapable reports whether this manager can install security
	// fixes alone. Arch and Alpine ship one rolling stream and cannot.
	SecurityCapable() bool
	// ExcludeSupported reports whether a never-upgrade list can be
	// honoured in this mode, so the page can say when it cannot rather
	// than pretending a setting took effect.
	ExcludeSupported(security bool) bool
	// Check lists what is waiting, marking the security fixes.
	Check(ctx context.Context, run Runner) (Pending, error)
	// UpgradeArgv is the command that installs. pending comes from a
	// fresh Check, which is what a manager without a security switch of
	// its own upgrades by name instead.
	UpgradeArgv(security bool, exclude []string, pending Pending) []string
	// RebootRequired reports whether the box wants restarting, and why.
	RebootRequired(ctx context.Context, run Runner) (bool, string)
}

// Drivers are the managers Ostiole knows, in the order they are looked
// for on PATH.
func Drivers() []Driver {
	return []Driver{dnf{}, apt{}, zypper{}, pacman{}, apk{}}
}

// ErrNoManager means this box has no package manager Ostiole can drive.
var ErrNoManager = errors.New("no supported package manager was found (dnf, apt-get, zypper, pacman, apk)")

// ErrNoSecurityChannel means the manager has no security-only mode, so
// "security" would have to mean "everything" to do anything at all.
var ErrNoSecurityChannel = errors.New("has no security-only channel")

// SecurityUnavailable explains, in the words the page uses, why security
// updates cannot be separated out on this box.
func SecurityUnavailable(d Driver) string {
	return d.Name() + " has no security-only channel; choose All or Manual"
}

// Detect finds the package manager on this box. A name selects one
// explicitly, for a box with two installed and for tests.
func Detect(name string) (Driver, error) {
	for _, d := range Drivers() {
		if name != "" {
			if d.Name() != name {
				continue
			}
			return d, nil
		}
		if _, err := lookPath(d.Name()); err == nil {
			return d, nil
		}
	}
	if name != "" {
		return nil, errors.New(name + " is not a package manager Ostiole can drive")
	}
	return nil, ErrNoManager
}

// exitCode digs the status out of a command error. A command that could
// not be started at all reports -1, which no package manager returns.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1
}

// lines splits output into trimmed, non-empty lines.
func lines(out []byte) []string {
	var keep []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
			keep = append(keep, l)
		}
	}
	return keep
}

// countSecurity is the usual meaning of Pending.Security: the packages
// the manager flagged.
func countSecurity(pkgs []Package) int {
	n := 0
	for _, p := range pkgs {
		if p.Security {
			n++
		}
	}
	return n
}

// sortPackages puts security fixes first and then sorts by name, so the
// rows that matter are the rows you see without scrolling.
func sortPackages(pkgs []Package) {
	sort.SliceStable(pkgs, func(i, j int) bool {
		if pkgs[i].Security != pkgs[j].Security {
			return pkgs[i].Security
		}
		return pkgs[i].Name < pkgs[j].Name
	})
}

// names lists the package names, for the managers that upgrade by name.
func names(pkgs []Package) []string {
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, p.Name)
	}
	return out
}

// excluded reports whether a package is on the never-upgrade list.
func excluded(name string, exclude []string) bool {
	for _, e := range exclude {
		if e == name {
			return true
		}
	}
	return false
}

// keepWanted drops the excluded packages and, when security is asked
// for, everything that is not a security fix.
func keepWanted(pkgs []Package, security bool, exclude []string) []Package {
	out := make([]Package, 0, len(pkgs))
	for _, p := range pkgs {
		if excluded(p.Name, exclude) || (security && !p.Security) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// kernelRebootHint compares the running kernel with the module
// directories on disk. It is a heuristic, and says so in its answer, but
// it works the same on every distro and catches the case that matters:
// a kernel was installed and nothing has booted it yet.
func kernelRebootHint(ctx context.Context, run Runner) (bool, string) {
	out, err := run.Run(ctx, "uname", "-r")
	running := strings.TrimSpace(string(out))
	if err != nil || running == "" {
		return false, ""
	}
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		return false, ""
	}
	installed := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			installed = append(installed, e.Name())
		}
	}
	newest := ""
	for _, v := range installed {
		if v == running {
			continue
		}
		if compareKernel(v, running) > 0 && (newest == "" || compareKernel(v, newest) > 0) {
			newest = v
		}
	}
	if newest == "" {
		return false, ""
	}
	return true, "kernel " + newest + " is installed but " + running + " is running"
}

// compareKernel orders two kernel releases by their leading numbers,
// which is as much as "6.12.0-55.el10.x86_64" and "6.12.0-50.el10.x86_64"
// need to be told apart.
func compareKernel(a, b string) int {
	an, bn := kernelNumbers(a), kernelNumbers(b)
	for i := 0; i < len(an) && i < len(bn); i++ {
		if an[i] != bn[i] {
			if an[i] > bn[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case len(an) > len(bn):
		return 1
	case len(an) < len(bn):
		return -1
	}
	return strings.Compare(a, b)
}

func kernelNumbers(v string) []int {
	var out []int
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		if n, err := strconv.Atoi(cur.String()); err == nil {
			out = append(out, n)
		}
		cur.Reset()
	}
	for _, r := range v {
		if r >= '0' && r <= '9' {
			cur.WriteRune(r)
			continue
		}
		flush()
		// Anything past the distro suffix is noise for ordering.
		if r == '.' || r == '-' || r == '_' {
			continue
		}
		break
	}
	flush()
	return out
}

// CheckTimeout bounds a check. Refreshing metadata over a slow line is
// normal; waiting five minutes for it is not.
const CheckTimeout = 3 * time.Minute

// UpgradeTimeout bounds an install. A distro upgrade on a small box can
// genuinely take a while.
const UpgradeTimeout = time.Hour
