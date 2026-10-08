package gateway

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
	"ostiole/internal/policy"
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

// fakeRouter keeps which gateways are demoted, as the kernel does in the
// metric, and records the calls that moved something.
type fakeRouter struct {
	mu        sync.Mutex
	down      map[string]bool
	demoted   []string
	restored  []string
	forgotten []string
	resolveTo map[string]string
	resolve6  map[string]string
	// foreign is the metric of a default route no gateway owns, 0 for none.
	foreign int
}

func (f *fakeRouter) Demote(g Status) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down[g.Name] {
		return false, nil
	}
	if f.down == nil {
		f.down = map[string]bool{}
	}
	f.down[g.Name] = true
	f.demoted = append(f.demoted, g.Name)
	return true, nil
}

func (f *fakeRouter) Restore(g Status) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.down[g.Name] {
		return false, nil
	}
	delete(f.down, g.Name)
	f.restored = append(f.restored, g.Name)
	return true, nil
}

func (f *fakeRouter) Forget(g Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.down, g.Name)
	f.forgotten = append(f.forgotten, g.Name)
	return nil
}

// putBack does what networkd does on a lease renewal: the route comes back
// at its own metric, whatever the monitor made of it.
func (f *fakeRouter) putBack(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.down, name)
}

func (f *fakeRouter) Resolve(g Status) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g.Address != "" {
		if familyOf(g.Address) == FamilyIPv6 {
			return "", g.Address
		}
		return g.Address, ""
	}
	return f.resolveTo[g.Name], f.resolve6[g.Name]
}

// Carriers picks by metric as the kernel does. A demoted gateway's is its
// own plus DemoteMetric.
func (f *fakeRouter) Carriers(gs []Status) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name, best := "", f.foreign
	if best == 0 {
		best = math.MaxInt
	}
	for _, g := range gs {
		metric := g.Metric
		if f.down[g.Name] {
			metric += DemoteMetric
		}
		if metric < best {
			name, best = g.Name, metric
		}
	}
	if name == "" {
		return nil, nil
	}
	return []string{name}, nil
}

func (f *fakeRouter) setForeign(metric int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.foreign = metric
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
	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "primary", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Priority: 0},
		{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.1", Priority: 1},
		{Name: "off", Enabled: false, Interface: "eth2", Address: "192.0.2.1"},
	}})
	return m, p, r
}

func tick(m *Monitor, times int) {
	for range times {
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

// A journal that keeps only warnings says when a gateway went down and
// when it came back, but not that it came up at start.
func TestAGatewayComingBackIsLoggedAtTheLevelOfGoingDown(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	p := &fakeProber{fail: map[string]bool{}}
	m := New(p, &fakeRouter{resolveTo: map[string]string{}},
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	m.Configure(&model.Config{Gateways: []model.Gateway{{Name: "primary", Enabled: true, Interface: "eth0", Address: "203.0.113.1"}}})
	tick(m, RiseAfter)
	p.setFail("203.0.113.1", true)
	tick(m, FailAfter)
	p.setFail("203.0.113.1", false)
	tick(m, RiseAfter)
	got := buf.String()
	if strings.Count(got, "gateway is up") != 1 || !strings.Contains(got, "gateway is down") ||
		!strings.Contains(got, `msg="gateway is up again"`) || !strings.Contains(got, "down=") {
		t.Errorf("warnings:\n%s", got)
	}
}

// Active is what the kernel does with the traffic. A default route no
// gateway owns, at a lower metric, carries it however well the gateways
// answer; the live check had the API call the best of them active then.
func TestMonitorActiveFollowsTheKernel(t *testing.T) {
	t.Parallel()
	m, _, router := newTestMonitor(t)
	router.setForeign(5)
	tick(m, 2)
	for _, s := range m.Statuses() {
		if !s.Online || s.Active {
			t.Errorf("%s = %+v, want online and not active", s.Name, s)
		}
	}
	router.setForeign(0)
	tick(m, 1)
	if s := m.Statuses(); !s[0].Active || s[1].Active {
		t.Errorf("with the gateways' routes alone: %+v", s)
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

// A router with one gateway has nowhere to fail over to, so its route is
// left alone however badly the probes go. The probing itself carries on:
// the dashboard still wants the latency and loss.
func TestMonitorLeavesASingleGatewayAlone(t *testing.T) {
	t.Parallel()
	p := &fakeProber{fail: map[string]bool{"203.0.113.1": true}}
	r := &fakeRouter{resolveTo: map[string]string{}}
	m := New(p, r, slog.New(slog.DiscardHandler))
	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"},
		{Name: "off", Enabled: false, Interface: "eth1", Address: "198.51.100.1"},
	}})
	tick(m, 5)

	if s := m.Statuses()[0]; s.Online || s.Unknown || s.LossPercent == 0 {
		t.Errorf("the only gateway should still be probed and marked down: %+v", s)
	}
	if demoted, _ := r.calls(); len(demoted) != 0 {
		t.Errorf("demoted = %v, want nothing: there is nowhere to fail over to", demoted)
	}
}

// Losing the second gateway must not strand the first one without a
// default route. Only the demote half is held back on a single gateway, so
// one demoted while it had a partner is restored when that partner goes.
func TestRemovingTheSecondGatewayRestoresTheDemotedOne(t *testing.T) {
	t.Parallel()
	m, prober, router := newTestMonitor(t)
	tick(m, 2)
	prober.setFail("203.0.113.1", true)
	tick(m, 3)
	if demoted, _ := router.calls(); len(demoted) != 1 || demoted[0] != "primary" {
		t.Fatalf("demoted = %v, want the primary once", demoted)
	}

	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "primary", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Priority: 0},
	}})
	tick(m, 1)
	if _, restored := router.calls(); len(restored) != 1 || restored[0] != "primary" {
		t.Errorf("restored = %v, want the primary back: it is the only gateway left", restored)
	}
}

