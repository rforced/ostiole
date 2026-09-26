package services

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/certs"
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
	// An uploaded certificate needs a pair to validate, and a private key
	// has no business in a repository. The fixtures leave both empty and
	// get a throwaway self-signed pair here.
	for i := range cfg.Certificates {
		c := &cfg.Certificates[i]
		if c.Source != model.SourceUploaded || c.CertPEM != "" || c.KeyPEM != "" {
			continue
		}
		certPEM, keyPEM, err := certs.GenerateSelfSigned([]string{c.ID + ".example.com"})
		if err != nil {
			t.Fatal(err)
		}
		c.CertPEM, c.KeyPEM = string(certPEM), string(keyPEM)
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

// The router keeps a query log of its own now, which makes it tempting to
// let a daemon keep one too. ADR-0016 says neither ever does: dnsmasq is
// never told log-queries, and unbound's verbosity never passes 2, where
// per-query lines begin. Pinned against the rendered goldens, which
// -update would otherwise accept a regression into.
func TestDaemonsNeverLogQueries(t *testing.T) {
	t.Parallel()
	dnsmasqs, _ := filepath.Glob("testdata/*.dnsmasq")
	if len(dnsmasqs) == 0 {
		t.Fatal("no dnsmasq goldens, so nothing was checked")
	}
	for _, path := range dnsmasqs {
		for _, line := range goldenLines(t, path) {
			if strings.HasPrefix(line, "log-queries") {
				t.Errorf("%s: %q", filepath.Base(path), line)
			}
		}
	}
	unbounds, _ := filepath.Glob("testdata/*.unbound")
	if len(unbounds) == 0 {
		t.Fatal("no unbound goldens, so nothing was checked")
	}
	for _, path := range unbounds {
		name := filepath.Base(path)
		var verbosity, queries, replies bool
		for _, line := range goldenLines(t, path) {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.TrimSpace(key) {
			case "verbosity":
				verbosity = true
				if n, err := strconv.Atoi(value); err != nil || n > 2 {
					t.Errorf("%s: verbosity %q; 2 is where per-query lines begin", name, value)
				}
			case "log-queries":
				queries = true
				if value != "no" {
					t.Errorf("%s: log-queries %q", name, value)
				}
			case "log-replies":
				replies = true
				if value != "no" {
					t.Errorf("%s: log-replies %q", name, value)
				}
			}
		}
		if !verbosity || !queries || !replies {
			t.Errorf("%s: verbosity, log-queries and log-replies have to be written, not left to the default", name)
		}
	}
}

// goldenLines reads a rendered daemon configuration, without its comments
// and blank lines.
func goldenLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
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
	files, _ := u.Render(loadConfig(t, "testdata/resolver-recursive.json"))
	err := u.Apply(context.Background(), files)
	if err == nil || !strings.Contains(err.Error(), "ostiole repair") {
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

// A pool on an interface that has been switched off must not reach
// dnsmasq. Nothing can arrive on the link, the firewall opens no port 67
// there, and a range dnsmasq still holds keeps leases alive in a subnet
// nobody serves. Switching the interface back on is what brings it back,
// so the configuration itself stays valid.
func TestDisabledInterfaceServesNothing(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/wireless.json")
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "ap1" {
			cfg.Interfaces[i].Enabled = false
		}
	}
	files, err := (&Dnsmasq{Dir: DefaultDir, Leases: LeaseFile}).Render(cfg)
	if err != nil {
		t.Fatalf("switching an interface off invalidated the configuration: %v", err)
	}
	conf := files[confName]
	// ap1 is a DNS listener as well as a pool, and neither binds a link
	// that is down.
	for _, s := range []string{"interface=ap1", "s_ap1"} {
		if strings.Contains(conf, s) {
			t.Errorf("disabled ap1 still serves (%q):\n%s", s, conf)
		}
	}
	if !strings.Contains(conf, "dhcp-range=set:s_br_lan,") || !strings.Contains(conf, "interface=br-lan") {
		t.Errorf("the pool on the enabled interface went missing:\n%s", conf)
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
	if err == nil || !strings.Contains(err.Error(), "ostiole repair") {
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
	// Sorted by family, then numerically: .50 before .101, IPv6 last. An
	// expiry of 0 is a lease that never expires.
	if !leases[0].Expires.IsZero() || leases[0].IP != "192.168.1.50" || leases[0].ClientID != "" || leases[0].Family != 4 {
		t.Errorf("lease 0 = %+v", leases[0])
	}
	if leases[1].IP != "192.168.1.101" || leases[1].Hostname != "laptop" || !leases[1].Expires.Equal(time.Unix(1758000000, 0)) {
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

// unbound follows root key rollovers by rewriting the trust anchor, so a
// directory it cannot write fails the setup instead of passing quietly.
func TestSetupFailsWhenUnboundCannotOwnTheAnchor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	run := &chownFails{}
	opts := SetupOptions{
		Resolver: true, UnitDir: filepath.Join(root, "units"), Run: run,
		Unbound:       &Unbound{Dir: filepath.Join(root, "unbound"), Anchor: filepath.Join(root, "lib", "root.key")},
		UnboundBinary: "/usr/sbin/unbound",
	}
	err := Setup(context.Background(), nil, opts, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), filepath.Join(root, "lib")) {
		t.Errorf("Setup = %v, want the anchor directory named", err)
	}
}

type chownFails struct{ fakeRunner }

func (c *chownFails) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "chown" {
		return []byte("chown: invalid user: 'unbound:unbound'"), errors.New("exit status 1")
	}
	return c.fakeRunner.Run(ctx, name, args...)
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

// A static lease with a hostname is a DNS record the operator never wrote
// on the DNS page; the overrides tab lists it from here, with the local
// domain expand-hosts adds. A bare IPv6 host part is not an address yet
// and is left out, as the hosts file leaves it out.
func TestSystemHostsComeFromStaticLeases(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	cfg.Services.DNS.Domain = "lan"
	cfg.Services.DHCP.StaticLeases = []model.StaticLease{
		{MAC: "AA:BB:CC:00:00:01", IP: "10.0.0.20", IPv6: "::20", Hostname: "calcifer", Description: "the NAS"},
		{MAC: "aa:bb:cc:00:00:02", IP: "10.0.0.21"},
		{MAC: "aa:bb:cc:00:00:03", IPv6: "fd00::3", Hostname: "sixonly"},
	}
	got := SystemHosts(cfg)
	want := []SystemHost{
		{Hostname: "calcifer", IP: "10.0.0.20", FQDN: "calcifer.lan", MAC: "aa:bb:cc:00:00:01", Description: "the NAS", Setting: "dhcp"},
		{Hostname: "sixonly", IP: "fd00::3", FQDN: "sixonly.lan", MAC: "aa:bb:cc:00:00:03", Setting: "dhcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SystemHosts =\n%+v\nwant\n%+v", got, want)
	}
	cfg.Services.DNS.Domain = ""
	if got := SystemHosts(cfg); got[0].FQDN != "" {
		t.Errorf("FQDN without a domain = %q", got[0].FQDN)
	}
	if got := SystemHosts(nil); len(got) != 0 {
		t.Errorf("nil config = %v", got)
	}
	// The rows are the hosts file: what the tab shows is what dnsmasq reads.
	_, hosts, err := (&Dnsmasq{}).render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"10.0.0.20 calcifer\n", "fd00::3 sixonly\n"} {
		if !strings.Contains(hosts, line) {
			t.Errorf("hosts file lacks %q:\n%s", line, hosts)
		}
	}
	if strings.Contains(hosts, "::20") {
		t.Errorf("a bare host part reached the hosts file:\n%s", hosts)
	}
}

// The lease file is dnsmasq's record of who has which address. Turning
// DHCP off, applying, or reverting rewrites the configuration and must
// leave that record alone: a router that forgot its leases hands out
// addresses that are still in use.
func TestApplyLeavesTheLeaseFileAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	leases := filepath.Join(dir, "ostiole.leases")
	const record = "1758300000 aa:bb:cc:00:00:01 10.0.0.20 calcifer *\n"
	if err := os.WriteFile(leases, []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Dnsmasq{Dir: filepath.Join(dir, "conf"), Leases: leases, Resolv: filepath.Join(dir, "resolv.conf"), Cmd: &fakeCmd{installed: true}}
	ctx := context.Background()
	on := loadConfig(t, "testdata/dhcp-only.json")
	files, err := d.Render(on)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(ctx, files); err != nil {
		t.Fatal(err)
	}
	off := loadConfig(t, "testdata/dhcp-only.json")
	off.Services.DHCP.Enabled = false
	files, err = d.Render(off)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(ctx, files); err != nil {
		t.Fatal(err)
	}
	// A revert to before any services existed installs no files at all.
	if err := d.Apply(ctx, network.Files{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(leases)
	if err != nil || string(got) != record {
		t.Errorf("lease file after apply, disable and revert = %q, %v", got, err)
	}
}

// Both caches clear on a HUP, which is what systemctl reload sends.
func TestReloadHUPsTheUnits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dc := &fakeCmd{installed: true, active: true}
	if err := (&Dnsmasq{Cmd: dc}).Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if !dc.has("systemctl", "reload", Unit) {
		t.Errorf("dnsmasq reload calls = %v", dc.calls)
	}

	uc := &fakeCmd{installed: true, active: true}
	if err := (&Unbound{Cmd: uc}).Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if !uc.has("systemctl", "reload", UnboundUnit) {
		t.Errorf("unbound reload calls = %v", uc.calls)
	}
}

// A unit that refuses the reload says so rather than reporting success.
func TestReloadReportsFailure(t *testing.T) {
	t.Parallel()
	c := &failCmd{}
	err := (&Dnsmasq{Cmd: c}).Reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), Unit) {
		t.Errorf("err = %v", err)
	}
}

type failCmd struct{}

func (failCmd) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte("Job failed"), errors.New("exit 1")
}
