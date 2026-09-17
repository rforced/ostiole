package sysupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pacman drives Arch. Arch ships one rolling stream with no security
// archive and no advisories in the package metadata, so there is nothing
// to install "only the security fixes" from: the honest answer is to
// refuse and say why.
type pacman struct {
	// scratch is where the throwaway database goes. It cannot be /tmp:
	// the daemon has a PrivateTmp of its own, and the pacman that reads
	// the directory runs in a transient unit that would see a different
	// one.
	scratch string
}

func (pacman) Name() string               { return "pacman" }
func (pacman) SecurityCapable() bool      { return false }
func (pacman) ExcludeSupported(bool) bool { return true }

func (p pacman) useScratch(dir string) Driver { p.scratch = dir; return p }

// pacmanLocalDB is the installed-package database the check borrows so
// it can sync fresh repository metadata without touching the real one.
var pacmanLocalDB = "/var/lib/pacman/local"

func (p pacman) Check(ctx context.Context, run Runner) (Pending, error) {
	// checkupdates does exactly this dance and is the tool an Arch user
	// expects to be used, so use it when it is installed.
	if _, err := lookPath("checkupdates"); err == nil {
		out, err := run.Run(ctx, "checkupdates")
		// It exits 2 when there is nothing waiting.
		if err != nil && exitCode(err) != 2 {
			return Pending{}, fmt.Errorf("checkupdates: %w: %s", err, tail(out))
		}
		return p.pending(out), nil
	}

	// Without it, sync into a throwaway database. A bare `pacman -Sy`
	// against the real one leaves the router one step from a partial
	// upgrade, which is how an Arch install breaks.
	dir, err := os.MkdirTemp(p.scratch, "pacman-db")
	if err != nil {
		return Pending{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Symlink(pacmanLocalDB, filepath.Join(dir, "local")); err != nil {
		return Pending{}, err
	}
	if out, err := run.Run(ctx, p.Name(), "-Sy", "--dbpath", dir, "--logfile", os.DevNull); err != nil {
		return Pending{}, fmt.Errorf("pacman -Sy: %w: %s", err, tail(out))
	}
	out, err := run.Run(ctx, p.Name(), "-Qu", "--dbpath", dir)
	// -Qu exits 1 when nothing is out of date.
	if err != nil && exitCode(err) != 1 {
		return Pending{}, fmt.Errorf("pacman -Qu: %w: %s", err, tail(out))
	}
	return p.pending(out), nil
}

func (pacman) pending(out []byte) Pending {
	pkgs := parseArrowList(out)
	sortPackages(pkgs)
	return Pending{
		Packages: pkgs,
		Note:     "Arch has no security-only channel; every update is in one stream",
	}
}

// parseArrowList reads the "name old -> new" listing that both
// checkupdates and `pacman -Qu` print.
func parseArrowList(out []byte) []Package {
	var pkgs []Package
	for _, line := range lines(out) {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[2] != "->" {
			continue
		}
		pkgs = append(pkgs, Package{Name: fields[0], From: fields[1], To: fields[3]})
	}
	return pkgs
}

func (p pacman) UpgradeArgv(security bool, exclude []string, _ Pending) []string {
	if security {
		return nil
	}
	argv := []string{p.Name(), "-Syu", "--noconfirm"}
	for _, e := range exclude {
		argv = append(argv, "--ignore", e)
	}
	return argv
}

func (p pacman) RebootRequired(ctx context.Context, run Runner) (bool, string) {
	return kernelRebootHint(ctx, run)
}

func (p pacman) Installed(ctx context.Context, run Runner, pkg string) (bool, error) {
	// -Q answers about the local database and says nothing at all about a
	// package it does not have, so the status is the whole answer.
	out, err := run.Run(ctx, p.Name(), "-Q", pkg)
	if err == nil {
		return true, nil
	}
	if strings.TrimSpace(string(out)) == "" {
		return false, fmt.Errorf("pacman -Q %s: %w", pkg, err)
	}
	return false, nil
}

func (p pacman) InstallArgv(pkgs []string) []string {
	return append([]string{p.Name(), "-S", "--noconfirm", "--needed"}, pkgs...)
}

func (p pacman) RemoveArgv(pkgs []string, preview bool) []string {
	// -R and not -Rs: taking a package's dependencies with it is a
	// judgement call, and this is not the place to make it on somebody's
	// router.
	if preview {
		return append([]string{p.Name(), "-R", "--print"}, pkgs...)
	}
	return append([]string{p.Name(), "-R", "--noconfirm"}, pkgs...)
}
