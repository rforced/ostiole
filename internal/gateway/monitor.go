// Package gateway watches upstream gateways and keeps the default route
// pointed at one that works.
package gateway

import (
	"context"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/policy"
)

// Defaults for the probe loop. They are deliberately unhurried: a router
// that flaps its default route on one lost packet is worse than one that
// takes twenty seconds to notice a dead line.
const (
	DefaultInterval = 5 * time.Second
	DefaultTimeout  = 2 * time.Second
	// FailAfter consecutive losses take a gateway offline, RiseAfter
	// consecutive answers bring it back.
	FailAfter = 3
	RiseAfter = 2
	// history is how many probes the loss figure covers.
	history = 20
	// settle is how long a removed policy route waits before the tables
	// are put back: networkd takes a link's routes out in a burst.
	settle = 200 * time.Millisecond
)

// Prober sends one probe and reports the round trip time.
type Prober interface {
	Probe(ctx context.Context, address, iface string, timeout time.Duration) (time.Duration, error)
}

// Router applies the failover decision: a gateway that is down has its
// default routes moved below every other, and back when it recovers. Both
// are asked every tick and report whether anything moved, so a route put
// back by something else, or a move that failed, is dealt with next time.
type Router interface {
	// Demote moves the default routes through a gateway below the rest.
	Demote(g Status) (bool, error)
	// Restore puts them back at their configured metric.
	Restore(g Status) (bool, error)
	// Forget clears up after a gateway nothing watches any more.
	Forget(g Status) error
	// Resolve fills in the next hop of a gateway that takes its address
	// from the network, and reports whether one exists yet.
	Resolve(g Status) (string, bool)
	// Carriers names the gateways whose default routes the kernel uses.
	Carriers(gs []Status) ([]string, error)
}

// Policy installs the routing tables and ip rules that make per-rule
// gateway selection work.
type Policy interface {
	Sync(targets []policy.Target) error
}

// Shaping keeps the traffic queues in place between applies. It rides the
// same tick because it is watching for the same thing: a link that comes
// and goes. The tick runs whether or not there are gateways to probe, so
// a router that only caps a LAN converges too.
type Shaping interface {
	Sync(cfg *model.Config) error
}

// Status is what the UI and the failover logic see.
type Status struct {
	Name      string `json:"name"`
	Interface string `json:"interface"`
	// Address is the next hop in use, resolved for dynamic gateways.
	Address  string `json:"address,omitempty"`
	Monitor  string `json:"monitor,omitempty"`
	Priority int    `json:"priority"`
	Metric   int    `json:"metric"`
	// Online is the monitor's verdict; Unknown means it has not probed yet.
	Online  bool `json:"online"`
	Unknown bool `json:"unknown"`
	// Active marks the gateway currently carrying the default route.
	Active bool `json:"active"`
	// Tunnel is a tunnel gateway: it sends rule traffic into its tunnel
	// and never carries the default route.
	Tunnel      bool      `json:"tunnel,omitempty"`
	LatencyMS   float64   `json:"latencyMs"`
	LossPercent float64   `json:"lossPercent"`
	Since       time.Time `json:"since,omitzero"`
	LastError   string    `json:"lastError,omitempty"`

	// learned is set when the gateway takes its next hop from the network,
	// so the router looks for its routes by metric as well as by address.
	learned bool
}

type state struct {
	gw model.Gateway
	// tunnel is fixed for the state's life: a gateway that stops or starts
	// being a tunnel gateway gets a new one.
	tunnel    bool
	address   string
	online    bool
	unknown   bool
	fails     int
	rises     int
	results   []bool
	latency   time.Duration
	since     time.Time
	lastError string
}

