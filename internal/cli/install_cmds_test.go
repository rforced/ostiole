package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/timezone"
)

// recordingSystemctl answers every call and keeps what was asked.
type recordingSystemctl struct{ calls []string }

func (r *recordingSystemctl) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, strings.Join(args, " "))
	return "", nil
}

// A self-update runs `install --units-only` with nobody at the console:
// the units, the directories they open and a reload, and nothing enabled,
// restarted or asked.
func TestInstallUnitsOnlyWritesTheUnitsAndNothingElse(t *testing.T) {
	t.Parallel()
	if newInstallCmd(&globals{}).Flags().Lookup("units-only") == nil {
		t.Fatal("install has no --units-only")
	}
	root := t.TempDir()
	lay := install.Layout{BinDir: filepath.Join(root, "bin"), UnitDir: filepath.Join(root, "units"),
		ConfigDir: filepath.Join(root, "etc"), BackupDir: filepath.Join(root, "backups")}
	// The UI stays where the installed unit serves it.
	old := install.Units(lay, install.Options{Listen: "10.0.0.1:9000"})[install.DaemonUnit]
	if err := os.MkdirAll(lay.UnitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lay.UnitDir, install.DaemonUnit), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	sc := &recordingSystemctl{}
	services := 0
	var out bytes.Buffer
	err := writeUnitsOnly(t.Context(), &out, sc, lay, install.Options{}, func(context.Context) error {
		services++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{install.FirewallUnit, install.DaemonUnit} {
		raw, err := os.ReadFile(filepath.Join(lay.UnitDir, u))
		if err != nil || !strings.Contains(string(raw), lay.Binary()) {
			t.Errorf("%s = %q, %v", u, raw, err)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(lay.UnitDir, install.DaemonUnit)); !strings.Contains(string(raw), "--listen 10.0.0.1:9000") {
		t.Errorf("daemon unit moved the UI:\n%s", raw)
	}
	if info, err := os.Stat(lay.BackupDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("backup dir = %v, %v", info, err)
	}
	if !slices.Equal(sc.calls, []string{"daemon-reload"}) {
		t.Errorf("systemctl calls = %q, want only daemon-reload", sc.calls)
	}
	if services != 1 {
		t.Errorf("service units written %d times", services)
	}
	if _, err := os.Stat(lay.ConfigDir); err == nil {
		t.Error("the configuration directory was made")
	}
	if got := out.String(); got != "wrote ostiole-firewall.service, ostiole.service and the service units\n" {
		t.Errorf("output = %q", got)
	}
}

// Every release up to 1.7.2 restarts into an update with a bare `install`
// that nobody can answer. On a router that has its units, that call gets
// the units; any other install with nobody to ask still wants --yes.
func TestAnOldReleasesUpdateRestartGetsTheUnits(t *testing.T) {
	t.Parallel()
	lay := install.Layout{UnitDir: t.TempDir()}
	if oldUpdateRestart(false, false, install.Options{}, false, lay) {
		t.Error("a router without units was taken for an update restart")
	}
	if err := os.WriteFile(filepath.Join(lay.UnitDir, install.DaemonUnit), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !oldUpdateRestart(false, false, install.Options{}, false, lay) {
		t.Error("the old restart's bare install was not taken for the units")
	}
	for name, c := range map[string]struct {
		yes, dryRun, interactive bool
		opts                     install.Options
	}{
		"--yes":      {yes: true},
		"--dry-run":  {dryRun: true},
		"a terminal": {interactive: true},
		"--listen":   {opts: install.Options{Listen: ":9443"}},
		"--timezone": {opts: install.Options{Timezone: "UTC"}},
	} {
		if oldUpdateRestart(c.yes, c.dryRun, c.opts, c.interactive, lay) {
			t.Errorf("an install with %s was taken for an update restart", name)
		}
	}
}

// `ostiole repair` runs the install again, so anything the install takes
// from a default rather than from the configuration is a setting the
// operator chose and the router quietly puts back.
func TestInstallTakesSettingsFromTheConfiguration(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	cfg.System.Logging.MaxUseGB = 25
	cfg.System.Logging.RetentionDays = 30
	cfg.System.Timezone = "Europe/London"

	if gb, days := journalLimits(cfg); gb != 25 || days != 30 {
		t.Errorf("journal limits = %dG, %d days, want the configured 25G, 30 days", gb, days)
	}
	if got := installZone("", cfg); got != "Europe/London" {
		t.Errorf("zone = %q, want the configured one", got)
	}

	// A router with no configuration yet gets the defaults.
	if gb, days := journalLimits(nil); gb != journald.DefaultMaxUseGB || days != journald.DefaultRetentionDays {
		t.Errorf("journal limits = %dG, %d days, want the defaults", gb, days)
	}
	if got := installZone("", nil); got != timezone.Default {
		t.Errorf("zone = %q, want %q", got, timezone.Default)
	}

	// A configuration that says nothing about them still gets defaults.
	empty := &model.Config{}
	if gb, days := journalLimits(empty); gb != journald.DefaultMaxUseGB || days != journald.DefaultRetentionDays {
		t.Errorf("journal limits = %dG, %d days, want the defaults", gb, days)
	}

	// The flag wins over both, and "-" still means "leave the clock".
	if got := installZone("Asia/Tokyo", cfg); got != "Asia/Tokyo" {
		t.Errorf("zone = %q, want the flag", got)
	}
	if got := installZone("-", cfg); got != "-" {
		t.Errorf("zone = %q, want the clock left alone", got)
	}
}
