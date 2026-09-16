package install

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		case "cat":
			if f.enabled[args[1]] == "" {
				return "No files found for " + args[1], os.ErrNotExist
			}
			return "[Unit]", nil
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

	run := &fakeRunner{}
	sysctlFile := filepath.Join(t.TempDir(), "90-ostiole.conf")
	rep, err := Install(context.Background(), sc, lay, Options{Source: src, Listen: ":8443", Run: run, SysctlFile: sysctlFile}, log)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OpenedIn != "firewalld" {
		t.Errorf("OpenedIn = %q, want firewalld", rep.OpenedIn)
	}
	if raw, err := os.ReadFile(sysctlFile); err != nil || !strings.Contains(string(raw), "ip_forward = 1") {
		t.Errorf("sysctl file = %q, %v", raw, err)
	}
	if len(run.calls) != 2 || strings.Join(run.calls[0], " ") != "firewall-cmd --add-port=8443/tcp" || strings.Join(run.calls[1], " ") != "firewall-cmd --permanent --add-port=8443/tcp" {
		t.Errorf("firewall-cmd calls = %v", run.calls)
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
	if !strings.Contains(string(daemon), "--network-backend auto") || !strings.Contains(string(daemon), "--listen :8443") {
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
	if _, err := Install(context.Background(), sc, lay, Options{Source: lay.Binary(), Run: &fakeRunner{}, SysctlFile: "-"}, log); err != nil {
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

func TestUnits(t *testing.T) {
	t.Parallel()
	units := Units(DefaultLayout(), Options{Listen: ":443"})
	d := units[DaemonUnit]
	if !strings.Contains(d, "--network-backend auto") || !strings.Contains(d, "ReadWritePaths=/etc/ostiole /etc/systemd/network /usr/local/bin -/etc/dnsmasq.d -/etc/unbound -/etc/resolv.conf -/etc/ppp") {
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

type fakeRunner struct {
	calls [][]string
	after func()
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.after != nil {
		f.after()
	}
	return nil, nil
}

func TestEnsureNetworkd(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)

	present := &fakeSystemctl{enabled: map[string]string{NetworkdUnit: "disabled"}}
	if err := EnsureNetworkd(context.Background(), present, &fakeRunner{}, "dnf", log); err != nil {
		t.Fatalf("present: %v", err)
	}

	missing := &fakeSystemctl{enabled: map[string]string{}}
	run := &fakeRunner{}
	run.after = func() { missing.enabled[NetworkdUnit] = "disabled" } // dnf "installs" it
	if err := EnsureNetworkd(context.Background(), missing, run, "dnf", log); err != nil {
		t.Fatalf("dnf path: %v", err)
	}
	if len(run.calls) != 1 || run.calls[0][0] != "dnf" {
		t.Errorf("runner calls = %v", run.calls)
	}

	if err := EnsureNetworkd(context.Background(), &fakeSystemctl{enabled: map[string]string{}}, &fakeRunner{}, "apk", log); err == nil {
		t.Error("expected error for unsupported package manager")
	}
}

func TestNetworkTakeover(t *testing.T) {
	t.Parallel()
	sc := &fakeSystemctl{}
	if err := NetworkTakeover(context.Background(), sc, []string{"NetworkManager", "NetworkManager-wait-online"}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"disable", "--now", "NetworkManager.service"}, {"mask", "NetworkManager.service"},
		{"disable", "--now", "NetworkManager-wait-online.service"}, {"mask", "NetworkManager-wait-online.service"},
		{"enable", "--now", NetworkdSocket, NetworkdUnit},
	}
	for i, w := range want {
		if strings.Join(sc.calls[i], " ") != strings.Join(w, " ") {
			t.Fatalf("call %d = %v, want %v (all: %v)", i, sc.calls[i], w, sc.calls)
		}
	}
}

func TestNetworkRevertAndRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if rec, err := LoadTakeoverRecord(dir); err != nil || rec != nil {
		t.Fatalf("empty record = %v, %v", rec, err)
	}
	if err := SaveTakeoverRecord(dir, TakeoverRecord{Managers: []string{"NetworkManager", "NetworkManager-wait-online"}}); err != nil {
		t.Fatal(err)
	}
	rec, err := LoadTakeoverRecord(dir)
	if err != nil || len(rec.Managers) != 2 {
		t.Fatalf("record = %v, %v", rec, err)
	}
	sc := &fakeSystemctl{}
	if err := NetworkRevert(context.Background(), sc, rec.Managers, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"disable", "--now", NetworkdSocket, NetworkdUnit},
		{"unmask", "NetworkManager.service"}, {"enable", "--now", "NetworkManager.service"},
		{"unmask", "NetworkManager-wait-online.service"}, {"enable", "NetworkManager-wait-online.service"},
	}
	for i, w := range want {
		if strings.Join(sc.calls[i], " ") != strings.Join(w, " ") {
			t.Fatalf("call %d = %v, want %v", i, sc.calls[i], w)
		}
	}
	run := &fakeRunner{}
	if err := ScheduleNetworkRevert(context.Background(), run, "/usr/local/bin/ostiole", "/etc/ostiole", 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(run.calls[len(run.calls)-1], " ")
	if !strings.Contains(last, "systemd-run") || !strings.Contains(last, "--on-active=180") || !strings.Contains(last, "AccuracySec=1s") || !strings.HasSuffix(last, "takeover --network --revert --in-unit") {
		t.Errorf("systemd-run call = %q", last)
	}
	if CancelNetworkRevert(context.Background(), &fakeRunner{}) {
		t.Error("cancel reported an armed timer on a fake that never arms one")
	}
}

func TestServiceBinaryPrefersInstalled(t *testing.T) {
	t.Parallel()
	lay := tempLayout(t)
	self, _ := os.Executable()
	if got, err := ServiceBinary(lay); err != nil || got != self {
		t.Fatalf("without install: %q, %v; want the running executable %q", got, err, self)
	}
	if err := os.MkdirAll(lay.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lay.Binary(), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ServiceBinary(lay); err != nil || got != lay.Binary() {
		t.Fatalf("with install: %q, %v; want %q", got, err, lay.Binary())
	}
}

func TestInSystemBinDirAndPackageManaged(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{"/usr/bin/ostiole": true, "/usr/local/bin/ostiole": true, "/root/ostiole": false, "/tmp/x/ostiole": false} {
		if got := InSystemBinDir(path); got != want {
			t.Errorf("InSystemBinDir(%q) = %v", path, got)
		}
	}
	if !PackageManaged("/usr/bin/ostiole") || PackageManaged("/usr/local/bin/ostiole") {
		t.Error("PackageManaged wrong")
	}
}