// Monitor probes every gateway and moves the default route when one dies.
type Monitor struct {
	Prober   Prober
	Router   Router
	Interval time.Duration
	Timeout  time.Duration
	Log      *slog.Logger
	// Source, when set, is read before every tick so the monitor follows
	// the saved configuration however it was changed: the API, the CLI, or
	// a rollback.
	Source func() *model.Config
	// Policy, when set, keeps the policy routing tables and ip rules in
	// step with what the probes just learned.
	Policy Policy
	// Shaping, when set, puts back any queue that has gone missing since
	// the last apply: a dialled session that has only now come up, a link
	// that was unplugged, a helper device somebody removed by hand.
	Shaping Shaping
	// OnTick, when set, is called after every pass, so the crons page can
	// say when the router last probed.
	OnTick func()
	// Removed, when set, signals that something took a policy route out of
	// the kernel. networkd does when it reconfigures a link, which an apply
	// makes it do; the tables go back at once instead of at the next probe.
	Removed func(ctx context.Context) (<-chan struct{}, error)

	mu     sync.Mutex
	states map[string]*state
	order  []string
	// dropped are gateways that stopped being watched since the last tick,
	// whose routes may still be demoted.
	dropped []Status
	// carriers are the gateways whose default routes the kernel used at
	// the last tick.
	carriers []string
}

// New returns a monitor with production defaults.
func New(p Prober, r Router, log *slog.Logger) *Monitor {
	return &Monitor{Prober: p, Router: r, Interval: DefaultInterval, Timeout: DefaultTimeout, Log: log}
}

// Configure replaces the watched set with cfg's gateways, keeping the
// state of those that are still there. It is called after every apply.
func (m *Monitor) Configure(cfg *model.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]*state, len(cfg.Gateways))
	order := make([]string, 0, len(cfg.Gateways))
	for _, g := range cfg.Gateways {
		if !g.Enabled {
			continue
		}
		tunnel := cfg.TunnelGateway(g)
		st, ok := m.states[g.Name]
		if !ok || st.gw.Interface != g.Interface || st.gw.Address != g.Address || st.gw.Monitor != g.Monitor ||
			st.tunnel != tunnel {
			st = &state{gw: g, tunnel: tunnel, unknown: true, since: time.Now()}
		} else {
			st.gw = g
		}
		next[g.Name] = st
		order = append(order, g.Name)
	}
	// A gateway that is gone, or now names another interface or address,
	// leaves routes nothing is watching. A new monitor address alone does
	// not: the routes are the same gateway's, and its next verdict moves
	// them.
	for name, st := range m.states {
		now := next[name]
		if now == nil || now.gw.Interface != st.gw.Interface || now.gw.Address != st.gw.Address {
			m.dropped = append(m.dropped, m.status(st, true))
		}
	}
	m.states, m.order = next, order
}

// Run probes until the context is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	interval := m.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	var removed <-chan struct{}
	if m.Removed != nil {
		ch, err := m.Removed(ctx)
		if err != nil {
			m.Log.Warn("cannot watch the policy routes; one that goes is put back at the next probe", "err", err)
		}
		removed = ch
	}
	var settled <-chan time.Time
	m.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick(ctx)
		case _, ok := <-removed:
			if !ok {
				removed = nil
				continue
			}
			settled = time.After(settle)
		case <-settled:
			settled = nil
			m.Resync()
		}
	}
}

// Resync puts the policy tables back as the last probes left them,
// without probing again.
func (m *Monitor) Resync() {
	if m.Source == nil {
		return
	}
	cfg := m.Source()
	if cfg == nil {
		return
	}
	m.mu.Lock()
	states := make([]*state, 0, len(m.order))
	for _, name := range m.order {
		states = append(states, m.states[name])
	}
	m.mu.Unlock()
	m.syncPolicy(cfg, states)
}

// Tick probes every gateway once and applies the routing decision.
func (m *Monitor) Tick(ctx context.Context) {
	var cfg *model.Config
	if m.Source != nil {
		if cfg = m.Source(); cfg != nil {
			m.Configure(cfg)
		}
	}
	m.mu.Lock()
	states := make([]*state, 0, len(m.order))
	for _, name := range m.order {
		states = append(states, m.states[name])
	}
	timeout := m.Timeout
	dropped := m.dropped
	m.dropped = nil
	m.mu.Unlock()
	m.forget(dropped)
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	for _, st := range states {
		m.probe(ctx, st, timeout)
	}
	m.applyRoutes(states)
	m.findCarriers(states)
	m.syncPolicy(cfg, states)
	m.syncShaping(cfg)
	if m.OnTick != nil {
		m.OnTick()
	}
}