func TestMonitorResolvesDynamicGateways(t *testing.T) {
	t.Parallel()
	p := &fakeProber{fail: map[string]bool{}}
	r := &fakeRouter{resolveTo: map[string]string{}}
	m := New(p, r, slog.New(slog.DiscardHandler))
	m.Configure(&model.Config{Gateways: []model.Gateway{{Name: "wan", Enabled: true, Interface: "eth0"}}})

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
	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "primary", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Priority: 0},
		{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.2", Priority: 1},
	}})
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
	if !netnstest.Enter(t) {
		return
	}

	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatalf("lo: %v", err)
	}
	if err := netlink.SetLinkUp(lo.Index); err != nil {
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
	if err := netlink.AddRoute(netlink.Route{LinkIndex: lo.Index, Dst: dst}); err != nil {
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
// router with no gateways at all: a router that only caps its LAN still has
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

// fakePolicy records the plans the tick hands policy routing.
type fakePolicy struct {
	mu    sync.Mutex
	plans [][]policy.Target
}

func (f *fakePolicy) Sync(targets []policy.Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans = append(f.plans, targets)
	return nil
}

func (f *fakePolicy) last(t *testing.T) []policy.Target {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.plans) == 0 {
		t.Fatal("the tick handed policy routing nothing")
	}
	return f.plans[len(f.plans)-1]
}

// carrying names the next hop a policy target routes through: the first
// online hop of the best tier that has one, or nothing when the target
// falls back to the main table.
func carrying(plan []policy.Target, name string) string {
	for _, t := range plan {
		if t.Name != name {
			continue
		}
		for _, tier := range t.Tiers {
			for _, h := range tier {
				if h.Online {
					return h.Address
				}
			}
		}
	}
	return ""
}

// Policy routing follows the probes. Before any verdict every gateway
// counts, so a router that has just booted still routes; a gateway that
// takes its address from DHCP is routed through the address it was given;
// and rules sent through a group move to the next tier when its line dies
// and back when it recovers.
func TestPolicyRoutingFollowsTheProbes(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{
		Version: model.SchemaVersion,
		Gateways: []model.Gateway{
			{Name: "fibre", Enabled: true, Interface: "eth0", Address: "203.0.113.1"},
			{Name: "lte", Enabled: true, Interface: "eth1", Priority: 1},
		},
		GatewayGroups: []model.GatewayGroup{{Name: "failover", Enabled: true, Members: []model.GatewayMember{
			{Gateway: "fibre"},
			{Gateway: "lte", Tier: 1},
		}}},
	}
	prober := &fakeProber{fail: map[string]bool{}}
	pol := &fakePolicy{}
	m := &Monitor{
		Prober: prober, Router: &fakeRouter{resolveTo: map[string]string{"lte": "198.51.100.1"}},
		Log: slog.New(slog.DiscardHandler), Policy: pol,
		Source: func() *model.Config { return cfg },
	}

	tick(m, 1)
	plan := pol.last(t)
	if got := carrying(plan, "failover"); got != "203.0.113.1" {
		t.Errorf("before any verdict the group routes via %q, want its best tier", got)
	}
	if got := carrying(plan, "lte"); got != "198.51.100.1" {
		t.Errorf("the DHCP line routes via %q, want the address its lease gave", got)
	}

	prober.setFail("203.0.113.1", true)
	tick(m, FailAfter)
	plan = pol.last(t)
	if got := carrying(plan, "failover"); got != "198.51.100.1" {
		t.Errorf("with the fibre down the group routes via %q, want the next tier", got)
	}
	if got := carrying(plan, "fibre"); got != "" {
		t.Errorf("a dead line still routes its own rules via %q, want them back on the main table", got)
	}

	prober.setFail("203.0.113.1", false)
	tick(m, RiseAfter)
	if got := carrying(pol.last(t), "failover"); got != "203.0.113.1" {
		t.Errorf("with the fibre back the group routes via %q, want the fibre", got)
	}
}

// Every line goes to policy routing, up while a gateway on it answers or
// while no gateway watches it. Only WireGuard's answers heed that: a
// tunnel this router dials has to be able to follow the default route off
// a dead line.
func TestALineIsUpWhileAGatewayOnItAnswers(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{
		Version: model.SchemaVersion,
		Zones:   []model.Zone{{Name: "wan", External: true}},
		Interfaces: []model.Interface{
			{Name: "eth0", Zone: "wan", Enabled: true},
			{Name: "eth1", Zone: "wan", Enabled: true},
			{Name: "eth2", Zone: "wan", Enabled: true},
		},
		Gateways: []model.Gateway{
			{Name: "fibre", Enabled: true, Interface: "eth0", Address: "203.0.113.1"},
			{Name: "lte", Enabled: true, Interface: "eth1", Address: "198.51.100.1", Priority: 1},
		},
	}
	prober := &fakeProber{fail: map[string]bool{}}
	pol := &fakePolicy{}
	m := &Monitor{
		Prober: prober, Router: &fakeRouter{}, Log: slog.New(slog.DiscardHandler), Policy: pol,
		Source: func() *model.Config { return cfg },
	}
	up := func(iface string) bool {
		t.Helper()
		for _, target := range pol.last(t) {
			if target.Line == iface {
				return target.Up
			}
		}
		t.Fatalf("no line %s in the plan", iface)
		return false
	}

	tick(m, 1)
	for _, iface := range []string{"eth0", "eth1", "eth2"} {
		if !up(iface) {
			t.Errorf("%s is down before any verdict", iface)
		}
	}
	prober.setFail("198.51.100.1", true)
	tick(m, FailAfter)
	if up("eth1") {
		t.Error("eth1 is up with its only gateway down")
	}
	if !up("eth0") || !up("eth2") {
		t.Error("a line whose gateway answers, or that has none, went down")
	}
}

// A dead gateway's route that something puts back, networkd renewing a
// lease for one, is moved off again on the next tick.
func TestARouteThatComesBackIsMovedOffAgain(t *testing.T) {
	t.Parallel()
	m, prober, router := newTestMonitor(t)
	tick(m, 2)
	prober.setFail("203.0.113.1", true)
	tick(m, FailAfter)
	router.putBack("primary")
	tick(m, 1)
	if demoted, restored := router.calls(); len(demoted) != 2 || len(restored) != 0 {
		t.Errorf("demoted = %v, restored = %v, want the primary moved off twice", demoted, restored)
	}
}

// A gateway that is removed, disabled, or moved to another interface or
// address leaves routes nobody watches, so the router is asked to clear
// them. A new monitor address changes nothing about the routes.
func TestAGatewayNoLongerWatchedIsForgotten(t *testing.T) {
	t.Parallel()
	m, _, router := newTestMonitor(t)
	tick(m, 2)
	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "primary", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Monitor: "192.0.2.53"},
		{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.9", Priority: 1},
	}})
	tick(m, 1)
	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "primary", Enabled: false, Interface: "eth0", Address: "203.0.113.1"},
		{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.9", Priority: 1},
	}})
	tick(m, 1)
	router.mu.Lock()
	defer router.mu.Unlock()
	if !slices.Equal(router.forgotten, []string{"backup", "primary"}) {
		t.Errorf("forgotten = %v, want the backup's old address, then the disabled primary", router.forgotten)
	}
}

