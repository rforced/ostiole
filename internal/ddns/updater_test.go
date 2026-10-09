package ddns

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/dnsprovider"
	"ostiole/internal/model"
	"ostiole/internal/store"
)

// fakeProvider holds records in memory and counts what it is asked.
type fakeProvider struct {
	mu      sync.Mutex
	held    map[string][]netip.Addr // "name type" → addresses
	sets    int
	lookups int
	fail    error
}

func (p *fakeProvider) Lookup(_ context.Context, _, name string, t dnsprovider.RecordType) ([]netip.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lookups++
	if p.fail != nil {
		return nil, p.fail
	}
	return p.held[name+" "+string(t)], nil
}

func (p *fakeProvider) Set(_ context.Context, _, name string, t dnsprovider.RecordType, addr netip.Addr) ([]netip.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sets++
	if p.fail != nil {
		return nil, p.fail
	}
	was := p.held[name+" "+string(t)]
	p.held[name+" "+string(t)] = []netip.Addr{addr}
	return was, nil
}

func (p *fakeProvider) Test(_ context.Context, domains []string) (*dnsprovider.TestResult, error) {
	if p.fail != nil {
		return nil, p.fail
	}
	out := &dnsprovider.TestResult{Zones: []string{"example.com"}}
	for _, d := range domains {
		out.Domains = append(out.Domains, dnsprovider.DomainTest{Domain: d})
	}
	return out, nil
}

func (p *fakeProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sets
}

// memState is a StateStore in memory.
type memState struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *memState) ReadState(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.files[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return raw, nil
}

func (m *memState) WriteState(name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[name] = data
	return nil
}

// rig is an updater with a fake provider, clock and interfaces.
type rig struct {
	u      *Updater
	p      *fakeProvider
	cfg    *model.Config
	now    time.Time
	addrs  map[string][]Addr
	builds int
	state  *memState
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		p:     &fakeProvider{held: map[string][]netip.Addr{}},
		now:   time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
		addrs: map[string][]Addr{"wan0": {{IP: netip.MustParseAddr("203.0.113.7")}, {IP: netip.MustParseAddr("2001:db8::7")}}},
		state: &memState{},
		cfg: &model.Config{
			DNSProviders: []model.DNSProvider{{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"token": "t"}, Domains: []string{"example.com"}}},
			Services: model.Services{DDNS: model.DDNS{Records: []model.DDNSRecord{
				{ID: "ddns-home", Enabled: true, Name: "home.example.com", Interface: "wan0", IPv4: true, IPv6: true},
			}}},
		},
	}
	r.u = &Updater{
		Config:    func() *model.Config { return r.cfg },
		Addresses: func(iface string) ([]Addr, error) { return r.addrs[iface], nil },
		Build: func(model.DNSProvider, dnsprovider.Options) (Provider, error) {
			r.builds++
			return r.p, nil
		},
		State: r.state,
		Now:   func() time.Time { return r.now },
	}
	return r
}

func (r *rig) pass(t *testing.T) {
	t.Helper()
	r.u.Pass(context.Background())
}

func (r *rig) status(t *testing.T, typ dnsprovider.RecordType) Status {
	t.Helper()
	for _, st := range r.u.Status() {
		if st.Type == typ {
			return st
		}
	}
	t.Fatalf("no %s status in %+v", typ, r.u.Status())
	return Status{}
}

func TestUpdaterPublishesAndThenLeavesTheProviderAlone(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if st := r.status(t, dnsprovider.TypeA); st.State != StatePending {
		t.Errorf("before the first pass: %+v", st)
	}
	r.pass(t)
	if r.p.count() != 2 {
		t.Fatalf("first pass set %d records, want 2", r.p.count())
	}
	st := r.status(t, dnsprovider.TypeA)
	if st.State != StateCurrent || st.Address != "203.0.113.7" || !slices.Equal(st.Published, []string{"203.0.113.7"}) || !st.ChangedAt.Equal(r.now) {
		t.Errorf("after the first pass: %+v", st)
	}
	if st := r.status(t, dnsprovider.TypeAAAA); st.State != StateCurrent || st.Address != "2001:db8::7" {
		t.Errorf("AAAA after the first pass: %+v", st)
	}

	// Nothing changed, so the provider hears nothing.
	r.now = r.now.Add(time.Hour)
	r.pass(t)
	if r.p.count() != 2 {
		t.Errorf("an unchanged pass set %d more", r.p.count()-2)
	}

	// The WAN renumbers: only the A record is written.
	r.addrs["wan0"][0].IP = netip.MustParseAddr("203.0.113.8")
	r.pass(t)
	if r.p.count() != 3 || !slices.Equal(r.p.held["home.example.com A"], []netip.Addr{netip.MustParseAddr("203.0.113.8")}) {
		t.Errorf("after the renumber: %d sets, held %v", r.p.count(), r.p.held)
	}
	if st := r.status(t, dnsprovider.TypeA); !st.ChangedAt.Equal(r.now) {
		t.Errorf("changedAt = %v, want %v", st.ChangedAt, r.now)
	}
}

