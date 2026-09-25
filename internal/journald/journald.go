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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/atomicfile"
)

// ConfFile is the drop-in Ostiole writes. journald reads every file in
// journald.conf.d in name order after the main file, so anything set
// here wins over the distribution's defaults.
const ConfFile = "/etc/systemd/journald.conf.d/ostiole.conf"

// mainConfs are where journald's own configuration file lives, and
// finding one is how a router says it has journald at all: a stripped
// image may have systemd and no journal. /etc is where Debian and
// Ubuntu still ship it; systemd's own place for defaults is /usr/lib,
// and RHEL 10 ships it there and nowhere else, so a router that looked
// only in /etc was told it had no journald and left unbounded.
var mainConfs = []string{
	"/etc/systemd/journald.conf",
	"/usr/lib/systemd/journald.conf",
	"/lib/systemd/journald.conf",
}

// DefaultMaxUseGB is the ceiling when the setting says nothing: enough
// to hold weeks of a busy firewall's logging, small next to any disk a
// router is likely to have.
const DefaultMaxUseGB = 10

// MaxMaxUseGB bounds the setting. A terabyte of logs is not a setting,
// it is a typo.
const MaxMaxUseGB = 1024

// DefaultRetentionDays is how long an entry is kept when nobody says
// otherwise.
const DefaultRetentionDays = 90

// Runner runs a command; nil in tests means nothing is restarted.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Applier bounds the journal; the engine calls one after every apply.
type Applier interface {
	Apply(ctx context.Context, maxUseGB, retentionDays int) error
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

// Content renders the drop-in for a ceiling in gigabytes and an age in
// days; zero or less means the default.
func Content(maxUseGB, retentionDays int) string {
	if maxUseGB <= 0 {
		maxUseGB = DefaultMaxUseGB
	}
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	return "# Written by ostiole. Overwritten on install and on every apply.\n" +
		"[Journal]\n" +
		"# Kept on disk, so the log of a crash survives the reboot after it.\n" +
		"Storage=persistent\n" +
		fmt.Sprintf("SystemMaxUse=%dG\n", maxUseGB) +
		"# Nothing about the people behind this router is kept for longer.\n" +
		fmt.Sprintf("MaxRetentionSec=%dday\n", retentionDays) +
		// journald deletes whole files, so a file still being written to
		// when the retention runs out holds entries past it.
		"MaxFileSec=1week\n"
}

// Apply writes the drop-in and restarts journald when it changed. A
// router with no journald is left alone, and an unchanged file restarts
// nothing: journald is restarted only when there is something new for it
// to read.
func (s System) Apply(ctx context.Context, maxUseGB, retentionDays int) error {
	if !s.Present() {
		return nil
	}
	path := s.path(ConfFile)
	want := Content(maxUseGB, retentionDays)
	if have, err := os.ReadFile(path); err == nil && string(have) == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // system config dir
		return err
	}
	if err := atomicfile.Write(path, []byte(want), 0o644); err != nil {
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
	for _, conf := range mainConfs {
		if _, err := os.Stat(s.path(conf)); err == nil {
			return true
		}
	}
	return false
}
