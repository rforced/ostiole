package ddns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sync"
	"time"

	"ostiole/internal/dnsprovider"
	"ostiole/internal/model"
	"ostiole/internal/netlink"
)

// Every is how often a pass runs by itself: to retry, to put back a hand
// edit, and to catch an address change the kernel's notices missed. A
// change the kernel announces starts a pass at once.
const Every = time.Hour

// Reread is how long a record goes unread at its provider, so an edit made
// there by hand is put back within a day.
const Reread = 24 * time.Hour

// maxBackoff is the longest a failing record waits before the next try.
const maxBackoff = time.Hour

// StateFile keeps what each record was last set to, and when, across
// restarts.
const StateFile = "ddns.json"

// States of a record and type, as the page shows them.
const (
	// StateCurrent: the provider holds the interface's address.
	StateCurrent = "current"
	// StatePending: not read at the provider since the router started or
	// the record changed, or the address moved since the last pass.
	StatePending = "pending"
	// StateUpdating: a request is out, or asked for.
	StateUpdating = "updating"
	// StateFailed: the last request failed; Error says why.
	StateFailed = "failed"
	// StateNoAddress: the interface has no public address of the family,
	// so the provider is left as it is.
	StateNoAddress = "no-address"
	// StateOff: the record is switched off.
	StateOff = "off"
)

// Errors UpdateNow returns.
var (
	ErrUnknownRecord = errors.New("no such dynamic DNS record in the applied configuration")
	ErrRecordOff     = errors.New("this dynamic DNS record is off")
)

// Status is one record and type as the page shows it.
type Status struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Type      dnsprovider.RecordType `json:"type"`
	Interface string                 `json:"interface"`
	Provider  string                 `json:"provider,omitempty"`
	State     string                 `json:"state"`
	// Address is the interface's public address of the family.
	Address string `json:"address,omitempty"`
	// Published is what the provider held at the last read or write.
	Published []string  `json:"published,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	ChangedAt time.Time `json:"changedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
	NextTry   time.Time `json:"nextTry,omitzero"`
}

// StateStore keeps the updater's file; the configuration store is one.
type StateStore interface {
	ReadState(name string) ([]byte, error)
	WriteState(name string, data []byte) error
}

// Updater keeps the records in the running configuration in step with the
// addresses their interfaces carry.
type Updater struct {
	// Config is the configuration the router is running.
	Config func() *model.Config
	// Options builds the clients.
	Options dnsprovider.Options
	// Addresses reads an interface's addresses; nil reads the kernel.
	Addresses func(iface string) ([]Addr, error)
	// Build makes a provider's client; nil builds the real one.
	Build func(p model.DNSProvider, o dnsprovider.Options) (Provider, error)
	// State keeps what was set across restarts; nil keeps it in memory.
	State StateStore
	Log   *slog.Logger
	// OnPass runs after every pass, for the cron page.
	OnPass func()
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Every is how often a pass runs; zero is Every.
	Every time.Duration
	// Watch announces address changes; nil subscribes to the kernel's.
	Watch func(ctx context.Context) (<-chan struct{}, error)

	pass sync.Mutex // one pass at a time

	mu      sync.Mutex // guards what follows
	loaded  bool
	entries map[string]*entry
	clients map[string]client
	kick    chan struct{}
	// lastConfig is configDigest of what the last pass read.
	lastConfig string
}

// entry is what the updater knows of one record and type.
type entry struct {
	// seen is what the record was last read as: its name, interface and
	// provider. A change means the provider is asked again.
	seen      string
	address   netip.Addr
	noAddress bool
	published []netip.Addr
	readAt    time.Time
	changedAt time.Time
	err       string
	logged    string
	failures  int
	retryAt   time.Time
	updating  bool
	force     bool
}

// client is a built provider and the settings it was built from, so a new
// token builds a new one and the zone ids it learned go with the old.
type client struct {
	from string
	p    Provider
}

func entryKey(id string, t dnsprovider.RecordType) string { return id + " " + string(t) }

// Run passes over the records until ctx is done: every Every, at once when
// UpdateNow asks or an address a record publishes changes, within a second
// of an apply that changes a record or a provider, and when a failed record
// is due another try.
func (u *Updater) Run(ctx context.Context) {
	every := u.Every
	if every <= 0 {
		every = Every
	}
	look := time.NewTicker(time.Second)
	defer look.Stop()
	kick := u.kicks()
	changes := u.watch(ctx)
	var due time.Time
	for {
		if !time.Now().Before(due) || u.moved() {
			u.Pass(ctx)
			due = u.next(time.Now().Add(every))
		}
		select {
		case <-ctx.Done():
			return
		case <-look.C:
		case <-kick:
			due = time.Time{}
		case _, ok := <-changes:
			if !ok {
				changes = nil
			} else if u.stale() {
				due = time.Time{}
			}
		}
	}
}

