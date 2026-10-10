// Package gateway watches upstream gateways and keeps the default route
// pointed at one that works.
package gateway

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/policy"
)

// The probe loop's defaults. It is deliberately unhurried: a router that
// flaps its default route on one lost packet is worse than one that takes
// a minute and a half to notice a dead line.
const (
	// DefaultInterval is how often the monitor looks for a gateway due its
	// probe; each is probed as often as its configuration says.
	DefaultInterval = time.Second
	DefaultTimeout  = 2 * time.Second
	// syncEvery is how often the routes, the policy tables and the queues
	// are put back between probes.
	syncEvery = 5 * time.Second
	// FailAfter consecutive losses take a gateway offline, RiseAfter
	// consecutive answers bring it back, unless the gateway says otherwise.
	FailAfter = model.DefaultDownAfterProbes
	RiseAfter = model.DefaultUpAfterProbes
	// history is how many probes the loss figure covers.
	history = 20
	// judgedProbes is how many probes, at least, slow and lossy are judged
	// over.
	judgedProbes = 10
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
	// Resolve finds a gateway's next hop in each family, empty where it
	// has none yet.
	Resolve(g Status) (v4, v6 string)
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
	// Online is the monitor's verdict; Unknown means it has not probed yet,
	// and NeverAnswered that it has failed without ever answering.
	Online        bool `json:"online"`
	Unknown       bool `json:"unknown"`
	NeverAnswered bool `json:"neverAnswered,omitempty"`
	// Active marks the gateway currently carrying the default route.
	Active bool `json:"active"`
	// Tunnel is a tunnel gateway: it sends rule traffic into its tunnel
	// and never carries the default route.
	Tunnel      bool      `json:"tunnel,omitempty"`
	LatencyMS   float64   `json:"latencyMs"`
	LossPercent float64   `json:"lossPercent"`
	Since       time.Time `json:"since,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
	// Families are the addresses the gateway is probed at, a family each.
	Families []FamilyStatus `json:"families,omitempty"`
	// Slow and Lossy are the families over the gateway's thresholds in its
	// last Span seconds of probes: the whole minutes that hold ten of them.
	Slow  []Over `json:"slow,omitempty"`
	Lossy []Over `json:"lossy,omitempty"`
	Span  int64  `json:"span"`

	// learned is set when the gateway takes its next hop from the network,
	// so the router looks for its routes by metric as well as by address.
	learned bool
}

// FamilyStatus is a gateway as the probes of one family find it.
type FamilyStatus struct {
	Family        string  `json:"family"`
	Address       string  `json:"address,omitempty"`
	Online        bool    `json:"online"`
	Unknown       bool    `json:"unknown"`
	NeverAnswered bool    `json:"neverAnswered,omitempty"`
	LatencyMS     float64 `json:"latencyMs"`
	LossPercent   float64 `json:"lossPercent"`
	LastError     string  `json:"lastError,omitempty"`
}

// Over is a family over a threshold in the last Span seconds of probes: its
// mean round trip or the share it lost, the limit, and since when.
type Over struct {
	Family      string    `json:"family"`
	LatencyMS   float64   `json:"latencyMs,omitempty"`
	LossPercent float64   `json:"lossPercent,omitempty"`
	Limit       int       `json:"limit"`
	Span        int64     `json:"span"`
	Since       time.Time `json:"since"`
}

// leg is one address a gateway is probed at: its next hop in a family,
// or its monitor.
type leg struct {
	family string
	hop    string
	online bool
	// unknown is a leg not judged yet; never one that failed before any
	// probe was answered; answered one with an answer since its state began.
	unknown   bool
	never     bool
	answered  bool
	fails     int
	rises     int
	results   []bool
	latency   time.Duration
	since     time.Time
	lastError string
}

func (l *leg) record(rtt time.Duration, err error, now time.Time, failAfter, riseAfter int) {
	l.results = append(l.results, err == nil)
	if len(l.results) > history {
		l.results = l.results[len(l.results)-history:]
	}
	if err != nil {
		l.lastError = err.Error()
		l.fails++
		l.rises = 0
		if (l.online || l.unknown) && l.fails >= failAfter {
			l.online, l.unknown, l.never, l.since = false, false, !l.answered, now
		}
		return
	}
	l.lastError = ""
	l.latency = rtt
	l.rises++
	l.fails = 0
	l.answered = true
	if (!l.online || l.unknown) && l.rises >= riseAfter {
		l.online, l.unknown, l.never, l.since = true, false, false, now
	}
}

func (l *leg) state() string {
	switch {
	case l.online:
		return StateUp
	case l.unknown:
		return ""
	case l.never:
		return StateNever
	}
	return StateDown
}

type state struct {
	gw model.Gateway
	// tunnel is fixed for the state's life: a gateway that stops or starts
	// being a tunnel gateway gets a new one.
	tunnel bool
	// address is the next hop policy routing uses: the gateway's own, or
	// the IPv4 one it learned.
	address string
	// next is when the gateway is probed next; zero is now.
	next      time.Time
	legs      []*leg
	online    bool
	unknown   bool
	never     bool
	since     time.Time
	lastError string
	// slow and lossy are the families over a threshold, by family.
	slow, lossy map[string]Over
}

func (st *state) leg(family string) *leg {
	for _, l := range st.legs {
		if l.family == family {
			return l
		}
	}
	return nil
}

// verdict is the gateway's state from its legs: up while one answers,
// down while one that answered before does not, never answered while
// none ever has.
func (st *state) verdict() string {
	worst := ""
	for _, l := range st.legs {
		switch s := l.state(); {
		case s == StateUp:
			return StateUp
		case s == StateDown:
			worst = StateDown
		case s == StateNever && worst == "":
			worst = StateNever
		}
	}
	return worst
}

func (st *state) set(verdict string, now time.Time) {
	st.online, st.unknown, st.never = verdict == StateUp, verdict == "", verdict == StateNever
	st.since = now
}

func (st *state) current() string {
	switch {
	case st.online:
		return StateUp
	case st.unknown:
		return ""
	case st.never:
		return StateNever
	}
	return StateDown
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
	// History, when set, keeps what the probes find and what changed.
	History *History
	// ProbeEvery, when set, probes every gateway this often whatever its
	// configuration says; the browser tests set it.
	ProbeEvery time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu     sync.Mutex
	states map[string]*state
	order  []string
	// dropped are gateways that stopped being watched since the last tick,
	// whose routes may still be demoted.
	dropped []Status
	// carriers are the gateways whose default routes the kernel used at
	// the last tick.
	carriers []string
	synced   time.Time
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// interval is how often a gateway is probed.
func (m *Monitor) interval(g model.Gateway) time.Duration {
	if m.ProbeEvery > 0 {
		return m.ProbeEvery
	}
	return g.ProbeInterval()
}

// judgedMinutes is how many whole minutes slow and lossy are judged over at
// a probe interval: enough to hold judgedProbes.
func judgedMinutes(interval time.Duration) int {
	return max(1, int((judgedProbes*interval+time.Minute-1)/time.Minute))
}

// New returns a monitor with production defaults.
func New(p Prober, r Router, log *slog.Logger) *Monitor {
	return &Monitor{Prober: p, Router: r, Interval: DefaultInterval, Timeout: DefaultTimeout, Log: log}
}

// Configure replaces the watched set with cfg's gateways, keeping the
// state of those that are still there. It is called after every apply.
func (m *Monitor) Configure(cfg *model.Config) {
	var changed []Event
	names := make([]string, 0, len(cfg.Gateways))
	defer func() {
		if m.History == nil {
			return
		}
		m.History.Keep(names)
		for _, e := range changed {
			m.Log.Info("gateway monitor changed", "gateway", e.Gateway, "monitor", e.Monitor, "was", e.Was)
			m.History.Note(e)
		}
	}()
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make(map[string]*state, len(cfg.Gateways))
	order := make([]string, 0, len(cfg.Gateways))
	for _, g := range cfg.Gateways {
		names = append(names, g.Name)
		if !g.Enabled {
			continue
		}
		tunnel := cfg.TunnelGateway(g)
		st, ok := m.states[g.Name]
		if ok && st.gw.Monitor != g.Monitor {
			changed = append(changed, Event{Gateway: g.Name, Kind: EventMonitor, Monitor: g.Monitor, Was: st.gw.Monitor})
		}
		if !ok || st.gw.Interface != g.Interface || st.gw.Address != g.Address || st.gw.Monitor != g.Monitor ||
			st.tunnel != tunnel {
			st = &state{gw: g, tunnel: tunnel, unknown: true, since: m.now()}
		} else {
			if st.gw.ProbeEverySeconds != g.ProbeEverySeconds {
				st.next = time.Time{}
			}
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
	m.tick(ctx, false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick(ctx, false)
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
func (m *Monitor) Tick(ctx context.Context) { m.tick(ctx, true) }

// tick probes the gateways that are due, or every one when all is set,
// and keeps the routes in step: after a probe, and every syncEvery.
func (m *Monitor) tick(ctx context.Context, all bool) {
	var cfg *model.Config
	if m.Source != nil {
		if cfg = m.Source(); cfg != nil {
			m.Configure(cfg)
		}
	}
	now := m.now()
	m.mu.Lock()
	states := make([]*state, 0, len(m.order))
	var due []*state
	for _, name := range m.order {
		st := m.states[name]
		states = append(states, st)
		if all || st.next.IsZero() || !now.Before(st.next) {
			due = append(due, st)
			st.next = nextProbe(st.next, now, m.interval(st.gw))
		}
	}
	timeout := m.Timeout
	dropped := m.dropped
	m.dropped = nil
	resync := all || len(due) > 0 || now.Sub(m.synced) >= syncEvery
	if resync {
		m.synced = now
	}
	m.mu.Unlock()
	m.forget(dropped)
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	for _, st := range due {
		m.probe(ctx, st, timeout)
	}
	m.mark(states)
	if !resync {
		return
	}
	m.applyRoutes(states)
	m.findCarriers(states)
	m.syncPolicy(cfg, states)
	m.syncShaping(cfg)
	if m.OnTick != nil && len(due) > 0 {
		m.OnTick()
	}
}

// nextProbe follows the schedule rather than the tick that ran the probe,
// so a second-long ticker does not make every probe a little later. One
// that is more than an interval behind starts afresh from now instead of
// firing the missed probes in a burst.
func nextProbe(next, now time.Time, interval time.Duration) time.Time {
	if !next.IsZero() && !now.Before(next) && now.Sub(next) < interval {
		return next.Add(interval)
	}
	return now.Add(interval)
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
	err := m.Policy.Sync(targets)
	if err == nil {
		return
	}
	// The hop policy routing has is the last probe's, so a line that loses
	// its carrier between probes has the kernel refuse it. That is the
	// outage, not a fault.
	if waiting := m.addressless(states); len(waiting) > 0 && unreachable(err) {
		for _, gw := range waiting {
			m.Log.Info("policy routing waits for an address", "gateway", gw.Name, "interface", gw.Interface, "err", err)
		}
		return
	}
	m.Log.Warn("could not update policy routing", "err", err)
}

// addressless are the gateways that learn their next hop and have none in
// the kernel now.
func (m *Monitor) addressless(states []*state) []model.Gateway {
	if m.Router == nil {
		return nil
	}
	var gws []model.Gateway
	m.mu.Lock()
	for _, st := range states {
		if !st.tunnel && st.gw.Address == "" {
			gws = append(gws, st.gw)
		}
	}
	m.mu.Unlock()
	return slices.DeleteFunc(gws, func(gw model.Gateway) bool {
		v4, _ := m.Router.Resolve(Status{Name: gw.Name, Interface: gw.Interface, Metric: gw.GatewayMetric(), learned: true})
		return v4 != ""
	})
}

// unreachable reports whether every error in err is the kernel refusing a
// next hop it has no route to.
func unreachable(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		errs := joined.Unwrap()
		return len(errs) > 0 && !slices.ContainsFunc(errs, func(e error) bool { return !unreachable(e) })
	}
	return errors.Is(err, syscall.ENETUNREACH) || strings.Contains(err.Error(), "network is unreachable")
}

// target is an address a gateway is probed at, in a family; an empty
// family is a gateway with nothing to probe yet.
type target struct {
	family string
	hop    string
	addr   string
}

func familyOf(address string) string {
	if a, err := model.ParseIP(address); err == nil && !a.Is4() {
		return FamilyIPv6
	}
	return FamilyIPv4
}

// targets are what a gateway is probed at this tick, and the next hop
// policy routing takes. A tunnel's probe goes to the monitor beyond it; a
// gateway with an address or a monitor is probed there alone; one that
// learns its next hops is probed at each, and keeps probing a family
// whose next hop went.
func (m *Monitor) targets(st *state) (model.Gateway, []target, string) {
	m.mu.Lock()
	gw, tunnel := st.gw, st.tunnel
	var have []string
	for _, l := range st.legs {
		have = append(have, l.family)
	}
	m.mu.Unlock()
	if tunnel {
		return gw, []target{{family: familyOf(gw.Monitor), addr: gw.Monitor}}, ""
	}
	v4, v6 := m.Router.Resolve(Status{
		Name: gw.Name, Interface: gw.Interface, Address: gw.Address, Metric: gw.GatewayMetric(), learned: gw.Address == "",
	})
	switch {
	case gw.Address != "":
		return gw, []target{{family: familyOf(gw.Address), hop: gw.Address, addr: cmp.Or(gw.Monitor, gw.Address)}}, gw.Address
	case gw.Monitor != "":
		family, hop := familyOf(gw.Monitor), v4
		if family == FamilyIPv6 {
			hop = v6
		}
		return gw, []target{{family: family, hop: hop, addr: gw.Monitor}}, v4
	}
	var out []target
	if v4 != "" || slices.Contains(have, FamilyIPv4) {
		out = append(out, target{family: FamilyIPv4, hop: v4, addr: v4})
	}
	if v6 != "" || slices.Contains(have, FamilyIPv6) {
		out = append(out, target{family: FamilyIPv6, hop: v6, addr: v6})
	}
	if len(out) == 0 {
		out = []target{{}}
	}
	return gw, out, v4
}

type result struct {
	target
	rtt time.Duration
	err error
	at  time.Time
}

type legChange struct {
	family, was, now string
	since            time.Time
	err              string
}

// missing is the error for a family with nothing to probe: lost while its
// leg had a next hop, and until one comes back, otherwise not there yet.
func (m *Monitor) missing(st *state, family string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l := st.leg(family); l != nil && (l.hop != "" || l.lastError == errAddressLost.Error()) {
		return errAddressLost
	}
	return errNoAddress
}

func (m *Monitor) probe(ctx context.Context, st *state, timeout time.Duration) {
	gw, targets, hop := m.targets(st)
	results := make([]result, 0, len(targets))
	for _, t := range targets {
		var rtt time.Duration
		var err error
		if t.addr != "" {
			rtt, err = m.Prober.Probe(ctx, t.addr, gw.Interface, timeout)
		} else {
			err = m.missing(st, t.family)
		}
		results = append(results, result{target: t, rtt: rtt, err: err, at: m.now()})
	}
	if m.History != nil {
		for _, r := range results {
			if r.family != "" {
				m.History.Probe(gw.Name, r.family, gw.Monitor, r.at, r.rtt, r.err == nil)
			}
		}
	}

	now := m.now()
	m.mu.Lock()
	st.address = hop
	if targets[0].family != "" {
		st.legs = slices.DeleteFunc(st.legs, func(l *leg) bool { return l.family == "" })
	}
	var changes []legChange
	for _, r := range results {
		l := st.leg(r.family)
		if l == nil {
			l = &leg{family: r.family, unknown: true, since: now}
			if r.family != "" && m.History != nil {
				l.answered = m.History.Answered(gw.Name, r.family, gw.Monitor)
			}
			st.legs = append(st.legs, l)
			slices.SortStableFunc(st.legs, func(a, b *leg) int { return cmp.Compare(a.family, b.family) })
		}
		l.hop = r.hop
		was, since := l.state(), l.since
		l.record(r.rtt, r.err, now, gw.DownAfter(), gw.UpAfter())
		if l.state() != was {
			changes = append(changes, legChange{family: r.family, was: was, now: l.state(), since: since, err: l.lastError})
		}
	}
	prev, prevSince := st.current(), st.since
	verdict := st.verdict()
	if verdict != prev {
		st.set(verdict, now)
	}
	st.lastError = ""
	for _, l := range st.legs {
		if l.lastError != "" {
			st.lastError = l.lastError
			break
		}
	}
	legs := len(st.legs)
	lastError := st.lastError
	m.mu.Unlock()

	monitor := cmp.Or(gw.Monitor, results[0].addr)
	took := now.Sub(prevSince).Round(time.Second)
	switch {
	case verdict == prev:
	case verdict == StateUp && prev == StateDown:
		// Coming back is logged at the level going down was, so a journal
		// that keeps only warnings still says when the gateway returned.
		m.Log.Warn("gateway is up again", "gateway", gw.Name, "monitor", monitor, "down", took)
		m.note(Event{Gateway: gw.Name, Kind: EventUp, For: int64(took / time.Second)})
	case verdict == StateUp && prev == StateNever:
		m.Log.Warn("gateway answered", "gateway", gw.Name, "monitor", monitor, "after", took)
		m.note(Event{Gateway: gw.Name, Kind: EventAnswered, For: int64(took / time.Second)})
	case verdict == StateUp:
		m.Log.Info("gateway is up", "gateway", gw.Name, "monitor", monitor)
	case verdict == StateDown:
		m.Log.Warn("gateway is down", "gateway", gw.Name, "monitor", monitor, "err", lastError)
		m.note(Event{Gateway: gw.Name, Kind: EventDown, Error: lastError})
	case verdict == StateNever:
		m.Log.Warn("gateway has not answered", "gateway", gw.Name, "monitor", monitor, "err", lastError)
		m.note(Event{Gateway: gw.Name, Kind: EventNever, Error: lastError})
	}
	if verdict != StateUp || prev == StateDown || prev == StateNever || legs < 2 {
		return
	}
	for _, c := range changes {
		after := now.Sub(c.since).Round(time.Second)
		switch {
		case c.now == StateDown:
			m.Log.Warn("gateway stopped answering in a family", "gateway", gw.Name, "family", c.family, "err", c.err)
			m.note(Event{Gateway: gw.Name, Family: c.family, Kind: EventFamilyDown, Error: c.err})
		case c.now == StateNever:
			m.Log.Warn("gateway has not answered in a family", "gateway", gw.Name, "family", c.family, "err", c.err)
			m.note(Event{Gateway: gw.Name, Family: c.family, Kind: EventFamilyNever, Error: c.err})
		case c.now == StateUp && c.was != "":
			m.Log.Warn("gateway answers in a family again", "gateway", gw.Name, "family", c.family, "after", after)
			m.note(Event{Gateway: gw.Name, Family: c.family, Kind: EventFamilyUp, For: int64(after / time.Second)})
		}
	}
}

func (m *Monitor) note(e Event) {
	if m.History != nil {
		m.History.Note(e)
	}
}

// mark records each gateway's state in the history, and closes the
// minutes that are over.
func (m *Monitor) mark(states []*state) {
	if m.History == nil {
		return
	}
	now := m.now()
	type mark struct{ name, state, monitor string }
	marks := make([]mark, 0, len(states))
	m.mu.Lock()
	for _, st := range states {
		marks = append(marks, mark{st.gw.Name, st.current(), st.gw.Monitor})
	}
	m.mu.Unlock()
	for _, k := range marks {
		m.History.Mark(k.name, k.state, k.monitor, now)
	}
	m.judge(m.History.Advance(now))
}

type judged struct {
	e    Event
	msg  string
	span time.Duration
}

// judge holds the window ending at each minute that is over against its
// gateway's thresholds: a family that answered is slow while the window's
// mean round trip is above one, lossy while the window lost more of its
// probes than the other. A window that is not full judges nothing. A
// minute the gateway was not up in ends both, without a word: down says
// more.
func (m *Monitor) judge(minutes []Minute) {
	var out []judged
	m.mu.Lock()
	for _, mn := range minutes {
		st := m.states[mn.Gateway]
		if st == nil {
			continue
		}
		if mn.State != StateUp {
			st.slow, st.lossy = nil, nil
			continue
		}
		w := judgedMinutes(m.interval(st.gw))
		fam, full := m.History.window(mn.Gateway, mn.Start, w, st.gw.Monitor)
		if !full {
			continue
		}
		slowAt, lossyAt, span := st.gw.SlowAbove(), st.gw.LossyAbove(), int64(w)*60
		for i, f := range fam {
			family := familyName(i)
			if l := st.leg(family); l == nil || !l.answered || f.sent == 0 {
				continue
			}
			_, slow := f.slow(slowAt)
			_, lossy := f.lossy(lossyAt)
			out = st.cross(out, &st.slow, mn, family, slow,
				Over{Family: family, LatencyMS: round(f.mean()), Limit: slowAt, Span: span}, EventSlow, EventSlowEnd)
			out = st.cross(out, &st.lossy, mn, family, lossy,
				Over{Family: family, LossPercent: round(f.loss()), Limit: lossyAt, Span: span}, EventLossy, EventLossyEnd)
		}
	}
	m.mu.Unlock()
	for _, j := range out {
		m.Log.Warn(j.msg, "gateway", j.e.Gateway, "family", j.e.Family, "latency_ms", j.e.LatencyMS,
			"loss_percent", j.e.LossPercent, "limit", j.e.Limit, "over", j.span,
			"for", time.Duration(j.e.For)*time.Second)
		m.note(j.e)
	}
}

// cross notes a family going over a threshold or back under it. The
// caller holds m.mu.
func (st *state) cross(out []judged, overs *map[string]Over, mn Minute, family string, over bool, now Over,
	start, end string,
) []judged {
	was, had := (*overs)[family]
	span := time.Duration(now.Span) * time.Second
	switch {
	case over:
		now.Since = mn.Start
		if had {
			now.Since = was.Since
		}
		if *overs == nil {
			*overs = map[string]Over{}
		}
		(*overs)[family] = now
		if !had {
			msg := "gateway is slow"
			if start == EventLossy {
				msg = "gateway is losing packets"
			}
			out = append(out, judged{Event{Gateway: mn.Gateway, Family: family, Kind: start,
				LatencyMS: now.LatencyMS, LossPercent: now.LossPercent, Limit: now.Limit, Span: now.Span}, msg, span})
		}
	case had:
		delete(*overs, family)
		msg := "gateway is no longer slow"
		if end == EventLossyEnd {
			msg = "gateway no longer loses packets"
		}
		took := mn.Start.Add(time.Minute).Sub(was.Since).Round(time.Second)
		out = append(out, judged{Event{Gateway: mn.Gateway, Family: family, Kind: end,
			For: int64(took / time.Second)}, msg, span})
	}
	return out
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
		g := m.status(st, true)
		m.mu.Unlock()
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
		Name:          st.gw.Name,
		Interface:     st.gw.Interface,
		Address:       st.address,
		Monitor:       st.gw.Monitor,
		Priority:      st.gw.Priority,
		Metric:        st.gw.GatewayMetric(),
		Online:        st.online,
		Unknown:       st.unknown,
		NeverAnswered: st.never,
		Since:         st.since,
		LastError:     st.lastError,
		Tunnel:        st.tunnel,
		Span:          int64(judgedMinutes(m.interval(st.gw))) * 60,
		learned:       st.gw.Address == "",
	}
	if s.Address == "" {
		s.Address = st.gw.Address
	}
	if s.Monitor == "" {
		s.Monitor = s.Address
	}
	for _, l := range st.legs {
		if l.family == "" {
			continue
		}
		f := FamilyStatus{
			Family: l.family, Address: l.hop, Online: l.online, Unknown: l.unknown, NeverAnswered: l.never,
			LastError: l.lastError,
		}
		if l.latency > 0 {
			f.LatencyMS = float64(l.latency.Microseconds()) / 1000
		}
		if n := len(l.results); n > 0 {
			lost := 0
			for _, ok := range l.results {
				if !ok {
					lost++
				}
			}
			f.LossPercent = float64(lost) / float64(n) * 100
		}
		s.Families = append(s.Families, f)
	}
	if len(s.Families) > 0 {
		s.LatencyMS, s.LossPercent = s.Families[0].LatencyMS, s.Families[0].LossPercent
	} else if len(st.legs) > 0 && len(st.legs[0].results) > 0 {
		s.LossPercent = 100
	}
	if st.online {
		s.Slow, s.Lossy = overs(st.slow), overs(st.lossy)
	}
	return s
}

func overs(m map[string]Over) []Over {
	out := make([]Over, 0, len(m))
	for _, o := range m {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b Over) int { return cmp.Compare(a.Family, b.Family) })
	if len(out) == 0 {
		return nil
	}
	return out
}
