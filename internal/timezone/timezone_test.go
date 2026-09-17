package timezone

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValid(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"UTC", "Europe/Berlin", "America/New_York"} {
		if !Valid(name) {
			t.Errorf("Valid(%q) = false", name)
		}
	}
	// Empty means "say nothing", not a zone; "Local" is whatever the last
	// person left behind; the rest are typos and traversal attempts.
	for _, name := range []string{"", "Local", "Europe/Berlín", "Mars/Olympus", "../../etc/passwd", "/etc/localtime"} {
		if Valid(name) {
			t.Errorf("Valid(%q) = true", name)
		}
	}
}

func TestLoadFallsBackToUTC(t *testing.T) {
	t.Parallel()
	if got := Load("Europe/Berlin"); got.String() != "Europe/Berlin" {
		t.Errorf("Load(Europe/Berlin) = %v", got)
	}
	for _, name := range []string{"", "nonsense"} {
		if got := Load(name); got != time.UTC {
			t.Errorf("Load(%q) = %v, want UTC", name, got)
		}
	}
}

// fakeRunner records commands and fails on demand.
type fakeRunner struct {
	calls [][]string
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return []byte("output"), f.err
}

func TestApplyUsesTimedatectl(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{}
	if err := (System{Run: run}).Apply(context.Background(), "Europe/Berlin"); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 1 || strings.Join(run.calls[0], " ") != "timedatectl set-timezone Europe/Berlin" {
		t.Errorf("calls = %v", run.calls)
	}
	// An empty zone is the default rather than an error, so a caller can
	// pass a configuration straight through.
	run = &fakeRunner{}
	if err := (System{Run: run}).Apply(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 1 || run.calls[0][2] != "UTC" {
		t.Errorf("calls = %v", run.calls)
	}
}

func TestApplyRejectsNonsense(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{}
	if err := (System{Run: run}).Apply(context.Background(), "Mars/Olympus"); err == nil {
		t.Fatal("want an error for a zone that does not exist")
	}
	if len(run.calls) != 0 {
		t.Errorf("ran %v for a zone that does not exist", run.calls)
	}
}

// Where there is no bus to call, the link is written instead.
func TestApplyFallsBackToTheLink(t *testing.T) {
	t.Parallel()
	root := fakeRoot(t)
	run := &fakeRunner{err: errors.New("Failed to connect to bus")}
	sys := System{Run: run, Root: root}
	if err := sys.Apply(context.Background(), "Europe/Berlin"); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(root, "etc/localtime"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "../usr/share/zoneinfo/Europe/Berlin" {
		t.Errorf("link = %q", target)
	}
	if got := sys.Current(); got != "Europe/Berlin" {
		t.Errorf("Current() = %q", got)
	}
	// Setting it again replaces the link rather than failing on the name
	// that is already there.
	if err := sys.Apply(context.Background(), "UTC"); err != nil {
		t.Fatal(err)
	}
	if got := sys.Current(); got != "UTC" {
		t.Errorf("Current() after second apply = %q", got)
	}
}

// The fallback cannot invent a zone file, and says so along with why
// timedatectl was not used.
func TestApplyReportsBothFailures(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{err: errors.New("exit status 1")}
	err := (System{Run: run, Root: t.TempDir()}).Apply(context.Background(), "Europe/Berlin")
	if err == nil {
		t.Fatal("want an error when neither route works")
	}
	// Which route failed is the difference between a missing bus and a
	// missing zone database, so both are named.
	for _, want := range []string{"Europe/Berlin", "exit status 1", "no zone file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
}

func TestCurrent(t *testing.T) {
	t.Parallel()
	// A router with nothing to read runs in UTC.
	if got := (System{Root: t.TempDir()}).Current(); got != Default {
		t.Errorf("Current() with no files = %q", got)
	}
	// Distributions that copy the zone file instead of linking to it keep
	// the name in /etc/timezone.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/timezone"), []byte("Asia/Tokyo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := (System{Root: root}).Current(); got != "Asia/Tokyo" {
		t.Errorf("Current() = %q", got)
	}
}

func TestZones(t *testing.T) {
	t.Parallel()
	root := fakeRoot(t)
	zones := (System{Root: root}).Zones()
	// UTC leads, because it is the default and nobody should have to scroll
	// to it.
	if len(zones) == 0 || zones[0] != Default {
		t.Fatalf("zones = %v", zones)
	}
	want := map[string]bool{"Europe/Berlin": true, "Asia/Tokyo": true}
	for _, z := range zones[1:] {
		delete(want, z)
	}
	if len(want) != 0 {
		t.Errorf("zones %v missing %v", zones, want)
	}
	// The comment column is not a zone, and neither is the header.
	for _, z := range zones {
		if strings.HasPrefix(z, "#") || strings.Contains(z, " ") {
			t.Errorf("zones contains %q", z)
		}
	}
	// A router with no zone database can only be UTC.
	if got := (System{Root: t.TempDir()}).Zones(); len(got) != 1 || got[0] != Default {
		t.Errorf("zones without a database = %v", got)
	}
}

// fakeRoot is a filesystem root with just enough zone database to work
// against: two zone files and the table that lists them.
func fakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, zone := range []string{"UTC", "Europe/Berlin", "Asia/Tokyo"} {
		path := filepath.Join(root, "usr/share/zoneinfo", zone)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("TZif"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	table := "# codes\tcoordinates\tTZ\tcomments\n" +
		"DE\t+5230+01322\tEurope/Berlin\n" +
		"JP\t+353916+1394441\tAsia/Tokyo\tthe comment column\n" +
		"XX\tshort line\n" +
		"\n"
	if err := os.WriteFile(filepath.Join(root, "usr/share/zoneinfo/zone1970.tab"), []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}