// syncShaping puts back anything the queues have lost. It warns rather
// than failing the tick: the gateways still have to be probed and the
// routes still have to move, whatever the shaper makes of the kernel.
func (m *Monitor) syncShaping(cfg *model.Config) {
	if m.Shaping == nil || cfg == nil {
		return
	}
	if err := m.Shaping.Sync(cfg); err != nil {
		m.Log.Warn("could not keep traffic shaping in place", "err", err)
	}
}

// syncPolicy hands the policy routing installer the gateways as the probes
// have just found them. A gateway the monitor has no verdict on yet counts
// as usable: a router that has only just booted should still route.
func (m *Monitor) syncPolicy(cfg *model.Config, states []*state) {
	if m.Policy == nil || cfg == nil {
		return
	}
	hops := make(map[string]policy.Hop, len(states))
	// A line is up while a gateway on it answers, or while nothing watches
	// it.
	up, watched := map[string]bool{}, map[string]bool{}
	m.mu.Lock()
	for _, st := range states {
		address := st.address
		if address == "" {
			address = st.gw.Address
		}
		hops[st.gw.Name] = policy.NewHop(cfg, st.gw, address, st.online || st.unknown)
		watched[st.gw.Interface] = true
		up[st.gw.Interface] = up[st.gw.Interface] || st.online || st.unknown
	}
	m.mu.Unlock()
	targets := append(policy.Plan(cfg, hops), policy.Translations(cfg)...)
	targets = append(targets, policy.Lines(cfg, func(iface string) bool { return up[iface] || !watched[iface] })...)
	if err := m.Policy.Sync(targets); err != nil {
		m.Log.Warn("could not update policy routing", "err", err)
	}
}

