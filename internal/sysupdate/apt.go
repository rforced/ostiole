package sysupdate

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// apt drives Debian and Ubuntu. There is no `--security` switch here:
// the security archive is a separate origin, and apt tells you which
// origin an upgrade comes from when you ask it to simulate one. That is
// the same fact `unattended-upgrades` keys off, without needing it
// installed.
type apt struct{}

func (apt) Name() string               { return "apt-get" }
func (apt) SecurityCapable() bool      { return true }
func (apt) ExcludeSupported(bool) bool { return true }

// rebootRequiredFile is Debian's flag that something wants a restart.
var rebootRequiredFile = "/run/reboot-required"

// aptConfirm keeps a package that ships a changed config file from
// stopping an unattended upgrade with a prompt: keep what is on the box.
var aptConfirm = []string{
	"-o", "Dpkg::Options::=--force-confold",
	"-o", "Dpkg::Options::=--force-confdef",
}

func (a apt) Check(ctx context.Context, run Runner) (Pending, error) {
	// A stale index reports nothing waiting, which is the one wrong
	// answer this page must never give. A refresh that fails is still
	// worth simulating against what we have.
	_, _ = run.Run(ctx, a.Name(), "-q", "update")
	out, err := run.Run(ctx, a.Name(), "-q", "-s", "upgrade", "--with-new-pkgs")
	if err != nil {
		return Pending{}, fmt.Errorf("apt-get -s upgrade: %w: %s", err, tail(out))
	}
	pkgs := parseAptSimulate(out)
	sortPackages(pkgs)
	return Pending{Packages: pkgs, Security: countSecurity(pkgs)}, nil
}

// parseAptSimulate reads the Inst lines of a simulated upgrade:
//
//	Inst libssl3 [3.0.11-1~deb12u2] (3.0.13-1~deb12u1 Debian-Security:12/stable-security [amd64])
//
// The origin in the parentheses is what marks a security upgrade.
func parseAptSimulate(out []byte) []Package {
	var pkgs []Package
	for _, line := range lines(out) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Inst ")
		if !ok {
			continue
		}
		name, rest, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if name == "" {
			continue
		}
		p := Package{Name: name}
		rest = strings.TrimSpace(rest)
		if after, found := strings.CutPrefix(rest, "["); found {
			if version, remainder, ok := strings.Cut(after, "]"); ok {
				p.From, rest = version, strings.TrimSpace(remainder)
			}
		}
		if open := strings.Index(rest, "("); open >= 0 {
			inner := rest[open+1:]
			if end := strings.Index(inner, ")"); end >= 0 {
				inner = inner[:end]
			}
			// The trailing [arch] is not worth a column.
			if bracket := strings.Index(inner, "["); bracket >= 0 {
				inner = inner[:bracket]
			}
			to, origin, _ := strings.Cut(strings.TrimSpace(inner), " ")
			p.To = to
			p.Repo = strings.TrimSpace(origin)
			p.Security = strings.Contains(strings.ToLower(p.Repo), "security")
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func (a apt) UpgradeArgv(security bool, exclude []string, pending Pending) []string {
	argv := append([]string{a.Name(), "-y"}, aptConfirm...)
	if !security && len(exclude) == 0 {
		return append(argv, "upgrade", "--with-new-pkgs")
	}
	// apt has nothing to exclude with and no security switch, so the
	// list is spelled out. Naming packages also means an upgrade can
	// never remove one.
	wanted := keepWanted(pending.Packages, security, exclude)
	if len(wanted) == 0 {
		return nil
	}
	argv = append(argv, "install", "--only-upgrade")
	return append(argv, names(wanted)...)
}

func (a apt) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	if _, err := os.Stat(rebootRequiredFile); err != nil {
		return kernelRebootHint(ctx, run)
	}
	reason := "a package on this box asked for a reboot"
	if raw, err := os.ReadFile(rebootRequiredFile + ".pkgs"); err == nil {
		if pkgs := lines(raw); len(pkgs) > 0 {
			reason = "updated and waiting on a reboot: " + strings.Join(pkgs, ", ")
		}
	}
	return true, reason
}
