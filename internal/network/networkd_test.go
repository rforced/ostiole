package network

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

	"github.com/vishvananda/netlink"

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
	calls    [][]string
	err      error
	networkd string // reply to `systemctl is-active systemd-networkd.service`
}

func (f *fakeCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "systemctl" && len(args) == 2 && args[0] == "is-active" {
		if f.networkd == "active" {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit 3")
	}
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
	if err := os.WriteFile(filepath.Join(dir, "00-ostiole-old.network"), []byte("stale"), 0o644); err != nil {
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
	if _, err := os.Stat(filepath.Join(dir, "00-ostiole-old.network")); !errors.Is(err, os.ErrNotExist) {
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
	for _, name := range []string{"20-wired.network", "../00-ostiole-x.network", "00-ostiole-../x"} {
		if err := n.Apply(context.Background(), Files{name: "x"}); err == nil {
			t.Errorf("Apply accepted %q", name)
		}
	}
}

func TestApplyReportsReloadFailure(t *testing.T) {
	t.Parallel()
	n := &Networkd{Dir: t.TempDir(), Cmd: &fakeCmd{err: errors.New("exit 1")}}
	err := n.Apply(context.Background(), Files{"00-ostiole-eth0.network": "x"})
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

func TestAutoBackendFollowsNetworkd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := &fakeCmd{networkd: "inactive"}
	a := &Auto{Networkd: &Networkd{Dir: dir, Cmd: cmd}, Log: slog.New(slog.DiscardHandler)}
	files, err := a.Render(loadConfig(t, "testdata/minimal.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("render = %v, %v", files, err)
	}
	if a.Name() != "none" {
		t.Errorf("Name() = %q while inactive", a.Name())
	}
	if err := a.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if snap, _ := a.Networkd.Snapshot(); len(snap) != 0 {
		t.Error("units written while networkd inactive")
	}
	for _, c := range cmd.calls {
		if c[0] == "networkctl" {
			t.Errorf("networkctl called while inactive: %v", c)
		}
	}

	cmd.networkd = "active"
	if a.Name() != "systemd-networkd" {
		t.Errorf("Name() = %q while active", a.Name())
	}
	if err := a.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if snap, _ := a.Networkd.Snapshot(); len(snap) != len(files) {
		t.Errorf("units not written while active: %v", snap.Names())
	}
}

// The bond modes and hash policies Ostiole offers have to be the names the
// kernel itself uses, or networkd writes a value the driver rejects at
// link-up rather than at apply.
func TestBondSettingsUseKernelNames(t *testing.T) {
	t.Parallel()
	for _, mode := range model.BondModes {
		if got := netlink.StringToBondMode(string(mode)); got == netlink.BOND_MODE_UNKNOWN {
			t.Errorf("bond mode %q is not a mode the kernel knows", mode)
		}
	}
	for _, policy := range model.HashPolicies {
		if got := netlink.StringToBondXmitHashPolicy(policy); got == netlink.BOND_XMIT_HASH_POLICY_UNKNOWN {
			t.Errorf("transmit hash policy %q is not one the kernel knows", policy)
		}
	}
}

// A bridge or bond member must end up attached to its master, and never
// carry addressing of its own.
func TestRenderEnslavesMembers(t *testing.T) {
	t.Parallel()
	files, err := (&Networkd{}).Render(loadConfig(t, "testdata/aggregated.json"))
	if err != nil {
		t.Fatal(err)
	}
	for member, want := range map[string]string{
		"eth0": "Bond=bond0",
		"eth1": "Bond=bond0",
		"eth2": "Bridge=br0",
		"eth3": "Bridge=br0",
		"eth4": "Bond=bond1",
	} {
		unit := files["00-ostiole-"+member+".network"]
		if !strings.Contains(unit, want) {
			t.Errorf("%s is not attached to its master:\n%s", member, unit)
		}
		if strings.Contains(unit, "Address=") || strings.Contains(unit, "DHCP=") {
			t.Errorf("%s carries addressing of its own:\n%s", member, unit)
		}
		if !strings.Contains(unit, "LinkLocalAddressing=no") {
			t.Errorf("%s should have no link-local address:\n%s", member, unit)
		}
	}
	// The bond itself keeps the addressing and comes up without carrier,
	// so a switch that is not ready yet does not leave it unconfigured.
	bond := files["00-ostiole-bond0.network"]
	if !strings.Contains(bond, "ConfigureWithoutCarrier=yes") || !strings.Contains(bond, "DHCP=ipv4") {
		t.Errorf("bond0 unit:\n%s", bond)
	}
}
