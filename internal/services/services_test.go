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

// The unbound configuration is golden-tested on the inputs that ask for a
// validating resolver; the others must produce no files at all.
func TestRenderUnboundGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, in)
			u := &Unbound{Dir: UnboundDir, Anchor: UnboundAnchor, CertBundle: "/etc/pki/tls/certs/ca-bundle.crt"}
			files, err := u.Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			golden := strings.TrimSuffix(in, ".json") + ".unbound"
			if !ResolverEnabled(cfg) {
				if len(files) != 0 {
					t.Fatalf("resolver off but rendered %v", files.Names())
				}
				return
			}
			got := files.String()
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

// The privacy hardening is a requirement, not an accident of the goldens:
// those can be regenerated with -update, which would quietly accept a
// dropped directive. Assert the directives on every input that runs unbound.
func TestUnboundHardening(t *testing.T) {
	t.Parallel()
	required := []string{
		// id.server and hostname.bind are refused.
		"hide-identity: yes",
		// version.server and version.bind are refused.
		"hide-version: yes",
		// Send upstreams the least of the name they need to answer.
		"qname-minimisation: yes",
	}
	inputs, _ := filepath.Glob("testdata/*.json")
	var checked int
	for _, in := range inputs {
		cfg := loadConfig(t, in)
		if !ResolverEnabled(cfg) {
			continue
		}
		checked++
		u := &Unbound{Dir: UnboundDir, Anchor: UnboundAnchor, CertBundle: "/ca.crt"}
		files, err := u.Render(cfg)
		if err != nil {
			t.Fatal(err)
		}
		got := files.String()
		for _, want := range required {
			if !strings.Contains(got, want) {
				t.Errorf("%s: %q missing from the rendered configuration", filepath.Base(in), want)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no input enabled the resolver, so nothing was checked")
	}
}

func TestBundleRoutesFilesToItsBackends(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	unboundDir := filepath.Join(dir, "unbound")
	dnsmasqDir := filepath.Join(dir, "dnsmasq")
	ucmd := &fakeCmd{installed: true}
	dcmd := &fakeCmd{installed: true}
	u := &Unbound{Dir: unboundDir, Anchor: filepath.Join(dir, "root.key"), CertBundle: "/ca.crt", Cmd: ucmd}
	d := &Dnsmasq{Dir: dnsmasqDir, Leases: filepath.Join(dir, "leases"), Cmd: dcmd}
	b := NewBundleOf(u, d)

	cfg := loadConfig(t, "testdata/resolver-tls.json")
	files, err := b.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["unbound/ostiole.conf"]; !ok {
		t.Fatalf("files = %v", files.Names())
	}
	if _, ok := files["dnsmasq/ostiole.conf"]; !ok {
		t.Fatalf("files = %v", files.Names())
	}
	if err := b.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if !ucmd.has("systemctl", "restart", UnboundUnit) || !dcmd.has("systemctl", "restart", Unit) {
		t.Errorf("units not started: %v %v", ucmd.calls, dcmd.calls)
	}
	// dnsmasq must ask unbound, not the upstreams.
	conf := files["dnsmasq/ostiole.conf"]
	if !strings.Contains(conf, "server=127.0.0.53#53") || !strings.Contains(conf, "proxy-dnssec") {
		t.Errorf("dnsmasq conf = %s", conf)
	}
	if strings.Contains(conf, "server=1.1.1.1") {
		t.Error("dnsmasq still forwards to the plain upstreams")
	}

	snap, err := b.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.String() != files.String() {
		t.Errorf("snapshot differs:\n%s", snap.String())
	}

	// Switching the resolver off stops unbound and removes its file.
	cfg.Services.DNS.Resolver = model.ResolverForward
	off, err := b.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ucmd.calls = nil
	if err := b.Apply(context.Background(), off); err != nil {
		t.Fatal(err)
	}
	if !ucmd.has("systemctl", "disable", "--now", UnboundUnit) {
		t.Errorf("unbound not stopped: %v", ucmd.calls)
	}
	if _, err := os.Stat(u.ConfPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("unbound configuration left behind")
	}
}

func TestUnboundApplyNeedsSetup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	u := &Unbound{Dir: dir, Cmd: &fakeCmd{installed: false}}
	files, _ := u.Render(loadConfig(t, "testdata/resolver-validate.json"))
	err := u.Apply(context.Background(), files)
	if err == nil || !strings.Contains(err.Error(), "--with-resolver") {
		t.Fatalf("err = %v", err)
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

// Under ProtectSystem=strict the daemon gets /etc/resolv.conf as a
// read-write bind mount inside a read-only /etc, so it cannot lay down a
// temporary file next to it or rename over it. A directory that takes no new
// entries stands in for that here.
func TestWriteFileRewritesInPlaceWhenTheDirectoryIsSealed(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes to an unwritable directory regardless")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(path, []byte("nameserver 9.9.9.9\nsearch stale.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	const want = "nameserver 127.0.0.1\n"
	if err := writeFile(path, want); err != nil {
		t.Fatalf("writeFile = %v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != want {
		t.Errorf("resolv.conf = %q, %v", raw, err)
	}
	// The old content was longer: nothing of it may survive the truncate,
	// and no temporary file may be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("directory holds %v, %v", entries, err)
	}
	// A file that does not exist yet still cannot be created there, and the
	// error says so rather than being swallowed.
	if err := writeFile(filepath.Join(dir, "new.conf"), want); err == nil {
		t.Error("writing a new file in a sealed directory reported success")
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
	err := Setup(context.Background(), d, SetupOptions{Dnsmasq: true, UnitDir: filepath.Join(root, "units"), Run: run, Binary: "/usr/sbin/dnsmasq"}, slog.New(slog.DiscardHandler))
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

// The pppd peer files are golden-tested on the inputs that dial; the rest
// must produce none at all.
func TestRenderPPPoEGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, in)
			files, err := (&PPPoE{Dir: PPPoEDir}).Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			golden := strings.TrimSuffix(in, ".json") + ".pppoe"
			if len(Sessions(cfg)) == 0 {
				if len(files) != 0 {
					t.Fatalf("no sessions configured but rendered %v", files.Names())
				}
				return
			}
			got := files.String()
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

// A password with a quote in it must not be able to end the option and
// start another; pppd reads these files as shell-ish words.
func TestPeerFileQuotesTheSecret(t *testing.T) {
	t.Parallel()
	files, err := (&PPPoE{Dir: PPPoEDir}).Render(loadConfig(t, "testdata/pppoe.json"))
	if err != nil {
		t.Fatal(err)
	}
	peer := files[PeerFile("ppp0")]
	if !strings.Contains(peer, `password "a \"quoted\" secret"`) {
		t.Errorf("the quote was not escaped:\n%s", peer)
	}
	// A session that is switched off is not dialled.
	if _, ok := files[PeerFile("ppp1")]; ok {
		t.Error("a disabled session should not be written")
	}
	// The gateway metric belongs on the route pppd installs.
	if !strings.Contains(peer, "defaultroute-metric 10") {
		t.Errorf("the default route has no metric:\n%s", peer)
	}
}

// A router that dials no PPPoE must not have /etc/ppp created under it. The
// daemon runs with ProtectSystem=strict, so an apply that has nothing to
// do with PPPoE used to fail with "mkdir /etc/ppp: read-only file
// system" — and so did the rollback, which is worse.
func TestPPPoEApplyTouchesNothingWithoutSessions(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "never", "created")
	cmd := &fakeCmd{}
	p := &PPPoE{Dir: dir, Cmd: cmd}

	cfg := loadConfig(t, "testdata/full.json")
	files, err := p.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("rendered %v for a configuration with no sessions", files.Names())
	}
	if err := p.Apply(context.Background(), files); err != nil {
		t.Fatalf("Apply with nothing to do: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory was created anyway: %v", err)
	}
	if len(cmd.calls) != 0 {
		t.Errorf("it talked to systemd for nothing: %v", cmd.calls)
	}
}

// And the same on the way back: a rollback after an unrelated failure
// must not be the thing that takes the router down.
func TestPPPoERollbackToNothingIsQuiet(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "gone")
	p := &PPPoE{Dir: dir, Cmd: &fakeCmd{}}
	if err := p.Apply(context.Background(), network.Files{}); err != nil {
		t.Fatalf("rollback to no sessions: %v", err)
	}
}
