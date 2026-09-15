package install

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSystemctl struct {
	calls   [][]string
	enabled map[string]string
	active  map[string]string
}

func (f *fakeSystemctl) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if len(args) == 2 {
		switch args[0] {
		case "is-enabled":
			v, ok := f.enabled[args[1]]
			if !ok {
				return "not-found", os.ErrNotExist
			}
			return v, nil
		case "is-active":
			return f.active[args[1]], nil
		}
	}
	return "", nil
}

func (f *fakeSystemctl) has(args ...string) bool {
	for _, c := range f.calls {
		if strings.Join(c, " ") == strings.Join(args, " ") {
			return true
		}
	}
	return false
}

func tempLayout(t *testing.T) Layout {
	t.Helper()
	root := t.TempDir()
	return Layout{BinDir: filepath.Join(root, "bin"), UnitDir: filepath.Join(root, "units"), ConfigDir: filepath.Join(root, "etc")}
}

func TestInstallAndUninstall(t *testing.T) {
	t.Parallel()
	lay := tempLayout(t)
	src := filepath.Join(t.TempDir(), "ostiole-src")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho fake\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sc := &fakeSystemctl{
		enabled: map[string]string{"firewalld.service": "enabled", "NetworkManager.service": "enabled", "ufw.service": "masked"},
		active:  map[string]string{"firewalld.service": "active", "NetworkManager.service": "active", "ufw.service": "inactive"},
	}
	log := slog.New(slog.DiscardHandler)

	rep, err := Install(context.Background(), sc, lay, Options{Source: src, Listen: ":8443"}, log)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(lay.Binary()); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("binary = %v, %v", info, err)
	}
	if info, err := os.Stat(lay.ConfigDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir = %v, %v", info, err)
	}
	for _, u := range []string{FirewallUnit, DaemonUnit} {
		raw, err := os.ReadFile(filepath.Join(lay.UnitDir, u))
		if err != nil {
			t.Fatalf("%s missing: %v", u, err)
		}
		if !strings.Contains(string(raw), lay.Binary()) || !strings.Contains(string(raw), lay.ConfigDir) {
			t.Errorf("%s does not reference layout:\n%s", u, raw)
		}
	}
	daemon, _ := os.ReadFile(filepath.Join(lay.UnitDir, DaemonUnit))
	if !strings.Contains(string(daemon), "--network-backend none") || !strings.Contains(string(daemon), "--listen :8443") {
		t.Errorf("daemon unit flags wrong:\n%s", daemon)
	}
	if !sc.has("daemon-reload") || !sc.has("enable", FirewallUnit) || !sc.has("enable", "--now", DaemonUnit) {
		t.Errorf("systemctl calls = %v", sc.calls)
	}
	if len(rep.Competitors) != 3 {
		t.Fatalf("competitors = %+v", rep.Competitors)
	}
	conflicting := 0
	for _, c := range rep.Competitors {
		if c.Conflicts() {
			conflicting++
		}
	}
	if conflicting != 2 {
		t.Errorf("conflicting = %d, want 2 (firewalld, NetworkManager)", conflicting)
	}

	// Re-install from the installed path is a no-op copy.
	if _, err := Install(context.Background(), sc, lay, Options{Source: lay.Binary()}, log); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(context.Background(), sc, lay, true, log); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(lay.UnitDir, DaemonUnit), filepath.Join(lay.UnitDir, FirewallUnit), lay.Binary(), lay.ConfigDir} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still exists after purge", p)
		}
	}
	if !sc.has("disable", "--now", DaemonUnit) {
		t.Errorf("daemon not disabled: %v", sc.calls)
	}
}

func TestUnitsWithNetwork(t *testing.T) {
	t.Parallel()
	units := Units(DefaultLayout(), Options{Listen: ":443", ManageNetwork: true})
	d := units[DaemonUnit]
	if !strings.Contains(d, "--network-backend networkd") || !strings.Contains(d, "ReadWritePaths=/etc/ostiole /etc/systemd/network") {
		t.Errorf("daemon unit:\n%s", d)
	}
	f := units[FirewallUnit]
	if !strings.Contains(f, "Before=network-pre.target") || !strings.Contains(f, "ConditionPathExists=/etc/ostiole/ruleset.nft") {
		t.Errorf("firewall unit:\n%s", f)
	}
}

func TestTakeover(t *testing.T) {
	t.Parallel()
	sc := &fakeSystemctl{}
	if err := Takeover(context.Background(), sc, []string{"firewalld", "ufw"}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{{"disable", "--now", "firewalld.service"}, {"mask", "firewalld.service"}, {"disable", "--now", "ufw.service"}, {"mask", "ufw.service"}} {
		if !sc.has(want...) {
			t.Errorf("missing call %v in %v", want, sc.calls)
		}
	}
}
