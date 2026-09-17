package sysupdate

import (
	"context"
	"fmt"
	"strings"
)

// apk drives Alpine. Like Arch it has one stream and no security-only
// channel; unlike Arch it has no way to hold a package back from an
// upgrade either, so an exclude list cannot be honoured here.
type apk struct{}

func (apk) Name() string               { return "apk" }
func (apk) SecurityCapable() bool      { return false }
func (apk) ExcludeSupported(bool) bool { return false }

func (a apk) Check(ctx context.Context, run Runner) (Pending, error) {
	_, _ = run.Run(ctx, a.Name(), "update")
	out, err := run.Run(ctx, a.Name(), "upgrade", "--simulate")
	if err != nil {
		return Pending{}, fmt.Errorf("apk upgrade --simulate: %w: %s", err, tail(out))
	}
	pkgs := parseAPKSimulate(out)
	sortPackages(pkgs)
	return Pending{
		Packages: pkgs,
		Note:     "Alpine has no security-only channel; every update is in one stream",
	}, nil
}

// parseAPKSimulate reads the lines a simulated upgrade prints:
//
//	(1/2) Upgrading busybox (1.36.1-r5 -> 1.36.1-r7)
func parseAPKSimulate(out []byte) []Package {
	var pkgs []Package
	for _, line := range lines(out) {
		_, rest, ok := strings.Cut(line, "Upgrading ")
		if !ok {
			continue
		}
		name, versions, ok := strings.Cut(strings.TrimSpace(rest), " (")
		if !ok || name == "" {
			continue
		}
		versions = strings.TrimSuffix(strings.TrimSpace(versions), ")")
		from, to, ok := strings.Cut(versions, " -> ")
		if !ok {
			continue
		}
		pkgs = append(pkgs, Package{Name: name, From: strings.TrimSpace(from), To: strings.TrimSpace(to)})
	}
	return pkgs
}

func (a apk) UpgradeArgv(security bool, _ []string, _ Pending) []string {
	if security {
		return nil
	}
	return []string{a.Name(), "upgrade"}
}

func (a apk) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	return kernelRebootHint(ctx, run)
}
