// Package gateway watches upstream gateways and keeps the default route
// pointed at one that works.
package gateway

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/policy"
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
)

// Prober sends one probe and reports the round trip time.
type Prober interface {
	Probe(ctx context.Context, address, iface string, timeout time.Duration) (time.Duration, error)
}

// Router applies the failover decision: a gateway that is down loses its
// default route, and gets it back when it recovers.
type Router interface {
	// Demote removes the default route through a gateway.
	Demote(g Status) error
	// Restore puts it back with its configured metric.
	Restore(g Status) error
	// Resolve fills in the next hop of a gateway that takes its address
	// from the network, and reports whether one exists yet.
	Resolve(g Status) (string, bool)
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
	Active      bool      `json:"active"`
	LatencyMS   float64   `json:"latencyMs"`
	LossPercent float64   `json:"lossPercent"`
	Since       time.Time `json:"since,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
}

type state struct {
	gw        model.Gateway
	address   string
	online    bool
	unknown   bool
	demoted   bool
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

	mu     sync.Mutex
	states map[string]*state
	order  []string
}

// New returns a monitor with production defaults.
func New(p Prober, r Router, log *slog.Logger) *Monitor {
	return &Monitor{Prober: p, Router: r, Interval: DefaultInterval, Timeout: DefaultTimeout, Log: log}
}

// Configure replaces the watched set, keeping the state of gateways that
// are still there. It is called after every apply.
func (m *Monitor) Configure(gateways []model.Gateway) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]*state, len(gateways))
	order := make([]string, 0, len(gateways))
	for _, g := range gateways {
		if !g.Enabled {
			continue
		}
		st, ok := m.states[g.Name]
		if !ok || st.gw.Interface != g.Interface || st.gw.Address != g.Address || st.gw.Monitor != g.Monitor {
			st = &state{gw: g, unknown: true, since: time.Now()}
		} else {
			st.gw = g
		}
		next[g.Name] = st
		order = append(order, g.Name)
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
	for {
		m.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick probes every gateway once and applies the routing decision.
func (m *Monitor) Tick(ctx context.Context) {
	var cfg *model.Config
	if m.Source != nil {
		if cfg = m.Source(); cfg != nil {
			m.Configure(cfg.Gateways)
		}
	}
	m.mu.Lock()
	states := make([]*state, 0, len(m.order))
	for _, name := range m.order {
		states = append(states, m.states[name])
	}
	timeout := m.Timeout
	m.mu.Unlock()
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	for _, st := range states {
		m.probe(ctx, st, timeout)
	}
	m.applyRoutes(states)
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
	m.mu.Lock()
	for _, st := range states {
		h := policy.Hop{
			Gateway:   st.gw.Name,
			Address:   st.address,
			Interface: st.gw.Interface,
			Online:    st.online || st.unknown,
		}
		if h.Address == "" {
			h.Address = st.gw.Address
		}
		hops[h.Gateway] = h
	}
	m.mu.Unlock()
	if err := m.Policy.Sync(policy.Plan(cfg, hops)); err != nil {
		m.Log.Warn("could not update policy routing", "err", err)
	}
}

func (m *Monitor) probe(ctx context.Context, st *state, timeout time.Duration) {
	m.mu.Lock()
	target := st.gw.Monitor
	addr := st.address
	m.mu.Unlock()

	if resolved, ok := m.Router.Resolve(Status{Name: st.gw.Name, Interface: st.gw.Interface, Address: st.gw.Address}); ok {
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
		st.online, st.unknown, st.since = true, false, time.Now()
		m.Log.Info("gateway is up", "gateway", st.gw.Name, "monitor", target, "rtt", rtt)
	}
}

// applyRoutes demotes dead gateways and restores recovered ones. The last
// usable default route is never removed: a router with no route at all is
// worse than one pointing at a gateway that might come back.
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
		switch {
		case !st2.online && !st2.unknown && !st2.demoted && anyOnline:
			if err := m.Router.Demote(m.status(&st2, false)); err != nil {
				m.Log.Warn("could not remove the route of a dead gateway", "gateway", st2.gw.Name, "err", err)
				continue
			}
			m.mu.Lock()
			st.demoted = true
			m.mu.Unlock()
			m.Log.Info("default route moved off a dead gateway", "gateway", st2.gw.Name)
		case (st2.online || !anyOnline) && st2.demoted:
			if err := m.Router.Restore(m.status(&st2, false)); err != nil {
				m.Log.Warn("could not restore a gateway route", "gateway", st2.gw.Name, "err", err)
				continue
			}
			m.mu.Lock()
			st.demoted = false
			m.mu.Unlock()
			m.Log.Info("gateway route restored", "gateway", st2.gw.Name)
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
	// The first gateway that is up carries the traffic.
	for i := range out {
		if out[i].Online && !out[i].Unknown {
			out[i].Active = true
			break
		}
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
