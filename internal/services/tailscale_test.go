package services

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/tailscale"
)

// The daemon's environment and the preference list are golden-tested on
// the inputs that join a tailnet; the rest must produce no files at all.
func TestRenderTailscaleGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, in)
			files, err := (&Tailscale{Dir: "/etc/ostiole/tailscale"}).Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			golden := strings.TrimSuffix(in, ".json") + ".tailscale"
			if !nft.TailscaleEnabled(cfg) {
				if len(files) != 0 {
					t.Fatalf("no tailnet but rendered %v", files.Names())
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

// setRunner records what `tailscale set` was given.
type setRunner struct {
	args []string
	err  error
}

func (s *setRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	s.args = args
	return nil, s.err
}

func (s *setRunner) Stream(context.Context, string, ...string) (io.ReadCloser, func() error, error) {
	return nil, nil, errors.New("not used")
}

func tailscaleBackend(t *testing.T) (*Tailscale, *fakeCmd, *setRunner) {
	t.Helper()
	cmd := &fakeCmd{installed: true}
	run := &setRunner{}
	return &Tailscale{
		Dir: t.TempDir(), Cmd: cmd,
		Client: &tailscale.Client{Bin: "tailscale", Run: run},
	}, cmd, run
}

func TestTailscaleApplyStartsTheUnitAndPushesThePreferences(t *testing.T) {
	t.Parallel()
	ts, cmd, run := tailscaleBackend(t)
	files, err := ts.Render(loadConfig(t, "testdata/tailscale.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "enable", "--now", TailscaleUnit) {
		t.Errorf("unit not enabled: %v", cmd.calls)
	}
	if !cmd.has("systemctl", "restart", TailscaleUnit) {
		t.Errorf("a first apply did not start the daemon: %v", cmd.calls)
	}
	argv := strings.Join(run.args, " ")
	for _, want := range []string{"set", "--netfilter-mode=off", "--hostname=gateway",
		"--advertise-routes=192.168.1.0/24", "--advertise-exit-node=true"} {
		if !strings.Contains(argv, want) {
			t.Errorf("missing %s in %s", want, argv)
		}
	}
	if raw, err := os.ReadFile(ts.EnvPath()); err != nil || !strings.Contains(string(raw), "PORT=41641") {
		t.Errorf("env file = %q, %v", raw, err)
	}

	// A second apply of the same configuration leaves the tunnel up.
	cmd.calls = nil
	if err := ts.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if cmd.has("systemctl", "restart", TailscaleUnit) {
		t.Errorf("an unchanged apply restarted the daemon: %v", cmd.calls)
	}
}

// Only the unit's own arguments are worth dropping the tunnel for. A
// preference is pushed to the running daemon instead.
func TestTailscaleRestartsOnlyOnAnEnvChange(t *testing.T) {
	t.Parallel()
	ts, cmd, _ := tailscaleBackend(t)
	cfg := loadConfig(t, "testdata/tailscale.json")
	files, err := ts.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		edit    func(*model.Tailscale)
		restart bool
	}{
		{"a route", func(ts *model.Tailscale) { ts.AdvertiseRoutes = []string{"10.0.0.0/8"} }, false},
		{"the port", func(ts *model.Tailscale) { ts.Port = 3478 }, true},
		{"log uploads", func(ts *model.Tailscale) { ts.LogUploads = true }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := loadConfig(t, "testdata/tailscale.json")
			in, _ := next.TailscaleInterface()
			tc.edit(in.Tailscale)
			files, err := ts.Render(next)
			if err != nil {
				t.Fatal(err)
			}
			cmd.calls = nil
			if err := ts.Apply(t.Context(), files); err != nil {
				t.Fatal(err)
			}
			if got := cmd.has("systemctl", "restart", TailscaleUnit); got != tc.restart {
				t.Errorf("restarted = %v, want %v: %v", got, tc.restart, cmd.calls)
			}
			// Put it back so the next case starts from the fixture.
			if err := ts.Apply(t.Context(), mustRender(t, ts, cfg)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func mustRender(t *testing.T, ts *Tailscale, cfg *model.Config) network.Files {
	t.Helper()
	files, err := ts.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Leaving the tailnet stops the unit and takes the files away. A router
// that never joined one gets no systemctl at all.
func TestTailscaleApplyRemoval(t *testing.T) {
	t.Parallel()
	ts, cmd, _ := tailscaleBackend(t)
	if err := ts.Apply(t.Context(), mustRender(t, ts, loadConfig(t, "testdata/tailscale.json"))); err != nil {
		t.Fatal(err)
	}
	cmd.calls = nil
	if err := ts.Apply(t.Context(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "disable", "--now", TailscaleUnit) {
		t.Errorf("unit not stopped: %v", cmd.calls)
	}
	if _, err := os.Stat(ts.EnvPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("env file survived removal: %v", err)
	}

	cmd.calls = nil
	if err := ts.Apply(t.Context(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 0 {
		t.Errorf("a router with no tailnet was touched: %v", cmd.calls)
	}
}

// The install script is what puts the daemon on the router, so an apply
// that needs it and cannot find it says which command to run — before
// anything has been applied.
func TestTailscaleRefusesWithoutTheDaemon(t *testing.T) {
	t.Parallel()
	ts, cmd, _ := tailscaleBackend(t)
	cmd.installed = false
	files := mustRender(t, ts, loadConfig(t, "testdata/tailscale.json"))

	err := ts.Preflight(t.Context(), files)
	if err == nil || !strings.Contains(err.Error(), "ostiole repair --tailscale") {
		t.Errorf("Preflight = %v, want the repair command", err)
	}
	if err := ts.Apply(t.Context(), files); err == nil {
		t.Error("apply went ahead without the daemon")
	}
	if err := ts.Preflight(t.Context(), network.Files{}); err != nil {
		t.Errorf("a router with no tailnet was refused: %v", err)
	}
}

// dnsmasq is what sends tailnet names to MagicDNS, because the daemon is
// told to leave resolv.conf alone.
func TestDnsmasqForwardsTailnetNames(t *testing.T) {
	t.Parallel()
	d := &Dnsmasq{Dir: DefaultDir, Leases: LeaseFile, Resolv: ResolvConf}
	files, err := d.Render(loadConfig(t, "testdata/tailscale.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := files.String()
	for _, want := range []string{
		"server=/ts.net/100.100.100.100",
		"rev-server=100.64.0.0/10,100.100.100.100",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}
