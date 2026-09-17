package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
)

type fakeProber struct {
	mu     sync.Mutex
	fail   map[string]bool
	probes int
}

func (f *fakeProber) Probe(_ context.Context, address, _ string, _ time.Duration) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes++
	if f.fail[address] {
		return 0, errors.New("timeout")
	}
	return 3 * time.Millisecond, nil
}

func (f *fakeProber) setFail(address string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[address] = down
}

type fakeRouter struct {
	mu        sync.Mutex
	demoted   []string
	restored  []string
	resolveTo map[string]string
}

func (f *fakeRouter) Demote(g Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.demoted = append(f.demoted, g.Name)
	return nil
}

func (f *fakeRouter) Restore(g Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored = append(f.restored, g.Name)
	return nil
}

func (f *fakeRouter) Resolve(g Status) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.resolveTo[g.Name]; ok {
		return a, true
	}
	return g.Address, g.Address != ""
}

func (f *fakeRouter) calls() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.demoted...), append([]string(nil), f.restored...)
}

func newTestMonitor(t *testing.T) (*Monitor, *fakeProber, *fakeRouter) {
	t.Helper()
	p := &fakeProber{fail: map[string]bool{}}
	r := &fakeRouter{resolveTo: map[string]string{}}
	m := New(p, r, slog.New(slog.DiscardHandler))
	m.Configure([]model.Gateway{
		{Name: "primary", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Priority: 0},
		{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.1", Priority: 1},
		{Name: "off", Enabled: false, Interface: "eth2", Address: "192.0.2.1"},
	})
	return m, p, r
}

func tick(m *Monitor, times int) {
	for i := 0; i < times; i++ {
		m.Tick(context.Background())
	}
}

func TestMonitorTakesGatewaysUpAndDown(t *testing.T) {
	t.Parallel()
	m, prober, router := newTestMonitor(t)

	// Disabled gateways are not watched at all.
	if got := m.Statuses(); len(got) != 2 {
		t.Fatalf("statuses = %+v, want the two enabled gateways", got)
	}
	// One answer is not enough to call a gateway up.
	tick(m, 1)
	if s := m.Statuses()[0]; !s.Unknown {
		t.Errorf("after one probe: %+v, want still unknown", s)
	}
	tick(m, 1)
	statuses := m.Statuses()
	if !statuses[0].Online || statuses[0].Unknown || !statuses[0].Active {
		t.Errorf("primary = %+v, want online and active", statuses[0])
	}
	if statuses[1].Active {
		t.Errorf("backup should not be active while the primary is up: %+v", statuses[1])
	}
	if statuses[0].LatencyMS != 3 {
		t.Errorf("latency = %v, want 3ms", statuses[0].LatencyMS)
	}

	// Two losses are tolerated, the third takes it down and moves the route.
	prober.setFail("203.0.113.1", true)
	tick(m, 2)
	if s := m.Statuses()[0]; !s.Online {
		t.Errorf("two losses should not fail a gateway: %+v", s)
	}
	tick(m, 1)
	statuses = m.Statuses()
	if statuses[0].Online || !statuses[1].Active {
		t.Errorf("after three losses: %+v", statuses)
	}
	if statuses[0].LossPercent == 0 {
		t.Errorf("loss = %v, want some", statuses[0].LossPercent)
	}
	demoted, _ := router.calls()
	if len(demoted) != 1 || demoted[0] != "primary" {
		t.Errorf("demoted = %v, want the primary once", demoted)
	}

	// Recovery restores the route, and only once.
	prober.setFail("203.0.113.1", false)
	tick(m, 2)
	if s := m.Statuses()[0]; !s.Online || !s.Active {
		t.Errorf("recovered primary = %+v", s)
	}
	tick(m, 2)
	demoted, restored := router.calls()
	if len(restored) != 1 || restored[0] != "primary" || len(demoted) != 1 {
		t.Errorf("demoted = %v, restored = %v", demoted, restored)
	}
}

func TestMonitorKeepsTheLastRoute(t *testing.T) {
	t.Parallel()
	m, prober, router := newTestMonitor(t)
	tick(m, 2)
	prober.setFail("203.0.113.1", true)
	prober.setFail("198.51.100.1", true)
	tick(m, 5)

	for _, s := range m.Statuses() {
		if s.Online {
			t.Errorf("%s should be down: %+v", s.Name, s)
		}
	}
	demoted, _ := router.calls()
	if len(demoted) > 1 {
		t.Errorf("demoted = %v, want at most one: the last route stays", demoted)
	}
}

func TestMonitorResolvesDynamicGateways(t *testing.T) {
	t.Parallel()
	p := &fakeProber{fail: map[string]bool{}}
	r := &fakeRouter{resolveTo: map[string]string{}}
	m := New(p, r, slog.New(slog.DiscardHandler))
	m.Configure([]model.Gateway{{Name: "wan", Enabled: true, Interface: "eth0"}})

	// Without a next hop there is nothing to probe.
	tick(m, 3)
	if s := m.Statuses()[0]; s.Online || s.LastError == "" {
		t.Errorf("status without an address = %+v", s)
	}

	// Once DHCP provides one, the monitor picks it up.
	r.mu.Lock()
	r.resolveTo["wan"] = "192.0.2.254"
	r.mu.Unlock()
	tick(m, 2)
	if s := m.Statuses()[0]; !s.Online || s.Address != "192.0.2.254" {
		t.Errorf("status after resolution = %+v", s)
	}
}

func TestConfigureKeepsStateOfUnchangedGateways(t *testing.T) {
	t.Parallel()
	m, _, _ := newTestMonitor(t)
	tick(m, 2)
	m.Configure([]model.Gateway{
		{Name: "primary", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Priority: 0},
		{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.2", Priority: 1},
	})
	statuses := m.Statuses()
	if !statuses[0].Online {
		t.Errorf("unchanged gateway lost its state: %+v", statuses[0])
	}
	if !statuses[1].Unknown {
		t.Errorf("gateway with a new address should start unknown: %+v", statuses[1])
	}
}

// TestICMPProbeInNamespace runs the real prober against a real kernel. It
// re-executes itself inside an unprivileged network namespace, where it
// has the raw socket capability without being root on the host.
func TestICMPProbeInNamespace(t *testing.T) {
	if os.Getenv("OSTIOLE_PROBE_NETNS") == "" {
		runInNamespace(t, "OSTIOLE_PROBE_NETNS", "TestICMPProbeInNamespace")
		return
	}

	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatalf("lo: %v", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		t.Fatalf("bring lo up: %v", err)
	}
	rtt, err := NewICMPProber().Probe(context.Background(), "127.0.0.1", "lo", 2*time.Second)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if rtt <= 0 {
		t.Errorf("rtt = %v", rtt)
	}
	// An address that routes but answers nothing must time out rather than
	// hang. Anything in 127/8 is local and would reply, so route a
	// different range into the loopback, where the kernel drops it.
	_, dst, _ := net.ParseCIDR("10.254.0.0/16")
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: lo.Attrs().Index, Dst: dst}); err != nil {
		t.Fatalf("add silent route: %v", err)
	}
	start := time.Now()
	if _, err := NewICMPProber().Probe(context.Background(), "10.254.0.1", "lo", 300*time.Millisecond); err == nil {
		t.Error("probe of a silent address succeeded")
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("probe waited %v, want the timeout to apply", waited)
	}
}

