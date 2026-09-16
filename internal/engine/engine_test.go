package engine

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
)

// fakeNet is an in-memory backend; render picks network or services output.
type fakeNet struct {
	mu       sync.Mutex
	files    network.Files
	applies  []network.Files
	applyErr error
	services bool
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
	table    bool
}

func (f *fakeRunner) Check(_ context.Context, _ string) error { return f.checkErr }

func (f *fakeRunner) Apply(_ context.Context, rs string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
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
	// First apply on a fresh store: revert removes the table.
	if !strings.Contains(fr.last(), "delete table inet ostiole") || strings.Contains(fr.last(), "{") {
		t.Errorf("revert ruleset = %q, want empty ruleset", fr.last())
	}
	if st.Exists() {
		t.Error("store must not be written for an expired apply")
	}
	status, _ := e.Status(context.Background())
	if status.Pending != nil || status.TableLoaded {
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

func TestLoad(t *testing.T) {
	t.Parallel()
	e, fr, _ := newEngine(t)
	if err := e.Load(context.Background()); !errors.Is(err, ErrNoRuleset) {
		t.Fatalf("Load on empty store: %v", err)
	}
	res, _ := e.Apply(context.Background(), cfg("boot"), ApplyOptions{})
	if err := e.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fr.count() != 2 || fr.last() != res.Ruleset {
		t.Errorf("Load applied wrong ruleset")
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
	fn.applyErr = errors.New("networkctl exploded")
	_, err := e.Apply(context.Background(), cfg("second"), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "networkctl exploded") {
		t.Fatalf("err = %v", err)
	}
	if fr.last() != first {
		t.Error("firewall not reverted after network failure")
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