// watch subscribes to address changes. Without them, a change waits for
// the next regular pass.
func (u *Updater) watch(ctx context.Context) <-chan struct{} {
	w := u.Watch
	if w == nil {
		w = netlink.WatchAddrs
	}
	changes, err := w(ctx)
	if err != nil {
		u.log().Warn("address changes are not watched; dynamic DNS waits for its hourly pass", "err", err)
		return nil
	}
	return changes
}

// next is when the next pass is due: at regular, or sooner when a failed
// record may be tried again.
func (u *Updater) next(regular time.Time) time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	due := regular
	for _, e := range u.entries {
		if e.err != "" && !e.retryAt.IsZero() && e.retryAt.Before(due) {
			due = e.retryAt
		}
	}
	return due
}

// stale reports whether an enabled record's interface has an address other
// than the one last published, which is what an address change has to
// answer before the provider is asked anything. It reads only the kernel.
func (u *Updater) stale() bool {
	cfg := u.config()
	for _, r := range cfg.Services.DDNS.Records {
		if !r.Enabled {
			continue
		}
		addrs, err := u.addresses(r.Interface)
		for _, t := range Types(r) {
			var addr netip.Addr
			if err == nil {
				addr, _ = Pick(addrs, t)
			}
			u.mu.Lock()
			e := u.entries[entryKey(r.ID, t)]
			moved := e == nil || e.address != addr
			u.mu.Unlock()
			if moved {
				return true
			}
		}
	}
	return false
}

// moved reports whether the records or the providers changed since the
// last pass read them.
func (u *Updater) moved() bool {
	d := configDigest(u.config())
	u.mu.Lock()
	defer u.mu.Unlock()
	return d != u.lastConfig
}

// configDigest names the part of a configuration a pass reads.
func configDigest(cfg *model.Config) string {
	raw, _ := json.Marshal(struct {
		Records   []model.DDNSRecord
		Providers []model.DNSProvider
	}{cfg.Services.DDNS.Records, cfg.DNSProviders})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func (u *Updater) kicks() chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.kick == nil {
		u.kick = make(chan struct{}, 1)
	}
	return u.kick
}

// UpdateNow has the next pass ask the provider about a record whatever it
// last found, and starts that pass.
func (u *Updater) UpdateNow(id string) error {
	cfg := u.config()
	r, ok := cfg.DDNSRecord(id)
	switch {
	case !ok:
		return ErrUnknownRecord
	case !r.Enabled:
		return ErrRecordOff
	}
	u.mu.Lock()
	for _, t := range Types(*r) {
		e := u.entry(entryKey(id, t))
		e.force, e.retryAt = true, time.Time{}
	}
	u.mu.Unlock()
	select {
	case u.kicks() <- struct{}{}:
	default:
	}
	return nil
}

// Pass looks at every record once.
func (u *Updater) Pass(ctx context.Context) {
	u.pass.Lock()
	defer u.pass.Unlock()
	u.load()
	cfg := u.config()
	u.mu.Lock()
	u.lastConfig = configDigest(cfg)
	u.mu.Unlock()
	live := map[string]bool{}
	for _, r := range cfg.Services.DDNS.Records {
		for _, t := range Types(r) {
			key := entryKey(r.ID, t)
			live[key] = true
			if r.Enabled {
				u.one(ctx, cfg, r, t, key)
				continue
			}
			// Nothing kept it while it was off, so switching it back on
			// reads the provider again.
			u.mu.Lock()
			u.entry(key).seen = ""
			u.mu.Unlock()
		}
	}
	u.mu.Lock()
	for key := range u.entries {
		if !live[key] {
			delete(u.entries, key)
		}
	}
	u.mu.Unlock()
	if u.OnPass != nil {
		u.OnPass()
	}
}