// Once a day the provider is read again, and a hand edit put back; a
// record that was right is not counted as changed.
func TestUpdaterReadsTheProviderDaily(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.pass(t)
	changed := r.status(t, dnsprovider.TypeA).ChangedAt
	r.now = r.now.Add(Reread)
	r.pass(t)
	if r.p.count() != 4 {
		t.Errorf("the daily read made %d sets, want 4 in all", r.p.count())
	}
	if st := r.status(t, dnsprovider.TypeA); !st.ChangedAt.Equal(changed) || !st.CheckedAt.Equal(r.now) {
		t.Errorf("after the daily read: %+v", st)
	}
}

func TestUpdaterBacksOffAfterAFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.cfg.Services.DDNS.Records[0].IPv6 = false
	r.p.fail = errors.New("Cloudflare refused the token for example.com")
	r.pass(t)
	st := r.status(t, dnsprovider.TypeA)
	if st.State != StateFailed || !strings.Contains(st.Error, "refused") || !st.NextTry.Equal(r.now.Add(time.Minute)) {
		t.Fatalf("after a failure: %+v", st)
	}
	r.now = r.now.Add(30 * time.Second)
	r.pass(t)
	if r.p.count() != 1 {
		t.Errorf("tried again inside the backoff")
	}
	r.now = r.now.Add(31 * time.Second)
	r.pass(t)
	if st := r.status(t, dnsprovider.TypeA); r.p.count() != 2 || !st.NextTry.Equal(r.now.Add(2*time.Minute)) {
		t.Errorf("second failure: %d sets, next try %v", r.p.count(), st.NextTry)
	}

	// A provider that asks for longer is given longer.
	r.p.fail = &dnsprovider.Error{Message: "Cloudflare asked to wait 5m0s", RetryAfter: 5 * time.Minute}
	r.now = r.now.Add(2 * time.Minute)
	r.pass(t)
	if st := r.status(t, dnsprovider.TypeA); !st.NextTry.Equal(r.now.Add(5 * time.Minute)) {
		t.Errorf("next try %v, want five minutes on", st.NextTry)
	}

	r.p.fail = nil
	r.now = r.now.Add(5 * time.Minute)
	r.pass(t)
	if st := r.status(t, dnsprovider.TypeA); st.State != StateCurrent || st.Error != "" || !st.NextTry.IsZero() {
		t.Errorf("after recovering: %+v", st)
	}
}

// A journal that keeps only warnings says a record failed and when it was
// reached again.
func TestARecordReachedAgainIsLoggedAtTheLevelOfItsFailure(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	r := newRig(t)
	r.u.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	r.cfg.Services.DDNS.Records[0].IPv6 = false
	r.p.fail = errors.New("Cloudflare refused the token for example.com")
	r.pass(t)
	r.p.fail = nil
	r.now = r.now.Add(time.Minute)
	r.pass(t)
	if got := buf.String(); !strings.Contains(got, "record not updated") || !strings.Contains(got, "record reached again") {
		t.Errorf("warnings:\n%s", got)
	}
}

func TestUpdateNowAsksAtOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.pass(t)
	if err := r.u.UpdateNow("ddns-home"); err != nil {
		t.Fatal(err)
	}
	if st := r.status(t, dnsprovider.TypeA); st.State != StateUpdating {
		t.Errorf("asked for: %+v", st)
	}
	r.pass(t)
	if r.p.count() != 4 {
		t.Errorf("Update now made %d sets, want 4 in all", r.p.count())
	}
	if err := r.u.UpdateNow("ddns-gone"); !errors.Is(err, ErrUnknownRecord) {
		t.Errorf("unknown record: %v", err)
	}
	r.cfg.Services.DDNS.Records[0].Enabled = false
	if err := r.u.UpdateNow("ddns-home"); !errors.Is(err, ErrRecordOff) {
		t.Errorf("record off: %v", err)
	}
}

