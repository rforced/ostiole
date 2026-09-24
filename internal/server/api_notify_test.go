package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/notify"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
)

// outbox is a sender that keeps what it is given, and refuses the
// webhook when told to.
type outbox struct {
	mu      sync.Mutex
	sent    []notify.Message
	refused bool
}

func (o *outbox) Send(_ context.Context, target string, _ model.Notifications, m notify.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.refused && target == notify.TargetWebhook {
		return errors.New("the hook answered 404 Not Found")
	}
	o.sent = append(o.sent, m)
	return nil
}

// notifying is a config that sends notices by mail.
func notifying() *model.Config {
	cfg := starter()
	cfg.Notifications = model.Notifications{Enabled: true, Email: model.NotifyEmail{
		Enabled: true, Server: "smtp.example.net", Password: "hunter2", From: "fw@example.net", To: []string{"me@example.net"},
	}}
	return cfg
}

// The test goes out at once to the targets the draft switches on, and only
// an admin may send one.
func TestNotificationTest(t *testing.T) {
	t.Parallel()
	box := &outbox{refused: true}
	var as *auth.Service
	srv := newTestServerWith(t, func(d *Deps) {
		d.Notify = &notify.Notifier{Sender: box}
		as = d.Auth
	})
	settings := notifying().Notifications
	settings.Webhook = model.NotifyWebhook{Enabled: true, URL: "https://hooks.example.net/x"}
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/notifications/test", settings)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("test: %d %s", resp.StatusCode, raw)
	}
	var results []testResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Target != notify.TargetEmail || results[0].Error != "" ||
		!strings.Contains(results[1].Error, "404") {
		t.Errorf("results = %+v", results)
	}
	if len(box.sent) != 1 || box.sent[0].Events[0].Kind != notify.KindTest {
		t.Errorf("sent = %+v", box.sent)
	}

	settings.Webhook.URL = "http://hooks.example.net/x"
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/notifications/test", settings); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(string(raw), "notifications.webhook.url") {
		t.Errorf("an http webhook: %d %s", resp.StatusCode, raw)
	}

	if err := as.CreateUser("hand", testPassword, auth.RoleOperator); err != nil {
		t.Fatal(err)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "hand", Password: testPassword}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/notifications/test", settings); resp.StatusCode != http.StatusForbidden {
		t.Errorf("an operator's test: %d", resp.StatusCode)
	}
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/notifications", nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"recent":[]`) {
		t.Errorf("status: %d %s", resp.StatusCode, raw)
	}
}

