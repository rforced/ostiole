// Package journald bounds what the system journal keeps on a router and
// makes sure it keeps it across a reboot.
//
// An appliance nobody logs into has two ways to fail here. A journal that
// only lives in /run is gone with the crash it would have explained, so
// storage is made persistent. And a journal with no ceiling of its own
// takes whatever journald's default allows of the disk it shares with the
// configuration, the revisions and the package cache, so it gets one.
package journald

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfFile is the drop-in Ostiole writes. journald reads every file in
// journald.conf.d in name order after the main file, so anything set
// here wins over the distribution's defaults.
const ConfFile = "/etc/systemd/journald.conf.d/ostiole.conf"

// mainConf is journald's own configuration file, whose presence is how a
// router says it has journald at all. Alpine does not.
const mainConf = "/etc/systemd/journald.conf"

// DefaultMaxUseGB is the ceiling when the setting says nothing: enough
// to hold weeks of a busy firewall's logging, small next to any disk a
// router is likely to have.
const DefaultMaxUseGB = 10

// MaxMaxUseGB bounds the setting. A terabyte of logs is not a setting,
// it is a typo.
const MaxMaxUseGB = 1024

// Runner runs a command; nil in tests means nothing is restarted.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Applier bounds the journal; the engine calls one after every apply.
type Applier interface {
	Apply(ctx context.Context, maxUseGB int) error
}

// System is the router this process runs on.
type System struct {
	// Run executes systemctl; nil restarts nothing, which is what a test
	// wants and what a router without systemd gets.
	Run Runner
	// Root is the filesystem root, overridden in tests.
	Root string
}

func (s System) path(p string) string {
	if s.Root == "" {
		return p
	}
	return filepath.Join(s.Root, p)
}

// Content renders the drop-in for a ceiling in gigabytes; zero or less
// means the default.
func Content(maxUseGB int) string {
	if maxUseGB <= 0 {
		maxUseGB = DefaultMaxUseGB
	}
	return "# Written by ostiole. Overwritten on install and on every apply.\n" +
		"[Journal]\n" +
		"# Kept on disk, so the log of a crash survives the reboot after it.\n" +
		"Storage=persistent\n" +
		fmt.Sprintf("SystemMaxUse=%dG\n", maxUseGB)
}

// Apply writes the drop-in and restarts journald when it changed. A
// router with no journald is left alone, and an unchanged file restarts
// nothing: journald is restarted only when there is something new for it
// to read.
func (s System) Apply(ctx context.Context, maxUseGB int) error {
	if _, err := os.Stat(s.path(mainConf)); err != nil {
		return nil
	}
	path := s.path(ConfFile)
	want := Content(maxUseGB)
	if have, err := os.ReadFile(path); err == nil && string(have) == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // system config dir
		return err
	}
	if err := writeFile(path, want); err != nil {
		return err
	}
	if s.Run == nil {
		return nil
	}
	// Restarting journald is safe on any systemd Ostiole runs on: since
	// v246 the log streams of running services survive it.
	if out, err := s.Run.Run(ctx, "systemctl", "restart", "systemd-journald"); err != nil {
		return fmt.Errorf("restart systemd-journald: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Present reports whether this router has journald.
func (s System) Present() bool {
	_, err := os.Stat(s.path(mainConf))
	return err == nil
}

// writeFile writes atomically, so journald never reads half a file.
func writeFile(path, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return errors.Join(err)
	}
	return nil
}
