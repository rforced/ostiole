package network

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
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

func TestNetworkdRenderGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	if len(inputs) == 0 {
		t.Fatal("no testdata")
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files, err := (&Networkd{}).Render(loadConfig(t, in))
			if err != nil {
				t.Fatal(err)
			}
			got := files.String()
			golden := strings.TrimSuffix(in, ".json") + ".networkd"
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

func TestRenderRouteNeedsInterface(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/full.json")
	cfg.Routes = append(cfg.Routes, model.StaticRoute{ID: "orphan", Enabled: true, Destination: "10.9.0.0/16", Gateway: "203.0.113.1"})
	_, err := (&Networkd{}).Render(cfg)
	if err == nil || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("err = %v", err)
	}
	cfg.Routes[len(cfg.Routes)-1].Enabled = false
	if _, err := (&Networkd{}).Render(cfg); err != nil {
		t.Fatalf("disabled orphan route should be ignored: %v", err)
	}
}

type fakeCmd struct {
	calls [][]string
	err   error
}

func (f *fakeCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return []byte("boom"), f.err
	}
	return nil, nil
}

func TestApplyWritesRemovesAndReloads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := &fakeCmd{}
	n := &Networkd{Dir: dir, Cmd: cmd}

	// Pre-existing: one stale owned file, one foreign file that must survive.
	if err := os.WriteFile(filepath.Join(dir, "10-ostiole-old.network"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-wired.network"), []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := n.Render(loadConfig(t, "testdata/minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}

	snap, err := n.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.String() != files.String() {
		t.Errorf("snapshot after apply differs:\n%s", snap.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "10-ostiole-old.network")); !errors.Is(err, os.ErrNotExist) {
		t.Error("stale owned file not removed")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "20-wired.network")); string(raw) != "foreign" {
		t.Error("foreign file touched")
	}
	if len(cmd.calls) != 2 || cmd.calls[0][1] != "reload" || cmd.calls[1][1] != "reconfigure" {
		t.Fatalf("calls = %v", cmd.calls)
	}
	if got := strings.Join(cmd.calls[1][2:], ","); got != "eth0,eth1" {
		t.Errorf("reconfigured %q, want eth0,eth1", got)
	}

	// Applying the same files again is a no-op: no reload.
	cmd.calls = nil
	if err := n.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 0 {
		t.Errorf("unchanged apply ran %v", cmd.calls)
	}

	// Revert via snapshot restores the previous set (here: nothing owned).
	if err := n.Apply(context.Background(), Files{}); err != nil {
		t.Fatal(err)
	}
	snap, _ = n.Snapshot()
	if len(snap) != 0 {
		t.Errorf("files left after empty apply: %v", snap.Names())
	}
}

func TestApplyRefusesForeignNames(t *testing.T) {
	t.Parallel()
	n := &Networkd{Dir: t.TempDir(), Cmd: &fakeCmd{}}
	for _, name := range []string{"20-wired.network", "../10-ostiole-x.network", "10-ostiole-../x"} {
		if err := n.Apply(context.Background(), Files{name: "x"}); err == nil {
			t.Errorf("Apply accepted %q", name)
		}
	}
}

func TestApplyReportsReloadFailure(t *testing.T) {
	t.Parallel()
	n := &Networkd{Dir: t.TempDir(), Cmd: &fakeCmd{err: errors.New("exit 1")}}
	err := n.Apply(context.Background(), Files{"10-ostiole-eth0.network": "x"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverFindsLoopback(t *testing.T) {
	t.Parallel()
	links, err := Discover()
	if err != nil {
		t.Skipf("netlink unavailable: %v", err)
	}
	for _, l := range links {
		if l.Kind == "loopback" && l.Name == "lo" {
			if !l.Up || l.MTU == 0 {
				t.Errorf("lo = %+v", l)
			}
			return
		}
	}
	t.Fatalf("no loopback in %+v", links)
}