// statusOf finds one gateway among the monitor's statuses.
func statusOf(t *testing.T, m *Monitor, name string) Status {
	t.Helper()
	for _, s := range m.Statuses() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no status for %s", name)
	return Status{}
}

// targetOf finds one policy target in a plan.
func targetOf(t *testing.T, plan []policy.Target, name string) policy.Target {
	t.Helper()
	for _, target := range plan {
		if target.Name == name {
			return target
		}
	}
	t.Fatalf("no target %s in the plan", name)
	return policy.Target{}
}

// A tunnel gateway is probed at the monitor beyond its tunnel and never
// touches the main table: it is not resolved, demoted, restored or active,
// and while it is down the one WAN of a router stays where it is. Policy
// routing sends its rules into the tunnel, and blocks them while it is
// down.
func TestATunnelGatewayIsProbedButNeverMovesTheDefaultRoute(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{
		Version: model.SchemaVersion,
		Interfaces: []model.Interface{{
			Name: "wg1", Enabled: true, IPv4: model.IPv4{Mode: model.AddrStatic, Address: "10.66.1.2/32"},
			WireGuard: &model.WireGuard{Peers: []model.WireGuardPeer{
				{Name: "provider", Enabled: true, AllowedIPs: []string{"0.0.0.0/0"}},
			}},
		}},
		Gateways: []model.Gateway{
			{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"},
			{Name: "vpn", Enabled: true, Interface: "wg1", Monitor: "10.64.0.1"},
		},
	}
	prober := &fakeProber{fail: map[string]bool{}}
	// Were the tunnel gateway resolved like a DHCP line, it would get this.
	router := &fakeRouter{resolveTo: map[string]string{"vpn": "10.66.1.1"}}
	pol := &fakePolicy{}
	m := &Monitor{
		Prober: prober, Router: router, Log: slog.New(slog.DiscardHandler), Policy: pol,
		Source: func() *model.Config { return cfg },
	}

	tick(m, RiseAfter)
	vpn := statusOf(t, m, "vpn")
	if !vpn.Tunnel || vpn.Address != "" || !vpn.Online || vpn.Active || vpn.Monitor != "10.64.0.1" {
		t.Errorf("tunnel gateway = %+v, want online, no next hop, not active", vpn)
	}
	if !statusOf(t, m, "wan").Active {
		t.Error("the WAN does not carry the default route beside a tunnel gateway")
	}
	target := targetOf(t, pol.last(t), "vpn")
	if !target.Block || len(target.Tiers) != 1 || !target.Tiers[0][0].Device || !target.Tiers[0][0].Online {
		t.Errorf("policy target = %+v, want the tunnel, online, blocking when down", target)
	}

	prober.setFail("10.64.0.1", true)
	tick(m, FailAfter)
	if statusOf(t, m, "vpn").Online {
		t.Error("the tunnel gateway stayed up with its monitor gone")
	}
	if target := targetOf(t, pol.last(t), "vpn"); target.Online() {
		t.Error("policy routing still sends traffic into a tunnel that is down")
	}
	demoted, restored := router.calls()
	if len(demoted) != 0 || slices.Contains(restored, "vpn") {
		t.Errorf("demoted %v, restored %v; want the main table left alone", demoted, restored)
	}

	// Gone from the configuration, it is not forgotten like a line either:
	// it has no routes in the main table to clear.
	cfg = &model.Config{Version: model.SchemaVersion, Gateways: cfg.Gateways[:1]}
	tick(m, 1)
	if slices.Contains(router.forgotten, "vpn") {
		t.Error("a removed tunnel gateway had the main table searched for its routes")
	}
}

// A policy route taken out between two ticks, as networkd does when an
// apply has it reconfigure a link, is put back at once from what the last
// probes found, without probing again.
func TestARemovedPolicyRouteIsPutBackWithoutWaitingForAProbe(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{
		Version:  model.SchemaVersion,
		Gateways: []model.Gateway{{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"}},
	}
	prober := &fakeProber{fail: map[string]bool{}}
	pol := &fakePolicy{}
	removed := make(chan struct{}, 1)
	m := &Monitor{
		Prober: prober, Router: &fakeRouter{resolveTo: map[string]string{}}, Log: slog.New(slog.DiscardHandler),
		Policy: pol, Interval: time.Hour, Source: func() *model.Config { return cfg },
		Removed: func(context.Context) (<-chan struct{}, error) { return removed, nil },
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go m.Run(ctx)
	waitPlans := func(n int) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			pol.mu.Lock()
			got := len(pol.plans)
			pol.mu.Unlock()
			if got >= n {
				return
			}
		}
		t.Fatalf("policy routing was synced fewer than %d times", n)
	}
	waitPlans(1)
	prober.mu.Lock()
	probes := prober.probes
	prober.mu.Unlock()

	removed <- struct{}{}
	waitPlans(2)
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if prober.probes != probes {
		t.Errorf("putting the route back probed %d more times", prober.probes-probes)
	}
}
