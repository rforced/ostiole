// Package timezone sets the zone an appliance reads its clock in and
// answers which zones it can be set to.
//
// Ostiole ships every router in UTC. A firewall's output is timestamps —
// log lines, leases, revisions, schedules — and they are read next to
// the timestamps of everything else on the network, so the zone has to
// be one the operator does not have to work out per router.
package timezone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	// The zone database is compiled in, so a name resolves the same way on
	// a stripped image that never installed tzdata as it does on a full
	// distro. One static binary, no host dependency.
	_ "time/tzdata"
)

// Default is the zone a router runs in until somebody says otherwise. UTC is
// the one zone whose timestamps mean the same thing on every machine, and
// the only one with no hour that happens twice a year.
const Default = "UTC"

// Where the router keeps its zone database and records its current zone.
const (
	zoneinfoDir   = "/usr/share/zoneinfo"
	localtimePath = "/etc/localtime"
	// timezoneFile is Debian's record of the same thing, read when
	// /etc/localtime is a copy of the zone file rather than a link to it.
	timezoneFile = "/etc/timezone"
)

// Valid reports whether name is a zone a clock can be set to. "Local" is
// rejected: it means "whatever the last person to touch this router left
// behind", which is the thing the setting exists to pin down.
func Valid(name string) bool {
	if name == "" || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// Load returns the location for name, falling back to UTC. Callers get a
// usable location instead of an error because a schedule read an hour off
// is better than a schedule that does not run.
func Load(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Runner runs a command. The installer and the daemon each have one
// already; tests pass a fake.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Applier sets the router's zone; the engine calls one after every apply.
type Applier interface {
	Apply(ctx context.Context, zone string) error
}

// Offsetter says the router's offset from UTC, which nft converts a
// schedule's hours with when it loads a ruleset.
type Offsetter interface {
	// Offset is the offset in seconds at t, and when it next changes: the
	// zero time for a zone that never does.
	Offset(t time.Time) (seconds int, next time.Time)
}

// System is the router this process runs on.
type System struct {
	// Run executes timedatectl; nil runs it directly.
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

func (s System) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run.Run(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Apply sets the router's zone. It is idempotent, so callers set the zone
// they want rather than working out whether it changed.
//
// timedatectl is the right tool: it is a bus call to a service outside
// the daemon's sandbox, so it works from a unit that has /etc read-only,
// and it updates everything else that tracks the zone. Where there is no
// bus to call — a container, an image being built — the link is written
// directly instead.
func (s System) Apply(ctx context.Context, zone string) error {
	if zone == "" {
		zone = Default
	}
	if !Valid(zone) {
		return fmt.Errorf("%q is not a timezone", zone)
	}
	out, err := s.run(ctx, "timedatectl", "set-timezone", zone)
	if err == nil {
		return nil
	}
	if linkErr := s.link(zone); linkErr != nil {
		// Both routes named, because which one failed is the whole
		// difference between a missing bus and a missing zone database.
		return fmt.Errorf("set timezone to %s: timedatectl: %w: %s, and the link: %w",
			zone, err, strings.TrimSpace(string(out)), linkErr)
	}
	return nil
}

// link points /etc/localtime at the zone file the way timedatectl would,
// with a relative target so the link still resolves inside a chroot.
func (s System) link(zone string) error {
	target := s.path(filepath.Join(zoneinfoDir, zone))
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("no zone file for %s: %w", zone, err)
	}
	link := s.path(localtimePath)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil { //nolint:gosec // /etc is world-readable
		return err
	}
	rel, err := filepath.Rel(filepath.Dir(link), target)
	if err != nil {
		return err
	}
	// Symlink cannot replace an existing name, so build the new link
	// beside it and rename over the old one.
	tmp := link + ".ostiole"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(rel, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Offset implements Offsetter from the zone file every process on the
// router reads, nft included, rather than from what the configuration says
// the zone should be. With no file the clock is UTC, as it is to libc.
func (s System) Offset(t time.Time) (int, time.Time) {
	loc := time.UTC
	if raw, err := os.ReadFile(s.path(localtimePath)); err == nil {
		if l, err := time.LoadLocationFromTZData("Local", raw); err == nil {
			loc = l
		}
	}
	at := t.In(loc)
	_, offset := at.Zone()
	_, next := at.ZoneBounds()
	return offset, next
}

// Current is the zone the router is set to, read the way every other tool
// reads it: the target of /etc/localtime. A router with no link is UTC.
func (s System) Current() string {
	if target, err := os.Readlink(s.path(localtimePath)); err == nil {
		if _, zone, ok := strings.Cut(filepath.ToSlash(target), "zoneinfo/"); ok && Valid(zone) {
			return zone
		}
	}
	// Distributions that copy the zone file instead of linking to it keep
	// the name here.
	if raw, err := os.ReadFile(s.path(timezoneFile)); err == nil {
		if zone := strings.TrimSpace(string(raw)); Valid(zone) {
			return zone
		}
	}
	return Default
}

// Zones lists the zones this router can be set to, UTC first and the rest in
// order. It reads the table the zone database ships rather than walking
// the directory, which is full of aliases nobody should be offered
// (US/Eastern, Etc/GMT+5). A router with no zone database can only be UTC.
func (s System) Zones() []string {
	for _, table := range []string{"zone1970.tab", "zone.tab"} {
		if found := s.readTable(filepath.Join(zoneinfoDir, table)); len(found) > 0 {
			return append([]string{Default}, found...)
		}
	}
	return []string{Default}
}

// readTable pulls the zone names out of a zone.tab: tab-separated lines
// of country codes, coordinates, zone, and an optional comment.
func (s System) readTable(path string) []string {
	raw, err := os.ReadFile(s.path(path))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		if zone := strings.TrimSpace(fields[2]); zone != "" && zone != Default {
			out = append(out, zone)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
