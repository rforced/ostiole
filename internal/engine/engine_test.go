package engine

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/shaping"
	"github.com/rforced/ostiole/internal/store"
)

// fakeNet is an in-memory backend; render picks network or services output.
type fakeNet struct {
	mu       sync.Mutex
	files    network.Files
	applies  []network.Files
	applyErr error
	// applyOnceErr fails the next Apply and then clears, which is what
	// a network that could not be configured looks like to a revert.
	applyOnceErr error
	services     bool
	// renews records every lease renewal asked for, as "link" or
	// "link!" when the lease was to be released first.
	renews []string
}

func (f *fakeNet) Renew(_ context.Context, link string, release bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if release {
		link += "!"
	}
	f.renews = append(f.renews, link)
	return nil
}

func (f *fakeNet) Name() string { return "fake" }

func (f *fakeNet) Render(cfg *model.Config) (network.Files, error) {
	if f.services {
		return (&services.Dnsmasq{}).Render(cfg)
	}
	return (&network.Networkd{}).Render(cfg)
}

func (f *fakeNet) Snapshot() (network.Files, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := network.Files{}
	for k, v := range f.files {
		out[k] = v
	}
	return out, nil
}

func (f *fakeNet) Apply(_ context.Context, files network.Files) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
	}
	if err := f.applyOnceErr; err != nil {
		f.applyOnceErr = nil
		return err
	}
	f.files = files
	f.applies = append(f.applies, files)
	return nil
}

func (f *fakeNet) current() network.Files {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files
}

// fakeRunner records rulesets and can be told to fail.
type fakeRunner struct {
	mu       sync.Mutex
	applied  []string
	checkErr error
	applyErr error
	// refuse fails an apply of any ruleset holding this text, the way
	// nft refuses one the kernel cannot load.
	refuse string
	// onCheck runs inside Check, which is where a caller that goes away
	// mid-apply goes.
	onCheck func()
	// onApply runs as each ruleset goes in, which is how a test puts the
	// loads in order with what else the engine does.
	onApply func()
	table   bool
}

func (f *fakeRunner) Check(_ context.Context, _ string) error {
	if f.onCheck != nil {
		f.onCheck()
	}
	return f.checkErr
}

func (f *fakeRunner) Apply(ctx context.Context, rs string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.onApply != nil {
		f.onApply()
	}
	if f.applyErr != nil {
		return f.applyErr
	}
	if f.refuse != "" && strings.Contains(rs, f.refuse) {
		return errors.New("nft: kernel refuses " + f.refuse)
	}
	f.applied = append(f.applied, rs)
	f.table = !strings.HasSuffix(strings.TrimSpace(rs), "delete table inet ostiole")
	return nil
}

func (f *fakeRunner) ListTableJSON(_ context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.table {
		return nil, nft.ErrNoTable
	}
	return []byte(`{"nftables":[{"rule":{"chain":"zone_lan","comment":"id:allow-lan","expr":[{"counter":{"packets":3,"bytes":30}}]}}]}`), nil
}

func (f *fakeRunner) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) == 0 {
		return ""
	}
	return f.applied[len(f.applied)-1]
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

func newEngine(t *testing.T) (*Engine, *fakeRunner, *store.Store) {
	t.Helper()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	log := slog.New(slog.DiscardHandler)
	return New(st, fr, nil, log), fr, st
}

func newEngineWithNet(t *testing.T) (*Engine, *fakeRunner, *fakeNet) {
	t.Helper()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	fn := &fakeNet{files: network.Files{}}
	return New(st, fr, fn, slog.New(slog.DiscardHandler)), fr, fn
}

func cfg(hostname string) *model.Config {
	return model.Starter(model.StarterOptions{Hostname: hostname, LAN: "eth1", LANAddress: "10.0.0.1/24", WAN: "eth0"})
}

// fakeClock records the zones an apply asked for.
type fakeClock struct {
	mu    sync.Mutex
	zones []string
	err   error
}

