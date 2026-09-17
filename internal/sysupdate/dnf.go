package sysupdate

import (
	"context"
	"fmt"
	"strings"
)

// dnf drives Fedora, RHEL and everything rebuilt from it. It is the one
// manager where the security channel is a flag rather than a trick:
// `dnf upgrade --security` is exactly what it says.
type dnf struct{}

func (dnf) Name() string               { return "dnf" }
func (dnf) SecurityCapable() bool      { return true }
func (dnf) ExcludeSupported(bool) bool { return true }

// dnfPending is the exit status check-update uses for "there is
// something waiting". Zero means nothing to do; anything else is a
// genuine failure.
const dnfPending = 100

func (d dnf) Check(ctx context.Context, run Runner) (Pending, error) {
	// --refresh so a weekly check does not report what the metadata
	// happened to say last time.
	all, err := d.checkUpdate(ctx, run, []string{"--refresh"}, nil)
	if err != nil {
		return Pending{}, err
	}
	// The second pass reads the metadata the first one just downloaded.
	sec, err := d.checkUpdate(ctx, run, []string{"--cacheonly"}, []string{"--security"})
	if err != nil {
		return Pending{}, err
	}
	secure := map[string]bool{}
	for _, p := range sec {
		secure[p.Name] = true
	}
	for i := range all {
		all[i].Security = secure[all[i].Name]
	}
	d.fillInstalled(ctx, run, all)
	sortPackages(all)
	return Pending{Packages: all, Security: countSecurity(all)}, nil
}

// checkUpdate runs one pass of check-update. The global options go
// before the command and the command's own after it, which is the one
// arrangement both dnf 4 and dnf 5 accept.
func (d dnf) checkUpdate(ctx context.Context, run Runner, global, extra []string) ([]Package, error) {
	argv := append([]string{"-q"}, global...)
	argv = append(argv, "check-update")
	argv = append(argv, extra...)
	out, err := run.Run(ctx, d.Name(), argv...)
	switch code := exitCode(err); code {
	case 0, dnfPending:
		return parseDNFCheck(out), nil
	default:
		return nil, fmt.Errorf("dnf check-update: %w: %s", err, tail(out))
	}
}

// parseDNFCheck reads the three-column listing. A long package name is
// printed on a line of its own with the version indented under it, so a
// lone name is remembered until the rest of its row arrives.
func parseDNFCheck(out []byte) []Package {
	var pkgs []Package
	pending := ""
	for _, line := range lines(out) {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			continue
		// Everything from here down is about packages being replaced,
		// not upgraded, and repeats what is already listed above.
		case strings.EqualFold(fields[0], "Obsoleting"):
			return pkgs
		case strings.HasPrefix(line, " ") && pending != "" && len(fields) == 2:
			pkgs = append(pkgs, Package{Name: pending, To: fields[0], Repo: fields[1]})
			pending = ""
		case len(fields) == 1 && strings.Contains(fields[0], "."):
			pending = stripArch(fields[0])
		// A row is exactly three columns. Anything else is dnf talking
		// about its metadata, not about a package.
		case len(fields) == 3:
			pending = ""
			pkgs = append(pkgs, Package{Name: stripArch(fields[0]), To: fields[1], Repo: fields[2]})
		}
	}
	return pkgs
}

// rpmArches are the suffixes check-update puts on a package name. They
// are stripped so "kernel" on the exclude list matches the row that
// dnf calls "kernel.x86_64".
var rpmArches = map[string]bool{
	"x86_64": true, "aarch64": true, "noarch": true, "i686": true,
	"armv7hl": true, "s390x": true, "ppc64le": true, "src": true,
}

func stripArch(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 && rpmArches[name[i+1:]] {
		return name[:i]
	}
	return name
}

// maxQueryNames bounds the argument list of the version lookup. A router
// with more pending packages than this is mid-distro-upgrade, and the
// installed column is the least of its worries.
const maxQueryNames = 200

// fillInstalled asks rpm what is installed now, because check-update
// only reports what is coming and "6.12.0-55" alone does not tell you
// whether it matters.
func (d dnf) fillInstalled(ctx context.Context, run Runner, pkgs []Package) {
	if len(pkgs) == 0 || len(pkgs) > maxQueryNames {
		return
	}
	argv := append([]string{"-q", "--qf", "%{NAME} %{EVR}\\n"}, names(pkgs)...)
	out, err := run.Run(ctx, "rpm", argv...)
	if err != nil && len(out) == 0 {
		return
	}
	installed := map[string]string{}
	for _, line := range lines(out) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.Contains(line, "not installed") {
			continue
		}
		installed[fields[0]] = fields[1]
	}
	for i := range pkgs {
		pkgs[i].From = installed[pkgs[i].Name]
	}
}

func (d dnf) UpgradeArgv(security bool, exclude []string, _ Pending) []string {
	argv := []string{d.Name(), "-y", "upgrade"}
	if security {
		argv = append(argv, "--security")
	}
	for _, e := range exclude {
		argv = append(argv, "--exclude="+e)
	}
	return argv
}

func (d dnf) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	out, err := run.Run(ctx, d.Name(), "needs-restarting", "-r")
	text := string(out)
	// The plugin is not installed everywhere, and a missing subcommand
	// exits with the same status as "yes, reboot", so the text decides.
	missing := strings.Contains(text, "No such command") ||
		strings.Contains(text, "Unknown argument") ||
		strings.Contains(text, "not a valid command")
	if !missing {
		if strings.Contains(text, "Reboot is required") || (exitCode(err) == 1 && strings.TrimSpace(text) != "") {
			return true, "dnf needs-restarting says core libraries or services were updated"
		}
		if exitCode(err) == 0 {
			return false, ""
		}
	}
	return kernelRebootHint(ctx, run)
}

func (d dnf) Installed(ctx context.Context, run Runner, pkg string) (bool, error) {
	return rpmInstalled(ctx, run, pkg)
}

func (d dnf) InstallArgv(pkgs []string) []string {
	return append([]string{d.Name(), "-y", "install"}, pkgs...)
}

func (d dnf) RemoveArgv(pkgs []string, preview bool) []string {
	// The answer goes before the command, like every other global option
	// dnf takes: --assumeno prints the transaction and then declines to
	// run it, which is the closest dnf comes to a dry run that lists what
	// would come away. It exits 1 having changed nothing, and
	// previewRefused knows that is the answer rather than a failure.
	answer := "-y"
	if preview {
		answer = "--assumeno"
	}
	return append([]string{d.Name(), answer, "remove"}, pkgs...)
}

// rpmInstalled asks the rpm database, which both dnf and zypper routers
// have. `rpm -q` prints the version of a package it has and says "not
// installed" about one it does not, exiting non-zero either way, so the
// text decides; only rpm failing to run at all is an error.
func rpmInstalled(ctx context.Context, run Runner, pkg string) (bool, error) {
	out, err := run.Run(ctx, "rpm", "-q", pkg)
	text := strings.TrimSpace(string(out))
	switch {
	case strings.Contains(text, "not installed"):
		return false, nil
	case err == nil && text != "":
		return true, nil
	case text == "":
		return false, fmt.Errorf("rpm -q %s: %w", pkg, err)
	}
	return false, nil
}

// tail keeps the end of a failed command's output, which is where the
// reason is.
func tail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 400 {
		s = "…" + s[len(s)-400:]
	}
	return s
}