// apiFor builds the API over a fresh store, the way Run does, for the
// watchers.
func apiFor(t *testing.T, box *outbox) (*api, *engine.Engine) {
	t.Helper()
	eng := engine.New(store.New(t.TempDir()), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	_, a := build(Deps{Engine: eng, Notify: &notify.Notifier{Sender: box, Config: eng.Effective}})
	return a, eng
}

// The fallback ruleset and an apply undone at the start are the dashboard's
// warnings, so they are conditions like any other; the undo has no end to
// report.
func TestTheWatcherSeesTheDashboardsWarnings(t *testing.T) {
	t.Parallel()
	a, eng := apiFor(t, &outbox{})
	cfg := notifying()
	if _, err := eng.Apply(context.Background(), cfg, engine.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(engine.FallbackStatus{Since: time.Now(), Reason: "nft refused it"})
	if err := eng.Store().WriteState(store.FallbackFile, raw); err != nil {
		t.Fatal(err)
	}
	now, ok := a.conditions(context.Background(), cfg)
	if !ok {
		t.Fatal("no conditions")
	}
	var fallback *notify.Condition
	for i, c := range now {
		if c.Kind == "fallback-ruleset" {
			fallback = &now[i]
		}
		if c.Kind == "dhcp-interface-off" {
			t.Errorf("an info warning became a condition: %+v", c)
		}
	}
	if fallback == nil || !strings.Contains(fallback.Detail, "nft refused it") || fallback.Once {
		t.Errorf("conditions = %+v", now)
	}
}

// Off, the watcher keeps nothing, not even what an earlier run left; on,
// it notifies what it finds on two looks running and remembers it.
func TestTheWatcherKeepsNothingWhileOff(t *testing.T) {
	t.Parallel()
	a, eng := apiFor(t, &outbox{})
	a.watchEvery = 5 * time.Millisecond
	off := notifying()
	off.Notifications.Enabled = false
	if _, err := eng.Apply(context.Background(), off, engine.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := eng.Store().WriteState(store.NotifyFile, []byte(`[{"kind":"gateway-down","title":"Gateway wan is down"}]`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.watchConditions(ctx); close(done) }()
	waitUntil(t, func() bool {
		_, err := eng.Store().ReadState(store.NotifyFile)
		return errors.Is(err, store.ErrNotFound)
	})
	cancel()
	<-done

	if _, err := eng.Apply(context.Background(), notifying(), engine.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	// The apply cleared the fallback; put it back as if boot had loaded it.
	raw, _ := json.Marshal(engine.FallbackStatus{Since: time.Now(), Reason: "nft refused it"})
	if err := eng.Store().WriteState(store.FallbackFile, raw); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { a.watchConditions(ctx); close(done) }()
	waitUntil(t, func() bool { return a.notifier.Status().Waiting > 0 })
	cancel()
	<-done
	if raw, err := eng.Store().ReadState(store.NotifyFile); err != nil || !strings.Contains(string(raw), "fallback-ruleset") {
		t.Errorf("state = %s, %v", raw, err)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The day's count names sites and numbers, busiest first, and nothing a
// client sent; a read that came back full makes it a floor.
func TestWAFNotice(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	if _, ok := (wafCount{from: from, sites: map[string]int{}}).notice(time.UTC); ok {
		t.Error("a notice for nothing blocked")
	}
	c := wafCount{from: from, sites: map[string]int{"git.example.net": 7, "cloud.example.net": 30}}
	e, ok := c.notice(time.UTC)
	if !ok || e.Title != "The web application firewall blocked 37 requests" ||
		!strings.HasPrefix(e.Detail, "Since 2026-09-23 09:00: 30 to cloud.example.net, 7 to git.example.net.") {
		t.Errorf("notice = %+v", e)
	}
	c.capped = true
	if e, _ := c.notice(time.UTC); !strings.Contains(e.Title, "at least 37") {
		t.Errorf("capped title = %q", e.Title)
	}
}

// The count reads the proxy's journal from where it left off, in the one
// form of time journalctl cannot read in another zone, and counts only
// what was blocked.
func TestWAFCountReadsFromWhereItLeftOff(t *testing.T) {
	t.Parallel()
	var asked diag.JournalOptions
	a := &api{journal: func(_ context.Context, o diag.JournalOptions) ([]diag.JournalEntry, error) {
		asked = o
		return []diag.JournalEntry{
			{Time: time.Now(), Message: blockedLine},
			{Time: time.Now(), Message: wouldBlockLine},
			{Time: time.Now(), Message: "starting up"},
		}, nil
	}}
	since := time.Unix(1789999000, 0)
	until := time.Unix(1790000000, 0)
	c := wafCount{since: since, from: since, sites: map[string]int{}}
	if err := a.countBlocked(context.Background(), &c, until); err != nil {
		t.Fatal(err)
	}
	if asked.Since != "@1789999000" || asked.Unit != services.ProxyUnit {
		t.Errorf("asked %+v", asked)
	}
	if c.sites["shop"] != 1 || len(c.sites) != 1 || !c.since.Equal(until) {
		t.Errorf("count = %+v", c)
	}
}

// A notice that did not arrive is said where the admin looks.
func TestTheDashboardSaysANoticeFailed(t *testing.T) {
	t.Parallel()
	box := &outbox{refused: true}
	var eng *engine.Engine
	n := &notify.Notifier{Sender: box, Gather: time.Millisecond, Retries: []time.Duration{}}
	srv := newTestServerWith(t, func(d *Deps) {
		eng = d.Engine
		n.Config = eng.Effective
		d.Notify = n
	})
	cfg := notifying()
	cfg.Notifications.Webhook = model.NotifyWebhook{Enabled: true, URL: "https://hooks.example.net/x"}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: cfg}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	n.Notify(notify.Event{Kind: "gateway-down", Title: "Gateway wan is down"})
	waitUntil(t, func() bool { return len(n.Status().Recent) == 1 })
	w := warning(getOverview(t, srv), "notify-failed")
	if w == nil || !strings.HasPrefix(w.Detail, "Webhook: the hook answered 404") {
		t.Errorf("warning = %+v", w)
	}
}
