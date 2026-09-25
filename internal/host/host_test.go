package host

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/nft"
)

// fakeUnits is a systemd that answers from a table: what is enabled, what
// is active, and which units it has heard of at all.
type fakeUnits struct {
	enabled map[string]string
	active  map[string]string
	known   map[string]bool
	calls   []string
}

func (f *fakeUnits) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if len(args) < 2 {
		return "", nil
	}
	unit := args[len(args)-1]
	switch args[0] {
	case "is-enabled":
		if state, ok := f.enabled[unit]; ok {
			return state, nil
		}
		return "not-found", errors.New("not found")
	case "is-active":
		if state, ok := f.active[unit]; ok {
			return state, nil
		}
		return "inactive", errors.New("inactive")
	case "cat":
		if f.known[unit] {
			return "[Unit]", nil
		}
		return "", errors.New("no such unit")
	}
	return "", nil
}

// noKernel is an nftables side that has nothing to say, which is what a
// daemon that is not root gets.
type noKernel struct{}

func (noKernel) ListChains(context.Context) ([]nft.ChainRef, error) {
	return nil, errors.New("not permitted")
}
func (noKernel) DeleteTable(context.Context, string, string) error {
	return errors.New("not permitted")
}

// fakeCommands is every command a test lets this package run. Nothing
// reaches the machine the test is running on.
type fakeCommands struct {
	out   map[string]string
	calls []string
}

func (f *fakeCommands) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	return []byte(f.out[line]), nil
}

func testDeps(t *testing.T, units *fakeUnits) Deps {
	t.Helper()
	return Deps{
		Root:        true,
		Units:       units,
		Kernel:      noKernel{},
		TableLoaded: func(context.Context) bool { return true },
		Backend:     "networkd",
		Dir:         t.TempDir(),
		Run:         &fakeCommands{},
		// A router with none of the daemons installed, whatever the
		// machine running the test has on it.
		Locate: func(string) string { return "" },
		Proc:   t.TempDir(),
		Log:    slog.New(slog.DiscardHandler),
	}
}

// The report is the three units, the daemons a router runs, and who owns
// the addresses, read from the router every time.
func TestStatusReadsTheRouter(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{
			install.DaemonUnit: "enabled", install.FirewallUnit: "enabled",
			install.NetworkdUnit: "enabled", "NetworkManager.service": "enabled",
		},
		active: map[string]string{install.DaemonUnit: "active", install.NetworkdUnit: "active"},
		known:  map[string]bool{install.NetworkdUnit: true},
	}
	d := testDeps(t, units)
	d.Locate = func(name string) string {
		if name == "nft" || name == "dnf" {
			return "/usr/sbin/" + name
		}
		return ""
	}
	rep := Status(context.Background(), d)
	if rep.Manager != "dnf" {
		t.Errorf("manager = %q, want dnf", rep.Manager)
	}
	if !rep.Present["nft"] || rep.Present["dnsmasq"] {
		t.Errorf("present = %+v", rep.Present)
	}
	if len(rep.Units) != 3 || rep.Units[0].Name != install.DaemonUnit || rep.Units[0].Active != "active" {
		t.Errorf("units = %+v", rep.Units)
	}
	if !rep.Firewalled {
		t.Error("the loaded ruleset was not reported")
	}
	if rep.Network.Networkd != "active" || rep.Network.Owned {
		t.Errorf("network = %+v", rep.Network)
	}
	if len(rep.Network.Managers) != 1 || rep.Network.Managers[0] != "NetworkManager" {
		t.Errorf("managers = %v, want the one still enabled", rep.Network.Managers)
	}
}

// Once the handover has happened the record is what the report reads, so
// a masked manager is named as retired rather than as competing.
func TestStatusFollowsTheTakeoverRecord(t *testing.T) {
	t.Parallel()
	d := testDeps(t, &fakeUnits{enabled: map[string]string{install.NetworkdUnit: "enabled"}})
	if err := install.SaveTakeoverRecord(d.Dir, install.TakeoverRecord{Managers: []string{"NetworkManager"}}); err != nil {
		t.Fatal(err)
	}
	rep := Status(context.Background(), d)
	if !rep.Network.Owned || len(rep.Network.Managers) != 1 {
		t.Errorf("network = %+v, want it owned with the manager it replaced", rep.Network)
	}
}

// Nothing that changes the router runs without root.
func TestFlushNeedsRoot(t *testing.T) {
	t.Parallel()
	d := testDeps(t, &fakeUnits{})
	d.Root = false
	if _, err := FlushLegacy(context.Background(), d, nil); !errors.Is(err, ErrNotRoot) {
		t.Errorf("flush = %v, want ErrNotRoot", err)
	}
}