func (f *fakeClock) Apply(_ context.Context, zone string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones = append(f.zones, zone)
	return f.err
}

func (f *fakeClock) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.zones...)
}

func TestApplySetsTheTimezone(t *testing.T) {
	t.Parallel()
	e, _, _ := newEngine(t)
	clock := &fakeClock{}
	e.WithTimezone(clock)

	c := cfg("a")
	c.System.Timezone = "Europe/Berlin"
	if _, err := e.Apply(context.Background(), c, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := clock.seen(); len(got) != 1 || got[0] != "Europe/Berlin" {
		t.Errorf("zones applied = %v", got)
	}

	// A configuration that says nothing still pins the clock to UTC rather
	// than leaving whatever the router was imaged with.
	c = cfg("b")
	c.System.Timezone = ""
	if _, err := e.Apply(context.Background(), c, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := clock.seen(); len(got) != 2 || got[1] != "UTC" {
		t.Errorf("zones applied = %v", got)
	}
}

// The clock is not worth failing an apply over: the rules are what the
// operator is waiting on.
func TestApplySurvivesAClockThatWillNotMove(t *testing.T) {
	t.Parallel()
	e, _, _ := newEngine(t)
	e.WithTimezone(&fakeClock{err: errors.New("Failed to connect to bus")})
	if _, err := e.Apply(context.Background(), cfg("a"), ApplyOptions{}); err != nil {
		t.Fatalf("apply failed because the clock did: %v", err)
	}
}

func TestApplyImmediateCommits(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)
	res, err := e.Apply(context.Background(), cfg("a"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending || res.Archived != nil {
		t.Errorf("res = %+v", res)
	}
	if fr.count() != 1 || fr.last() != res.Ruleset {
		t.Errorf("runner applied %d rulesets", fr.count())
	}
	saved, err := st.Load()
	if err != nil || saved.System.Hostname != "a" {
		t.Fatalf("store = %v, %v", saved, err)
	}
	rs, _ := st.LoadRuleset()
	if rs != res.Ruleset {
		t.Error("ruleset not saved")
	}
	status, err := e.Status(context.Background())
	if err != nil || !status.Configured || !status.TableLoaded || status.Pending != nil {
		t.Errorf("status = %+v, %v", status, err)
	}
	c, err := e.Counters(context.Background())
	if err != nil || c["allow-lan"].Packets != 3 {
		t.Errorf("counters = %v, %v", c, err)
	}
}

func TestApplyConfirmCommits(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)
	if _, err := e.Apply(context.Background(), cfg("first"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(context.Background(), cfg("second"), ApplyOptions{ConfirmTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pending || res.Deadline.IsZero() {
		t.Fatalf("res = %+v", res)
	}
	if _, err := e.Apply(context.Background(), cfg("third"), ApplyOptions{}); !errors.Is(err, ErrPending) {
		t.Errorf("second apply while pending: %v, want ErrPending", err)
	}
	status, _ := e.Status(context.Background())
	if status.Pending == nil || status.Pending.Remaining <= 0 {
		t.Errorf("status.Pending = %+v", status.Pending)
	}
	archived, err := e.Confirm(context.Background())
	if err != nil || archived == nil {
		t.Fatalf("Confirm = %v, %v", archived, err)
	}
	saved, _ := st.Load()
	if saved.System.Hostname != "second" {
		t.Errorf("saved hostname = %q", saved.System.Hostname)
	}
	if fr.count() != 2 {
		t.Errorf("applied %d rulesets, want 2 (no revert)", fr.count())
	}
	if _, err := e.Confirm(context.Background()); !errors.Is(err, ErrNothingPending) {
		t.Errorf("double confirm: %v", err)
	}
}

func TestApplyRevertRestoresPrevious(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)
	first, _ := e.Apply(context.Background(), cfg("first"), ApplyOptions{})
	if _, err := e.Apply(context.Background(), cfg("second"), ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := e.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fr.last() != first.Ruleset {
		t.Error("revert did not re-apply the previous ruleset")
	}
	saved, _ := st.Load()
	if saved.System.Hostname != "first" {
		t.Errorf("store changed on revert: %q", saved.System.Hostname)
	}
	if err := e.Revert(context.Background()); !errors.Is(err, ErrNothingPending) {
		t.Errorf("double revert: %v", err)
	}
}

func TestApplyExpiresAndReverts(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)
	if _, err := e.Apply(context.Background(), cfg("unconfirmed"), ApplyOptions{ConfirmTimeout: 30 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for fr.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fr.count() != 2 {
		t.Fatalf("expected automatic revert, applied=%d", fr.count())
	}
	// First apply on a fresh store: nothing to go back to, so the
	// fallback goes in. A router with no table at all is wide open.
	if got := fr.last(); got != nft.Fallback(nil, []uint16{model.DefaultWebPort, 22}) {
		t.Errorf("revert ruleset = %q, want the fallback", got)
	}
	if st.Exists() {
		t.Error("store must not be written for an expired apply")
	}
	status, _ := e.Status(context.Background())
	if status.Pending != nil || !status.TableLoaded {
		t.Errorf("status after expiry = %+v", status)
	}
	if _, err := e.Confirm(context.Background()); !errors.Is(err, ErrNothingPending) {
		t.Errorf("confirm after expiry: %v", err)
	}
}

func TestApplyFailuresLeaveNoPending(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)

	fr.checkErr = errors.New("syntax error")
	if _, err := e.Apply(context.Background(), cfg("x"), ApplyOptions{ConfirmTimeout: time.Minute}); err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("err = %v", err)
	}
	fr.checkErr = nil

	fr.applyErr = errors.New("kernel said no")
	if _, err := e.Apply(context.Background(), cfg("x"), ApplyOptions{ConfirmTimeout: time.Minute}); err == nil {
		t.Fatal("expected apply error")
	}
	fr.applyErr = nil

	if _, err := e.Apply(context.Background(), &model.Config{}, ApplyOptions{}); err == nil {
		t.Fatal("expected validation error")
	}

	if fr.count() != 0 || st.Exists() {
		t.Errorf("side effects after failures: applied=%d exists=%v", fr.count(), st.Exists())
	}
	status, _ := e.Status(context.Background())
	if status.Pending != nil {
		t.Error("pending state left behind")
	}
}

func TestLoadAppliesTheConfirmedRulesetAtBoot(t *testing.T) {
	t.Parallel()
	e, fr, _ := newEngine(t)
	ctx := context.Background()
	// Nothing saved: the fallback, never an empty kernel.
	got, err := e.Load(ctx)
	if err != nil || got.Source != LoadedFallback || !errors.Is(got.Reason, ErrNoRuleset) {
		t.Fatalf("Load on empty store = %+v, %v", got, err)
	}
	res, _ := e.Apply(ctx, cfg("boot"), ApplyOptions{})
	if got, err := e.Load(ctx); err != nil || got.Source != LoadedSaved {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if fr.count() != 3 || fr.last() != res.Ruleset {
		t.Errorf("Load applied wrong ruleset")
	}
}

// A saved ruleset that is gone or damaged is rendered again from the saved
// configuration, which is the policy the router was confirmed to run.
func TestLoadRendersTheConfigurationWhenTheRulesetIsDamaged(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)
	ctx := context.Background()
	res, err := e.Apply(ctx, cfg("boot"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, store.RulesetFile), []byte("damaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fr.refuse = "damaged"
	got, err := e.Load(ctx)
	if err != nil || got.Source != LoadedRendered || got.Reason == nil {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if fr.last() != res.Ruleset {
		t.Error("the configuration rendered again is not the confirmed ruleset")
	}
	if status, _ := e.Status(ctx); status.Fallback != nil {
		t.Errorf("a rendered ruleset reported as the fallback: %+v", status.Fallback)
	}
}

// When nothing the configuration renders will load, the fallback keeps the
// management ports on the anti-lockout interfaces, and the dashboard hears
// of it until an apply is confirmed.
func TestLoadFallsBackWhenNothingElseLoads(t *testing.T) {
	t.Parallel()
	e, fr, _ := newEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("boot"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	fr.refuse = "chain zone_"
	got, err := e.Load(ctx)
	if err != nil || got.Source != LoadedFallback {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if want := nft.Fallback(cfg("boot"), nil); fr.last() != want {
		t.Errorf("fallback applied:\n%s\nwant:\n%s", fr.last(), want)
	}
	if !strings.Contains(fr.last(), `iifname "eth1" tcp dport`) {
		t.Errorf("the fallback does not keep the LAN's management ports:\n%s", fr.last())
	}
	status, _ := e.Status(ctx)
	if status.Fallback == nil || !strings.Contains(status.Fallback.Reason, "refuses") {
		t.Fatalf("status after a fallback = %+v", status.Fallback)
	}
	fr.refuse = ""
	if _, err := e.Apply(ctx, cfg("fixed"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if status, _ := e.Status(ctx); status.Fallback != nil {
		t.Errorf("a confirmed apply left the fallback note: %+v", status.Fallback)
	}
}

func TestNetworkAppliedAndRevertedTogether(t *testing.T) {
	t.Parallel()
	e, fr, fn := newEngineWithNet(t)

	res, err := e.Apply(context.Background(), cfg("first"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Network) == 0 || len(fn.current()) != len(res.Network) {
		t.Fatalf("network files not applied: %v", fn.current().Names())
	}
	firstFiles := fn.current()

	second := cfg("second")
	second.Interfaces[0].IPv4.Address = "10.9.9.1/24"
	if _, err := e.Apply(context.Background(), second, ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if fn.current().String() == firstFiles.String() {
		t.Fatal("second apply did not change network files")
	}
	if err := e.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fn.current().String() != firstFiles.String() {
		t.Error("revert did not restore the previous network files")
	}
	if fr.count() != 3 {
		t.Errorf("nft applies = %d, want 3", fr.count())
	}
	st, _ := e.Status(context.Background())
	if st.Network != "fake" {
		t.Errorf("status.Network = %q", st.Network)
	}
}

func TestNetworkFailureRevertsFirewall(t *testing.T) {
	t.Parallel()
	e, fr, fn := newEngineWithNet(t)
	if _, err := e.Apply(context.Background(), cfg("first"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	first := fr.last()
	firstNet := fn.current().String()
	fn.applyOnceErr = errors.New("networkctl exploded")
	_, err := e.Apply(context.Background(), cfg("second"), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "networkctl exploded") ||
		!strings.Contains(err.Error(), "network reverted") {
		t.Fatalf("err = %v", err)
	}
	if fr.last() != first {
		t.Error("firewall not reverted after network failure")
	}
	if fn.current().String() != firstNet {
		t.Error("network units not put back after network failure")
	}
	if st, _ := e.Status(context.Background()); st.Pending != nil {
		t.Error("pending left behind")
	}
}

func TestCheckSurfacesNetworkErrors(t *testing.T) {
	t.Parallel()
	e, _, _ := newEngineWithNet(t)
	c := cfg("x")
	c.Routes = []model.StaticRoute{{ID: "orphan", Enabled: true, Destination: "10.9.0.0/16", Gateway: "203.0.113.1"}}
	if _, err := e.Check(context.Background(), c); err == nil || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("err = %v", err)
	}
}

func TestServicesAppliedAndRevertedWithTheRest(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	fn := &fakeNet{files: network.Files{}}
	svc := &fakeNet{files: network.Files{}, services: true}
	e := New(st, fr, fn, slog.New(slog.DiscardHandler)).WithServices(svc)

	first := cfg("first")
	first.Services.DNS = model.DNSServer{Enabled: true, Upstreams: []string{"1.1.1.1"}}
	if _, err := e.Apply(context.Background(), first, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	before := svc.current().String()

	second := cfg("second")
	second.Services.DNS = model.DNSServer{Enabled: true, Upstreams: []string{"9.9.9.9"}}
	if _, err := e.Apply(context.Background(), second, ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if svc.current().String() == before {
		t.Fatal("services files unchanged after second apply")
	}
	if err := e.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if svc.current().String() != before {
		t.Error("services not reverted")
	}

	svc.applyErr = errors.New("dnsmasq refused")
	_, err := e.Apply(context.Background(), second, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "dnsmasq refused") {
		t.Fatalf("err = %v", err)
	}
	if fn.current().String() != (func() string { f, _ := (&network.Networkd{}).Render(first); return f.String() })() {
		t.Error("network not rolled back after services failure")
	}
}

// Watchers such as the gateway monitor follow the effective configuration,
// so a change is acted on inside its confirmation window and undone with it.
func TestEffectiveFollowsThePendingApply(t *testing.T) {
	t.Parallel()
	e, _, _ := newEngine(t)
	if got := e.Effective(); got != nil {
		t.Errorf("Effective before anything = %+v, want nil", got)
	}
	if _, err := e.Apply(context.Background(), cfg("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := e.Effective(); got == nil || got.System.Hostname != "saved" {
		t.Fatalf("Effective = %+v, want the saved configuration", got)
	}

	if _, err := e.Apply(context.Background(), cfg("pending"), ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if got := e.Effective(); got == nil || got.System.Hostname != "pending" {
		t.Fatalf("Effective while pending = %+v, want the unconfirmed configuration", got)
	}

	if err := e.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.Effective(); got == nil || got.System.Hostname != "saved" {
		t.Errorf("Effective after a revert = %+v, want the saved configuration back", got)
	}
}

// fakeShaper is the traffic shaping backend as the engine sees it: files
// in, files out, and a preflight it can be told to refuse.
type fakeShaper struct {
	fakeNet
	preflightErr error
	preflighted  int
}

func (f *fakeShaper) Render(cfg *model.Config) (network.Files, error) { return shaping.Render(cfg) }

func (f *fakeShaper) Preflight(_ context.Context, files network.Files) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflighted++
	if len(files) == 0 {
		return nil
	}
	return f.preflightErr
}

// shapedConfig is cfg with a speed on the WAN, which is what makes the
// shaping backend render anything at all.
func shapedConfig(hostname string, download int64) *model.Config {
	c := cfg(hostname)
	for i := range c.Interfaces {
		if c.Interfaces[i].Name == "eth0" {
			c.Interfaces[i].Shaping = &model.Shaping{Download: download, Upload: 20_000_000}
		}
	}
	return c
}

// Shaping goes in with everything else and comes back out with it: a
// figure that makes the router unreachable has to be undone by the same
// window that undoes a bad rule.
func TestShapingAppliedAndRevertedWithTheRest(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	fn := &fakeNet{files: network.Files{}}
	sh := &fakeShaper{fakeNet: fakeNet{files: network.Files{}}}
	e := New(st, fr, fn, slog.New(slog.DiscardHandler)).WithShaping(sh)

	if _, err := e.Apply(context.Background(), shapedConfig("first", 200_000_000), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	before := sh.current().String()
	if !strings.Contains(before, "bandwidth 200000000bit") {
		t.Fatalf("first apply installed %q", before)
	}

	// The figure a careless operator types: applied, then left to expire.
	if _, err := e.Apply(context.Background(), shapedConfig("second", 64_000), ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sh.current().String(), "bandwidth 64000bit") {
		t.Fatal("the second apply did not reach the shaper")
	}
	if err := e.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sh.current().String(); got != before {
		t.Errorf("revert left %q, want %q", got, before)
	}
}

// Shaping is applied last, so a failure there has to undo the firewall,
// the network, and the services behind it.
func TestShapingFailureRollsBackEverything(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	fn := &fakeNet{files: network.Files{}}
	svc := &fakeNet{files: network.Files{}, services: true}
	sh := &fakeShaper{fakeNet: fakeNet{files: network.Files{}}}
	e := New(st, fr, fn, slog.New(slog.DiscardHandler)).WithServices(svc).WithShaping(sh)

	first := shapedConfig("first", 200_000_000)
	if _, err := e.Apply(context.Background(), first, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	firstRuleset := fr.last()
	netBefore, svcBefore := fn.current().String(), svc.current().String()

	sh.applyErr = errors.New("tc refused")
	_, err := e.Apply(context.Background(), shapedConfig("second", 100_000_000), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "tc refused") {
		t.Fatalf("err = %v", err)
	}
	if fr.last() != firstRuleset {
		t.Error("the firewall was left with the ruleset of a failed apply")
	}
	if fn.current().String() != netBefore {
		t.Error("the network was not rolled back")
	}
	if svc.current().String() != svcBefore {
		t.Error("the services were not rolled back")
	}
}

// A router that cannot shape says so before anything is applied, rather
// than accepting the configuration and quietly not shaping.
func TestShapingPreflightRefusesBeforeAnythingIsApplied(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	sh := &fakeShaper{fakeNet: fakeNet{files: network.Files{}}, preflightErr: errors.New("tc is not installed")}
	e := New(st, fr, nil, slog.New(slog.DiscardHandler)).WithShaping(sh)

	_, err := e.Apply(context.Background(), shapedConfig("first", 200_000_000), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "tc is not installed") {
		t.Fatalf("err = %v", err)
	}
	if fr.count() != 0 {
		t.Error("the firewall was applied despite a preflight failure")
	}
	if len(sh.current()) != 0 {
		t.Error("the shaper was applied despite its own preflight failure")
	}

	// A configuration that shapes nothing is allowed through on the same
	// router: there is nothing for the missing command to do.
	if _, err := e.Apply(context.Background(), cfg("plain"), ApplyOptions{}); err != nil {
		t.Fatalf("an unshaped configuration was refused: %v", err)
	}
}

// A renewal goes to the backend only for an interface the running
// configuration gives a lease to; a static one has nothing to renew, and
// a name that is not configured is not a link to touch.
func TestRenewLeaseGatesOnTheConfiguration(t *testing.T) {
	t.Parallel()
	eng, _, fn := newEngineWithNet(t)
	ctx := context.Background()
	if err := eng.RenewLease(ctx, "eth0", false); !errors.Is(err, ErrUnknownInterface) {
		t.Errorf("before any apply: err = %v, want ErrUnknownInterface", err)
	}
	if _, err := eng.Apply(ctx, cfg("fw"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := eng.RenewLease(ctx, "eth1", false); !errors.Is(err, ErrNotDynamic) {
		t.Errorf("static LAN: err = %v, want ErrNotDynamic", err)
	}
	if err := eng.RenewLease(ctx, "nope", false); !errors.Is(err, ErrUnknownInterface) {
		t.Errorf("unknown: err = %v, want ErrUnknownInterface", err)
	}
	if err := eng.RenewLease(ctx, "eth0", false); err != nil {
		t.Fatal(err)
	}
	if err := eng.RenewLease(ctx, "eth0", true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"eth0", "eth0!"}; !reflect.DeepEqual(fn.renews, want) {
		t.Errorf("renews = %v, want %v", fn.renews, want)
	}

	// No network backend: nothing manages the interfaces, so say so.
	bare, _, _ := newEngine(t)
	if err := bare.RenewLease(ctx, "eth0", false); !errors.Is(err, ErrNoNetwork) {
		t.Errorf("no backend: err = %v, want ErrNoNetwork", err)
	}
}