// one brings one record and type in step, if it has to.
func (u *Updater) one(ctx context.Context, cfg *model.Config, r model.DDNSRecord, t dnsprovider.RecordType, key string) {
	now := u.now()
	var addr netip.Addr
	addrs, err := u.addresses(r.Interface)
	if err == nil {
		addr, _ = Pick(addrs, t)
	}
	p, zone, found := cfg.ProviderFor(r.Name)

	u.mu.Lock()
	e := u.entry(key)
	e.address, e.noAddress = addr, !addr.IsValid()
	if !found {
		// Validation keeps this out of an applied configuration.
		e.force, e.err = false, fmt.Sprintf("no DNS provider holds %s", r.Name)
		u.mu.Unlock()
		return
	}
	if e.noAddress {
		// Nothing to publish: the provider is left as it is, and a
		// request someone made has nothing to do.
		e.force = false
		u.mu.Unlock()
		return
	}
	seen := fingerprint(r, p)
	need := e.force || e.seen != seen || !slices.Equal(e.published, []netip.Addr{addr}) || now.Sub(e.readAt) >= Reread
	if !need || (!e.force && now.Before(e.retryAt)) {
		u.mu.Unlock()
		return
	}
	e.updating = true
	u.mu.Unlock()

	name := model.NormalizeDomain(r.Name)
	was, err := u.set(ctx, *p, zone, name, t, addr)

	u.mu.Lock()
	defer u.mu.Unlock()
	e.updating, e.force = false, false
	if err != nil {
		e.failures++
		e.err = err.Error()
		wait := min(time.Minute<<min(e.failures-1, 10), maxBackoff)
		if pe := (*dnsprovider.Error)(nil); errors.As(err, &pe) && pe.RetryAfter > wait {
			wait = pe.RetryAfter
		}
		e.retryAt = now.Add(wait)
		if e.logged != e.err {
			e.logged = e.err
			u.log().Warn("dynamic DNS record not updated", "name", name, "type", t, "err", err)
		}
		return
	}
	if e.err != "" {
		// At the level its failure was logged at, so a journal that keeps
		// only warnings says it was put right.
		u.log().Warn("dynamic DNS record reached again", "name", name, "type", t)
	}
	e.seen, e.published, e.readAt = seen, []netip.Addr{addr}, now
	e.err, e.logged, e.failures, e.retryAt = "", "", 0, time.Time{}
	if !slices.Equal(was, e.published) {
		e.changedAt = now
		u.log().Info("dynamic DNS record updated", "name", name, "type", t, "address", addr, "was", was)
		u.save()
	}
}

// set asks the provider, bounded so one slow provider cannot hold up the
// pass for long.
func (u *Updater) set(ctx context.Context, p model.DNSProvider, zone, name string, t dnsprovider.RecordType, addr netip.Addr) ([]netip.Addr, error) {
	c, err := u.client(p)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, dnsprovider.RequestTimeout)
	defer cancel()
	return c.Set(ctx, zone, name, t, addr)
}

// client returns the provider's client, built again when its settings
// changed.
func (u *Updater) client(p model.DNSProvider) (Provider, error) {
	from := digest(p)
	u.mu.Lock()
	c, ok := u.clients[p.ID]
	u.mu.Unlock()
	if ok && c.from == from {
		return c.p, nil
	}
	built, err := u.build(p)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	if u.clients == nil {
		u.clients = map[string]client{}
	}
	u.clients[p.ID] = client{from: from, p: built}
	u.mu.Unlock()
	return built, nil
}

func (u *Updater) build(p model.DNSProvider) (Provider, error) {
	if u.Build != nil {
		return u.Build(p, u.Options)
	}
	return Build(p, u.Options)
}

// Status reports every record in the running configuration, A before AAAA.
func (u *Updater) Status() []Status {
	u.load()
	cfg := u.config()
	u.mu.Lock()
	defer u.mu.Unlock()
	out := []Status{}
	for _, r := range cfg.Services.DDNS.Records {
		provider := ""
		if p, _, ok := cfg.ProviderFor(r.Name); ok {
			provider = p.ID
		}
		for _, t := range Types(r) {
			st := Status{ID: r.ID, Name: r.Name, Type: t, Interface: r.Interface, Provider: provider}
			e := u.entries[entryKey(r.ID, t)]
			if e != nil {
				if e.address.IsValid() {
					st.Address = e.address.String()
				}
				for _, a := range e.published {
					st.Published = append(st.Published, a.String())
				}
				st.CheckedAt, st.ChangedAt, st.Error = e.readAt, e.changedAt, e.err
				if e.err != "" {
					st.NextTry = e.retryAt
				}
			}
			switch {
			case !r.Enabled:
				st.State = StateOff
			case e == nil:
				st.State = StatePending
			case e.updating || e.force:
				st.State = StateUpdating
			case e.noAddress:
				st.State = StateNoAddress
			case e.err != "":
				st.State = StateFailed
			case e.seen != "" && slices.Equal(e.published, []netip.Addr{e.address}):
				st.State = StateCurrent
			default:
				st.State = StatePending
			}
			out = append(out, st)
		}
	}
	return out
}