// Without a public address nothing is sent, and the provider keeps what it
// has; a record that is off is left alone as well.
func TestUpdaterLeavesTheProviderWithoutAnAddress(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.addrs["wan0"] = []Addr{{IP: netip.MustParseAddr("192.168.1.2")}}
	r.pass(t)
	if r.p.count() != 0 || r.status(t, dnsprovider.TypeA).State != StateNoAddress {
		t.Errorf("private address: %d sets, %+v", r.p.count(), r.status(t, dnsprovider.TypeA))
	}
	r.cfg.Services.DDNS.Records[0].Enabled = false
	r.addrs["wan0"] = []Addr{{IP: netip.MustParseAddr("203.0.113.7")}}
	r.pass(t)
	if r.p.count() != 0 || r.status(t, dnsprovider.TypeA).State != StateOff {
		t.Errorf("record off: %d sets, %+v", r.p.count(), r.status(t, dnsprovider.TypeA))
	}
}

// A new token is a new client, and the record is read again with it.
func TestUpdaterFollowsTheProviderSettings(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.pass(t)
	r.cfg.DNSProviders[0].Settings = map[string]string{"token": "new"}
	r.pass(t)
	if r.builds != 2 || r.p.count() != 4 {
		t.Errorf("after a new token: %d builds, %d sets", r.builds, r.p.count())
	}
}

// When a record last changed survives a restart; it is read again anyway.
func TestUpdaterRemembersAcrossARestart(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.pass(t)
	changed := r.status(t, dnsprovider.TypeA).ChangedAt

	again := newRig(t)
	again.state = r.state
	again.u.State = r.state
	st := again.status(t, dnsprovider.TypeA)
	if st.State != StatePending || !st.ChangedAt.Equal(changed) || !slices.Equal(st.Published, []string{"203.0.113.7"}) {
		t.Errorf("after a restart: %+v", st)
	}
}