// runInNamespace re-runs the calling test inside a fresh unprivileged user
// and network namespace, where it has the network capabilities without
// being root. It skips when the sandbox forbids that, which is what
// GitHub's runners do.
func runInNamespace(t *testing.T, env, name string) {
	t.Helper()
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not installed")
	}
	cmd := exec.Command("unshare", "-Urn", os.Args[0], "-test.run", "^"+name+"$", "-test.v")
	cmd.Env = append(os.Environ(), env+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	if strings.Contains(string(out), "uid_map") || strings.Contains(string(out), "Operation not permitted") {
		t.Skipf("unprivileged namespaces are not allowed here: %s", strings.TrimSpace(string(out)))
	}
	t.Fatalf("inside namespace: %v\n%s", err, out)
}

// fakeShaping records what the tick handed it and can be told to fail.
type fakeShaping struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeShaping) Sync(cfg *model.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cfg.System.Hostname)
	return f.err
}

func (f *fakeShaping) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// The tick puts the queues back as well as the routes, and it does so on a
// router with no gateways at all: a box that only caps its LAN still has
// to converge after a reboot or a link coming back.
func TestTickKeepsShapingInPlace(t *testing.T) {
	t.Parallel()
	sh := &fakeShaping{}
	cfg := &model.Config{Version: model.SchemaVersion, System: model.System{Hostname: "capped"}}
	m := &Monitor{
		Prober: &fakeProber{fail: map[string]bool{}}, Router: &fakeRouter{},
		Log: slog.New(slog.DiscardHandler), Shaping: sh,
		Source: func() *model.Config { return cfg },
	}
	m.Tick(context.Background())
	if got := sh.seen(); len(got) != 1 || got[0] != "capped" {
		t.Fatalf("shaping sync calls = %v, want one for the saved configuration", got)
	}

	// A shaper that cannot do its job must not stop the probes.
	sh.err = errors.New("tc refused")
	m.Tick(context.Background())
	if got := len(sh.seen()); got != 2 {
		t.Errorf("shaping was called %d times, want 2", got)
	}
}
