// Package logging caps how much each daemon Ostiole runs writes to the
// journal.
//
// Two mechanisms, both driven by one level. A daemon with a switch of its
// own is rendered quiet by whatever writes its configuration: dnsmasq's
// quiet-dhcp, unbound's verbosity, hostapd's logger level. On top of that
// every Ostiole-owned unit gets a LogLevelMax= drop-in, so the journal
// refuses anything above the level whether or not the daemon could have
// been told. Nothing writes a log file of its own; everything goes to the
// journal, which has an age and a size.
package logging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// DropInName is the file written under each unit's drop-in directory.
const DropInName = "ostiole-logging.conf"

// UnitDir is where systemd reads unit drop-ins from.
const UnitDir = "/etc/systemd/system"

// Priority is the syslog level name journald takes in LogLevelMax.
func Priority(l model.LogLevel) string {
	switch l {
	case model.LogError:
		return "err"
	case model.LogInfo:
		return "info"
	case model.LogDebug:
		return "debug"
	default:
		return "warning"
	}
}

// Slog is the same level for Ostiole's own logger.
func Slog(l model.LogLevel) slog.Level {
	switch l {
	case model.LogError:
		return slog.LevelError
	case model.LogInfo:
		return slog.LevelInfo
	case model.LogDebug:
		return slog.LevelDebug
	default:
		return slog.LevelWarn
	}
}

// Unit is one Ostiole-owned unit whose journal stream is capped.
type Unit struct {
	Name string
	// StderrIsDuplicate marks a daemon that prints to stderr what it also
	// sends to syslog. Its stderr copy is sunk to debug so the syslog copy
	// keeps its real priority and nothing is logged twice.
	StderrIsDuplicate bool
	// Templated units are restarted by pattern: systemd will not act on
	// the template itself.
	Templated bool
}

// Dir is the drop-in directory for this unit under root.
func (u Unit) Dir(root string) string {
	return filepath.Join(root, UnitDir, u.Name+".d")
}

// Pattern is what systemctl is given to restart this unit: the instances
// of a template, or the unit itself.
func (u Unit) Pattern() string {
	if !u.Templated {
		return u.Name
	}
	return strings.Replace(u.Name, "@.service", "@*.service", 1)
}

// Units are the Ostiole-owned units whose journal stream is capped. The
// names are written out rather than taken from internal/services, which
// both this package and internal/install would otherwise have to import
// in a circle; TestLoggingCapsEveryServiceUnit keeps the two lists
// together.
//
// tailscaled is not here: it prints everything at one priority, so a cap
// would hide its errors along with the rest, and what it names is peers
// rather than the people on the LAN. Neither is the reverse proxy, for
// the same reason: its level is set in its own configuration instead, and
// a cap would take its WAF events with the rest.
var Units = []Unit{
	{Name: "ostiole-dnsmasq.service"},
	{Name: "ostiole-unbound.service"},
	{Name: "ostiole-miniupnpd.service", StderrIsDuplicate: true},
	{Name: "ostiole-pppoe@.service", StderrIsDuplicate: true, Templated: true},
	{Name: "ostiole-hostapd@.service", Templated: true},
	{Name: "ostiole-chronyd.service"},
}

// Runner runs a command; nil means nothing is restarted.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Applier caps the daemons; the engine calls one after every apply.
type Applier interface {
	Apply(ctx context.Context, level model.LogLevel) error
}

// System writes the drop-ins and restarts what changed.
type System struct {
	// Run executes systemctl; nil restarts nothing, which is what a test
	// wants and what a router without systemd gets.
	Run Runner
	// Root is the filesystem root, overridden in tests.
	Root string
	// Slog is Ostiole's own level; nil leaves it alone, which is what a
	// --log-level on the command line asks for.
	Slog *slog.LevelVar
}

// Content renders a unit's drop-in.
func Content(u Unit, level model.LogLevel) string {
	var b strings.Builder
	b.WriteString("# Written by ostiole. Overwritten on every apply.\n")
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "LogLevelMax=%s\n", Priority(level))
	if u.StderrIsDuplicate {
		// This daemon says everything twice: once to syslog with a real
		// priority and once to stderr. Sinking the stderr copy to debug
		// leaves one legible copy at the level it deserves.
		b.WriteString("SyslogLevel=debug\n")
	}
	return b.String()
}

// Apply writes a drop-in for each unit and restarts the ones whose file
// changed. A restart is the only way the cap reaches a daemon that is
// already running, so it happens when the level changes and not
// otherwise.
func (s System) Apply(ctx context.Context, level model.LogLevel) error {
	if level == "" {
		level = model.LogWarning
	}
	if s.Slog != nil {
		s.Slog.Set(Slog(level))
	}
	var changed []Unit
	var errs []error
	for _, u := range Units {
		dir := u.Dir(s.Root)
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			// The unit is not set up on this router, so there is nothing
			// to cap. `ostiole install` creates the directory for each
			// unit it writes.
			continue
		}
		path := filepath.Join(dir, DropInName)
		want := Content(u, level)
		if have, err := os.ReadFile(path); err == nil && string(have) == want {
			continue
		}
		if err := writeFile(path, want); err != nil {
			errs = append(errs, fmt.Errorf("write %s: %w", path, err))
			continue
		}
		changed = append(changed, u)
	}
	if len(changed) == 0 || s.Run == nil {
		return errors.Join(errs...)
	}
	if out, err := s.Run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return errors.Join(append(errs, fmt.Errorf("systemctl daemon-reload: %w: %s",
			err, bytes.TrimSpace(out)))...)
	}
	for _, u := range changed {
		// Every unit has a drop-in directory, because install creates them
		// all, but only the ones that were set up have a unit file, and
		// systemctl refuses to restart what it cannot find.
		if _, err := s.Run.Run(ctx, "systemctl", "cat", u.Name); err != nil {
			continue
		}
		// try-restart leaves a unit that is not running alone, which is
		// most of them on most routers.
		if out, err := s.Run.Run(ctx, "systemctl", "try-restart", u.Pattern()); err != nil {
			errs = append(errs, fmt.Errorf("restart %s: %w: %s", u.Pattern(), err, bytes.TrimSpace(out)))
		}
	}
	return errors.Join(errs...)
}

// writeFile replaces path atomically, so systemd never reads half a file.
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
	return os.Rename(name, path)
}
