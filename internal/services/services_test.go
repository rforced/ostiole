package services

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadConfig(t *testing.T, path string) *model.Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

type fakeCmd struct {
	calls     [][]string
	installed bool
	active    bool
}

func (f *fakeCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "systemctl" && len(args) >= 2 {
		switch args[0] {
		case "cat":
			if f.installed {
				return []byte("[Unit]"), nil
			}
			return nil, errors.New("no such unit")
		case "is-active":
			if f.active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), errors.New("exit 3")
		case "restart", "enable":
			f.active = true
		case "disable":
			f.active = false
		}
	}
	return nil, nil
}

func (f *fakeCmd) has(parts ...string) bool {
	for _, c := range f.calls {
		if strings.Join(c, " ") == strings.Join(parts, " ") {
			return true
		}
	}
	return false
}

func TestRenderGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := &Dnsmasq{Dir: DefaultDir, Leases: LeaseFile, Resolv: ResolvConf}
			files, err := d.Render(loadConfig(t, in))
			if err != nil {
				t.Fatal(err)
			}
			got := files.String()
			golden := strings.TrimSuffix(in, ".json") + ".dnsmasq"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("mismatch (run with -update to accept)\n--- got ---\n%s", got)
			}
		})
	}
}

func TestRenderDisabledOnlyWritesResolv(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/dhcp-only.json")
	cfg.Services = model.Services{}
	d := &Dnsmasq{Resolv: "/etc/resolv.conf"}
	files, err := d.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !strings.Contains(files["resolv.conf"], "nameserver 8.8.8.8") {
		t.Errorf("files = %v", files)
	}
	d.Resolv = ""
	files, _ = d.Render(cfg)
	if len(files) != 0 {
		t.Errorf("files without resolv management = %v", files.Names())
	}
}

func TestApplyStartsStopsAndReverts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resolv := filepath.Join(t.TempDir(), "resolv.conf")
	cmd := &fakeCmd{installed: true}
	d := &Dnsmasq{Dir: dir, Leases: filepath.Join(dir, "leases"), Resolv: resolv, Cmd: cmd}

	cfg := loadConfig(t, "testdata/full.json")
	files, err := d.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "enable", Unit) || !cmd.has("systemctl", "restart", Unit) {
		t.Errorf("unit not started: %v", cmd.calls)
	}
	if raw, _ := os.ReadFile(resolv); !strings.Contains(string(raw), "nameserver 127.0.0.1") {
		t.Errorf("resolv.conf = %q", raw)
	}
	snap, _ := d.Snapshot()
	if snap.String() != files.String() {
		t.Errorf("snapshot differs:\n%s", snap.String())
	}

	// Same files again: no restart.
	cmd.calls = nil
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 0 {
		t.Errorf("unchanged apply ran %v", cmd.calls)
	}

	// Disable: unit stopped, generated files gone.
	cfg.Services = model.Services{}
	off, _ := d.Render(cfg)
	cmd.calls = nil
	if err := d.Apply(context.Background(), off); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "disable", "--now", Unit) {
		t.Errorf("unit not stopped: %v", cmd.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "ostiole.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Error("ostiole.conf left behind")
	}

	// Revert to the snapshot restarts the unit.
	cmd.calls = nil
	if err := d.Apply(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "restart", Unit) {
		t.Errorf("revert did not restart: %v", cmd.calls)
	}
}

func TestApplyNeedsSetup(t *testing.T) {
	t.Parallel()
	d := &Dnsmasq{Dir: t.TempDir(), Cmd: &fakeCmd{installed: false}}
	files, _ := d.Render(loadConfig(t, "testdata/full.json"))
	err := d.Apply(context.Background(), files)
	if err == nil || !strings.Contains(err.Error(), "services setup") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseLeases(t *testing.T) {
	t.Parallel()
	in := strings.NewReader("1758000000 aa:bb:cc:dd:ee:ff 192.168.1.101 laptop 01:aa:bb:cc:dd:ee:ff\n" +
		"0 11:22:33:44:55:66 192.168.1.50 nas *\n" +
		"duid 00:01:00:01\n" +
		"1758000100 123456 2001:db8:1::abc phone 00:01:00:01:2f:aa:bb\n" +
		"garbage\n")
	leases, err := ParseLeases(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 3 {
		t.Fatalf("leases = %+v", leases)
	}
	// Sorted by family, then numerically: .50 before .101, IPv6 last.
	if !leases[0].Static || leases[0].IP != "192.168.1.50" || leases[0].ClientID != "" || leases[0].Family != 4 {
		t.Errorf("lease 0 = %+v", leases[0])
	}
	if leases[1].IP != "192.168.1.101" || leases[1].Hostname != "laptop" || leases[1].Static || leases[1].Expires.IsZero() {
		t.Errorf("lease 1 = %+v", leases[1])
	}
	// IPv6 leases carry an IAID where a MAC would be, and a DUID at the end.
	v6 := leases[2]
	if v6.Family != 6 || v6.IP != "2001:db8:1::abc" || v6.MAC != "" || v6.Hostname != "phone" ||
		v6.ClientID != "00:01:00:01:2f:aa:bb" {
		t.Errorf("lease 2 = %+v", v6)
	}
	d := &Dnsmasq{Leases: filepath.Join(t.TempDir(), "none")}
	if l, err := d.ReadLeases(); err != nil || len(l) != 0 {
		t.Errorf("missing lease file: %v, %v", l, err)
	}
}

type fakeRunner struct{ calls [][]string }

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "systemctl" && len(args) == 2 && args[0] == "cat" {
		return []byte("[Unit]"), nil
	}
	return nil, nil
}

func TestSetupWritesUnitAndMasksCompetitors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	resolv := filepath.Join(root, "resolv.conf")
	target := filepath.Join(root, "stub-resolv.conf")
	_ = os.WriteFile(target, []byte("nameserver 127.0.0.53\n"), 0o644)
	_ = os.Symlink(target, resolv)
	d := &Dnsmasq{Dir: filepath.Join(root, "generated"), Leases: filepath.Join(root, "lib", "leases"), Resolv: resolv}
	run := &fakeRunner{}
	err := Setup(context.Background(), d, SetupOptions{UnitDir: filepath.Join(root, "units"), Run: run, Binary: "/usr/sbin/dnsmasq"}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(root, "units", Unit))
	if err != nil || !strings.Contains(string(unit), "--conf-file="+filepath.Join(root, "generated", "ostiole.conf")) {
		t.Errorf("unit = %s, %v", unit, err)
	}
	if info, err := os.Lstat(resolv); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Error("resolv.conf still a symlink")
	}
	for _, want := range [][]string{{"systemctl", "mask", "dnsmasq.service"}, {"systemctl", "mask", "systemd-resolved.service"}, {"systemctl", "daemon-reload"}} {
		found := false
		for _, c := range run.calls {
			if strings.Join(c, " ") == strings.Join(want, " ") {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %v in %v", want, run.calls)
		}
	}
	var _ network.Backend = d
}