func (m *Monitor) probe(ctx context.Context, st *state, timeout time.Duration) {
	m.mu.Lock()
	target := st.gw.Monitor
	addr := st.address
	m.mu.Unlock()

	// A tunnel has no next hop to find: the probe goes into the tunnel,
	// to the monitor beyond it.
	if st.tunnel {
		addr = ""
	} else if resolved, ok := m.Router.Resolve(Status{Name: st.gw.Name, Interface: st.gw.Interface, Address: st.gw.Address}); ok {
		addr = resolved
	} else if st.gw.Address != "" {
		addr = st.gw.Address
	}
	if target == "" {
		target = addr
	}

	var rtt time.Duration
	var err error
	if target == "" {
		err = errNoAddress
	} else {
		rtt, err = m.Prober.Probe(ctx, target, st.gw.Interface, timeout)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	st.address = addr
	st.results = append(st.results, err == nil)
	if len(st.results) > history {
		st.results = st.results[len(st.results)-history:]
	}
	if err != nil {
		st.lastError = err.Error()
		st.fails++
		st.rises = 0
		if (st.online || st.unknown) && st.fails >= FailAfter {
			st.online, st.unknown, st.since = false, false, time.Now()
			m.Log.Warn("gateway is down", "gateway", st.gw.Name, "monitor", target, "err", err)
		}
		return
	}
	st.lastError = ""
	st.latency = rtt
	st.rises++
	st.fails = 0
	if (!st.online || st.unknown) && st.rises >= RiseAfter {
		// Coming back is logged at the level going down was, so a journal
		// that keeps only warnings still says when the gateway returned.
		if !st.unknown {
			m.Log.Warn("gateway is up again", "gateway", st.gw.Name, "monitor", target, "rtt", rtt,
				"down", time.Since(st.since).Round(time.Second))
		} else {
			m.Log.Info("gateway is up", "gateway", st.gw.Name, "monitor", target, "rtt", rtt)
		}
		st.online, st.unknown, st.since = true, false, time.Now()
	}
}

// applyRoutes demotes dead gateways and restores the rest. A demoted route
// stays in the table below every other, so a router is never left with no
// default route at all, but moving the only one achieves nothing either.
//
// That guard is also what keeps failover off a router with one gateway:
// demoting needs some other gateway to be online or unknown, and the one
// being demoted is neither, so two have to be watched before anything
// moves. Restoring is not held back the same way, so a gateway demoted
// while it had a partner gets its route back when that partner is disabled
// or removed. Both ends are held by tests, which is the only thing keeping
// the single-gateway rule true if this guard is ever reworked.
func (m *Monitor) applyRoutes(states []*state) {
	if m.Router == nil {
		return
	}
	// A tunnel gateway has no default route to move, and counting it as a
	// line that works would demote the only WAN of a router with one.
	states = slices.DeleteFunc(slices.Clone(states), func(st *state) bool { return st.tunnel })
	anyOnline := false
	for _, st := range states {
		if st.online || st.unknown {
			anyOnline = true
		}
	}
	for _, st := range states {
		m.mu.Lock()
		st2 := *st
		m.mu.Unlock()
		g := m.status(&st2, false)
		switch {
		case !st2.online && !st2.unknown && anyOnline:
			moved, err := m.Router.Demote(g)
			if err != nil {
				m.Log.Warn("could not move the default route off a dead gateway", "gateway", g.Name, "err", err)
			} else if moved {
				m.Log.Info("default route moved off a dead gateway", "gateway", g.Name)
			}
		case st2.online || !anyOnline:
			moved, err := m.Router.Restore(g)
			if err != nil {
				m.Log.Warn("could not restore a gateway route", "gateway", g.Name, "err", err)
			} else if moved {
				m.Log.Info("gateway route restored", "gateway", g.Name)
			}
		}
	}
}

// findCarriers asks the kernel which gateways carry the default routes,
// once the routes have moved. The best gateway that is up need not be one:
// a route no gateway owns can have a lower metric.
func (m *Monitor) findCarriers(states []*state) {
	if m.Router == nil {
		return
	}
	gs := make([]Status, 0, len(states))
	m.mu.Lock()
	for _, st := range states {
		if !st.tunnel {
			gs = append(gs, m.status(st, true))
		}
	}
	m.mu.Unlock()
	names, err := m.Router.Carriers(gs)
	if err != nil {
		m.Log.Debug("could not read which gateway carries the default route", "err", err)
	}
	m.mu.Lock()
	m.carriers = names
	m.mu.Unlock()
}

// forget clears up after gateways that are no longer watched: removed,
// disabled, or moved to another interface or address.
func (m *Monitor) forget(dropped []Status) {
	if m.Router == nil {
		return
	}
	for _, g := range dropped {
		if g.Tunnel {
			continue
		}
		if err := m.Router.Forget(g); err != nil {
			m.Log.Warn("could not clear the routes of a gateway no longer watched", "gateway", g.Name, "err", err)
		}
	}
}

// Statuses reports every watched gateway, best priority first.
func (m *Monitor) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.status(m.states[name], true))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	for i := range out {
		out[i].Active = slices.Contains(m.carriers, out[i].Name)
	}
	return out
}

// status converts internal state; the caller holds the lock when locked
// is true.
func (m *Monitor) status(st *state, _ bool) Status {
	s := Status{
		Name:      st.gw.Name,
		Interface: st.gw.Interface,
		Address:   st.address,
		Monitor:   st.gw.Monitor,
		Priority:  st.gw.Priority,
		Metric:    st.gw.GatewayMetric(),
		Online:    st.online,
		Unknown:   st.unknown,
		Since:     st.since,
		LastError: st.lastError,
		Tunnel:    st.tunnel,
		learned:   st.gw.Address == "",
	}
	if s.Address == "" {
		s.Address = st.gw.Address
	}
	if s.Monitor == "" {
		s.Monitor = s.Address
	}
	if st.latency > 0 {
		s.LatencyMS = float64(st.latency.Microseconds()) / 1000
	}
	if n := len(st.results); n > 0 {
		lost := 0
		for _, ok := range st.results {
			if !ok {
				lost++
			}
		}
		s.LossPercent = float64(lost) / float64(n) * 100
	}
	return s
}