func TestCheckSaysWhatAnApplyWouldDo(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.p.held["home.example.com A"] = []netip.Addr{netip.MustParseAddr("198.51.100.5")}
	r.p.held["home.example.com AAAA"] = []netip.Addr{netip.MustParseAddr("2001:db8::7")}
	got, err := r.u.Check(context.Background(), r.cfg, r.cfg.Services.DDNS.Records[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Action != "change" || got[1].Action != "none" || got[0].Published[0] != "198.51.100.5" {
		t.Errorf("check = %+v", got)
	}
	if r.p.count() != 0 {
		t.Errorf("a check wrote %d records", r.p.count())
	}

	delete(r.p.held, "home.example.com A")
	r.addrs["wan0"] = r.addrs["wan0"][:1]
	got, _ = r.u.Check(context.Background(), r.cfg, r.cfg.Services.DDNS.Records[0])
	if got[0].Action != "create" || got[1].Action != StateNoAddress {
		t.Errorf("check = %+v", got)
	}

	other := r.cfg.Services.DDNS.Records[0]
	other.Name = "home.example.net"
	if _, err := r.u.Check(context.Background(), r.cfg, other); err == nil {
		t.Error("a name no provider holds was checked")
	}
}

// An apply that adds a record is acted on within a second or two, not at
// the next regular pass.
func TestRunPassesWhenTheRecordsChange(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var mu sync.Mutex
	cfg := r.cfg
	r.u.Config = func() *model.Config {
		mu.Lock()
		defer mu.Unlock()
		return cfg
	}
	r.u.Now = nil
	r.u.Every = time.Hour
	r.u.Watch = func(context.Context) (<-chan struct{}, error) { return nil, nil }
	passes := make(chan struct{}, 10)
	r.u.OnPass = func() { passes <- struct{}{} }
	ctx := t.Context()
	go r.u.Run(ctx)

	select {
	case <-passes:
	case <-time.After(5 * time.Second):
		t.Fatal("no first pass")
	}
	next := *cfg
	next.Services.DDNS.Records = append(slices.Clone(cfg.Services.DDNS.Records),
		model.DDNSRecord{ID: "ddns-new", Enabled: true, Name: "new.example.com", Interface: "wan0", IPv4: true})
	mu.Lock()
	cfg = &next
	mu.Unlock()
	select {
	case <-passes:
	case <-time.After(5 * time.Second):
		t.Fatal("the new record waited for the hourly pass")
	}
	if held := r.p.held["new.example.com A"]; len(held) != 1 {
		t.Errorf("the new record was not written: %v", r.p.held)
	}
}

// A record switched off and on again is read at the provider again: an
// edit made there meanwhile is put back.
func TestUpdaterRereadsARecordSwitchedBackOn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.cfg.Services.DDNS.Records[0].IPv6 = false
	r.pass(t)
	r.cfg.Services.DDNS.Records[0].Enabled = false
	r.pass(t)
	r.p.held["home.example.com A"] = []netip.Addr{netip.MustParseAddr("198.51.100.99")}
	r.cfg.Services.DDNS.Records[0].Enabled = true
	r.pass(t)
	if got := r.p.held["home.example.com A"]; !slices.Equal(got, []netip.Addr{netip.MustParseAddr("203.0.113.7")}) {
		t.Errorf("after switching back on the provider holds %v", got)
	}
}

// An address change the kernel announces is written at once, and one that
// leaves every record's address as it was starts nothing.
func TestRunPassesWhenAnAddressChanges(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var mu sync.Mutex
	addrs := slices.Clone(r.addrs["wan0"])
	r.u.Addresses = func(string) ([]Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(addrs), nil
	}
	r.u.Now = nil
	r.u.Every = time.Hour
	changes := make(chan struct{}, 1)
	r.u.Watch = func(context.Context) (<-chan struct{}, error) { return changes, nil }
	passes := make(chan struct{}, 10)
	r.u.OnPass = func() { passes <- struct{}{} }
	ctx := t.Context()
	go r.u.Run(ctx)
	<-passes

	changes <- struct{}{}
	select {
	case <-passes:
		t.Fatal("a notice that changed nothing started a pass")
	case <-time.After(1500 * time.Millisecond):
	}

	mu.Lock()
	addrs[0].IP = netip.MustParseAddr("203.0.113.8")
	mu.Unlock()
	changes <- struct{}{}
	select {
	case <-passes:
	case <-time.After(5 * time.Second):
		t.Fatal("the new address waited for the hourly pass")
	}
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	if got := r.p.held["home.example.com A"]; !slices.Equal(got, []netip.Addr{netip.MustParseAddr("203.0.113.8")}) {
		t.Errorf("the provider holds %v", got)
	}
}

// A failed record is tried again when its backoff ends, not at the hour.
func TestNextPassComesForARetry(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	regular := r.now.Add(time.Hour)
	if got := r.u.next(regular); !got.Equal(regular) {
		t.Errorf("with nothing failing the next pass is %v, want %v", got, regular)
	}
	r.p.fail = errors.New("Cloudflare: 1000 something")
	r.pass(t)
	if got := r.u.next(regular); !got.Equal(r.now.Add(time.Minute)) {
		t.Errorf("after a failure the next pass is %v, want a minute on", got)
	}
}

// A provider is tested with the credentials and domains it is given,
// lower case and without a trailing dot.
func TestTestProviderReadsItsDomains(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got, err := r.u.TestProvider(context.Background(), model.DNSProvider{ID: "cf", Kind: "cloudflare", Domains: []string{"Example.COM.", " "}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Domains) != 1 || got.Domains[0].Domain != "example.com" {
		t.Errorf("tested %+v", got.Domains)
	}
}

// Only a kind offered for dynamic DNS builds a client here.
func TestBuildKeepsToDynamicKinds(t *testing.T) {
	t.Parallel()
	if _, err := Build(model.DNSProvider{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"token": "t"}}, dnsprovider.Options{}); err != nil {
		t.Errorf("cloudflare: %v", err)
	}
	_, err := Build(model.DNSProvider{ID: "x", Kind: "exec", Settings: map[string]string{"program": "/bin/true"}}, dnsprovider.Options{})
	if err == nil || err.Error() != "exec cannot keep a dynamic DNS record" {
		t.Errorf("exec: %v", err)
	}
}