// CheckResult is what a pass would do with one record and type, found by
// reading the provider and writing nothing.
type CheckResult struct {
	Type dnsprovider.RecordType `json:"type"`
	// Address is the interface's public address of the family.
	Address string `json:"address,omitempty"`
	// Published is what the provider holds now.
	Published []string `json:"published"`
	// Action is create, change or none; no-address when the interface
	// has nothing to publish, several when the provider holds more than
	// one record for the name.
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Check reads what the provider holds for a record, with the credentials
// cfg gives, and says what a pass would do. It is how a record is tried
// before it is applied.
func (u *Updater) Check(ctx context.Context, cfg *model.Config, r model.DDNSRecord) ([]CheckResult, error) {
	p, zone, ok := cfg.ProviderFor(r.Name)
	if !ok {
		return nil, fmt.Errorf("no DNS provider holds %s", r.Name)
	}
	c, err := u.build(*p)
	if err != nil {
		return nil, err
	}
	addrs, _ := u.addresses(r.Interface)
	name := model.NormalizeDomain(r.Name)
	out := []CheckResult{}
	for _, t := range Types(r) {
		res := CheckResult{Type: t, Published: []string{}}
		addr, has := Pick(addrs, t)
		if has {
			res.Address = addr.String()
		}
		lookCtx, cancel := context.WithTimeout(ctx, dnsprovider.RequestTimeout)
		held, err := c.Lookup(lookCtx, zone, name, t)
		cancel()
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		for _, a := range held {
			res.Published = append(res.Published, a.String())
		}
		switch {
		case !has:
			res.Action = StateNoAddress
		case len(held) == 0:
			res.Action = "create"
		case len(held) > 1:
			res.Action = "several"
		case held[0] == addr:
			res.Action = "none"
		default:
			res.Action = "change"
		}
		out = append(out, res)
	}
	return out, nil
}

// TestProvider tries a provider's credentials as a draft holds them, and
// reads each of its domains. Nothing is written.
func (u *Updater) TestProvider(ctx context.Context, p model.DNSProvider) (*dnsprovider.TestResult, error) {
	c, err := u.build(p)
	if err != nil {
		return nil, err
	}
	var domains []string
	for _, d := range p.Domains {
		if d = model.NormalizeDomain(d); d != "" {
			domains = append(domains, d)
		}
	}
	return c.Test(ctx, domains)
}

// saved is the state file: what each record and type was last set to.
type saved struct {
	Published []netip.Addr `json:"published,omitempty"`
	ChangedAt time.Time    `json:"changedAt,omitzero"`
}

// load reads the state file once, so a restart does not forget when a
// record last changed. It does not count as a read at the provider.
func (u *Updater) load() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.loaded {
		return
	}
	u.loaded = true
	if u.State == nil {
		return
	}
	raw, err := u.State.ReadState(StateFile)
	if err != nil {
		return
	}
	var all map[string]saved
	if err := json.Unmarshal(raw, &all); err != nil {
		u.log().Warn("dynamic DNS state unreadable; starting afresh", "err", err)
		return
	}
	for key, s := range all {
		e := u.entry(key)
		e.published, e.changedAt = s.Published, s.ChangedAt
	}
}

// save writes the state file. Called with u.mu held.
func (u *Updater) save() {
	if u.State == nil {
		return
	}
	all := map[string]saved{}
	for key, e := range u.entries {
		if len(e.published) > 0 || !e.changedAt.IsZero() {
			all[key] = saved{Published: e.published, ChangedAt: e.changedAt}
		}
	}
	raw, err := json.Marshal(all)
	if err == nil {
		err = u.State.WriteState(StateFile, raw)
	}
	if err != nil {
		u.log().Warn("could not keep the dynamic DNS state", "err", err)
	}
}

// entry returns the entry for key, making it. Called with u.mu held.
func (u *Updater) entry(key string) *entry {
	if u.entries == nil {
		u.entries = map[string]*entry{}
	}
	e, ok := u.entries[key]
	if !ok {
		e = &entry{}
		u.entries[key] = e
	}
	return e
}

func (u *Updater) config() *model.Config {
	if u.Config != nil {
		if cfg := u.Config(); cfg != nil {
			return cfg
		}
	}
	return &model.Config{}
}

func (u *Updater) addresses(iface string) ([]Addr, error) {
	if u.Addresses != nil {
		return u.Addresses(iface)
	}
	return Addresses(iface)
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Updater) log() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.Default()
}

// fingerprint is what a record is read as: its name and interface, and the
// provider holding it, credentials included.
func fingerprint(r model.DDNSRecord, p *model.DNSProvider) string {
	return model.NormalizeDomain(r.Name) + " " + r.Interface + " " + digest(*p)
}

// digest names a provider's settings without holding them.
func digest(p model.DNSProvider) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}
