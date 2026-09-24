package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/notify"
	"github.com/rforced/ostiole/internal/services"
)

func (a *api) registerNotify(mux *router) {
	mux.HandleFunc("GET /api/v1/notifications", a.readNoEngine(a.notificationStatus))
	mux.HandleFunc("POST /api/v1/notifications/test", a.admin(a.needEngine(a.testNotifications)))
}

// notificationStatus says how each target has done and what went out.
func (a *api) notificationStatus(w http.ResponseWriter, _ *http.Request) error {
	st := notify.Status{Targets: map[string]notify.TargetStatus{}, Recent: []notify.Record{}}
	if a.notifier != nil {
		st = a.notifier.Status()
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

// kindNotifyFailed is the dashboard's warning that notices are failing.
const kindNotifyFailed = "notify-failed"

// targetName is how a target reads in a sentence.
var targetName = map[string]string{notify.TargetEmail: "Mail", notify.TargetWebhook: "Webhook"}

// testResult is what one target made of the test.
type testResult struct {
	Target string `json:"target"`
	Error  string `json:"error,omitempty"`
}

// testNotifications sends a test to the targets the settings in the body
// switch on: the draft, so that they can be tried before they are applied.
func (a *api) testNotifications(w http.ResponseWriter, r *http.Request) error {
	if a.notifier == nil {
		return &unavailable{errors.New("nothing sends notifications on this router")}
	}
	var settings model.Notifications
	if err := decodeJSON(r, &settings); err != nil {
		return err
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	// The saved configuration names the router and the zone.
	saved, _ := a.engine.Store().Load()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	results, err := a.notifier.Test(ctx, settings, saved)
	if err != nil {
		return &badRequest{err}
	}
	out := make([]testResult, 0, len(results))
	for target, err := range results {
		res := testResult{Target: target}
		if err != nil {
			res.Error = err.Error()
		}
		out = append(out, res)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	writeJSON(w, http.StatusOK, out)
	return nil
}

// watchEvery is how often the warnings are looked at for a notice.
const watchEvery = time.Minute

// watchConditions looks at what the router is in once a minute and
// notifies what began and what ended. It reads the configuration the
// notifier sends by, and while that has notifications off, nothing about
// them is kept.
func (a *api) watchConditions(ctx context.Context) {
	every := a.watchEvery
	if every <= 0 {
		every = watchEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		switch cfg := a.engine.Effective(); {
		case cfg == nil || !cfg.Notifications.Enabled:
			a.tracker.Reset()
			a.notifier.Forget()
		default:
			if now, ok := a.conditions(ctx, cfg); ok {
				for _, e := range a.tracker.Observe(now, time.Now()) {
					a.notifier.Notify(e)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// conditions is what the router is in that a notice could be about: the
// dashboard's warnings, and what only a notice says. ok is false when a
// source could not say, which is no reason to call everything over.
func (a *api) conditions(ctx context.Context, cfg *model.Config) ([]notify.Condition, bool) {
	st, err := a.engine.Status(ctx)
	if err != nil {
		return nil, false
	}
	links, err := network.Discover()
	if err != nil {
		// With no links every interface reads as missing.
		return nil, false
	}
	var out []notify.Condition
	for _, w := range a.warnings(ctx, cfg, st, a.serviceStates(ctx, cfg), summarizeLinks(cfg, links)) {
		// A notice about notices failing would go the way they fail.
		if w.Level != "warn" || w.Kind == kindNotifyFailed {
			continue
		}
		// The undo is over by the time it is reported; its end says nothing.
		out = append(out, notify.Condition{Kind: w.Kind, Key: w.Key, Title: w.Title, Detail: w.Detail, Once: w.Kind == "apply-undone"})
	}
	if a.updater != nil {
		if s := a.updater.Cached(); s.Available && s.Latest != "" {
			detail := "This router runs " + s.Current + "."
			if s.Security {
				detail += " It fixes a security problem."
			}
			out = append(out, notify.Condition{
				Kind: notify.KindUpdate, Level: notify.LevelInfo, Once: true,
				Title: "Ostiole " + s.Latest + " is available", Detail: detail,
			})
		}
	}
	out = append(out, a.packageConditions(cfg)...)
	out = append(out, a.feedConditions(cfg)...)
	return out, true
}

// packageConditions are the operating system's: updates waiting that this
// router will not install on its own, an install that failed, a reboot.
func (a *api) packageConditions(cfg *model.Config) []notify.Condition {
	if a.packages == nil || !a.packages.Available() {
		return nil
	}
	st := a.packages.Status(cfg.Updates.SystemMode() == model.UpdateSecurity)
	var out []notify.Condition
	if st.RebootRequired {
		out = append(out, notify.Condition{Kind: notify.KindOSUpdates, Title: "This router wants a reboot", Detail: st.RebootReason})
	}
	if st.LastError != "" {
		out = append(out, notify.Condition{Kind: notify.KindOSUpdates, Title: "Package updates did not install", Detail: st.LastError})
	}
	mode := cfg.Updates.SystemMode()
	if n := st.Pending.Security; n > 0 && (mode == model.UpdateManual || mode == model.UpdateDisabled) {
		out = append(out, notify.Condition{
			Kind: notify.KindOSUpdates, Level: notify.LevelInfo, Title: "Security updates are waiting",
			Detail: fmt.Sprintf("%d of %d waiting packages are security fixes. This router installs them by hand.", n, len(st.Pending.Packages)),
		})
	}
	return out
}

// feedConditions are the lists that stopped refreshing: an address list
// the firewall matches on, or a blocklist the resolver refuses from.
func (a *api) feedConditions(cfg *model.Config) []notify.Condition {
	var out []notify.Condition
	if a.feedCache != nil {
		for _, f := range a.feedCache.Statuses(cfg) {
			if !f.Stale || f.LastError == "" {
				continue
			}
			name := "Address list " + f.Alias
			if f.Alias == feeds.BogonAlias {
				name = "The bogon list"
			}
			out = append(out, notify.Condition{Kind: notify.KindFeed, Title: name + " is not refreshing", Detail: f.LastError})
		}
	}
	if a.blockCache != nil {
		for _, l := range a.blockCache.Statuses(cfg) {
			if !l.Enabled || !l.Stale || l.LastError == "" {
				continue
			}
			out = append(out, notify.Condition{Kind: notify.KindFeed, Title: "Blocklist " + l.Name + " is not refreshing", Detail: l.LastError})
		}
	}
	return out
}

// The WAF's count is read every ten minutes, so that a busy stretch fits
// the lines one journal read returns, and sent once a day at nine in the
// router's zone.
const (
	wafEvery = 10 * time.Minute
	wafHour  = 9
)

// watchWAF counts what the web application firewall blocked and sends the
// count once a day. It is counted per site only: which clients and which
// addresses stay on the router.
func (a *api) watchWAF(ctx context.Context) {
	tick := time.NewTicker(wafEvery)
	defer tick.Stop()
	var c wafCount
	sentOn, failed := "", ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		cfg := a.engine.Effective()
		if cfg == nil || !cfg.Notifications.Enabled || !wafOn(cfg) {
			c = wafCount{}
			continue
		}
		if c.since.IsZero() {
			c = wafCount{since: now, from: now, sites: map[string]int{}}
			continue
		}
		if err := a.countBlocked(ctx, &c, now); err != nil {
			// Once, not every ten minutes it goes on.
			if err.Error() != failed {
				slog.Warn("could not read what the web application firewall blocked", "err", err)
			}
			failed = err.Error()
			continue
		}
		failed = ""
		local := now.In(cfg.System.Location())
		day := local.Format(time.DateOnly)
		if local.Hour() < wafHour || sentOn == day {
			continue
		}
		sentOn = day
		if e, ok := c.notice(cfg.System.Location()); ok {
			a.notifier.Notify(e)
		}
		c = wafCount{since: now, from: now, sites: map[string]int{}}
	}
}

// wafCount is what the WAF blocked from one moment on, by site.
type wafCount struct {
	// from is when the count began, since how far it has been read.
	from, since time.Time
	sites       map[string]int
	// capped says a read came back full, so the count is a floor.
	capped bool
}

// wafOn reports whether any site the proxy serves is inspected.
func wafOn(cfg *model.Config) bool {
	if !cfg.ProxyEnabled() {
		return false
	}
	for _, s := range cfg.Services.Proxy.Sites {
		if s.Enabled && s.WAF != "" {
			return true
		}
	}
	return false
}

// countBlocked adds to c what the proxy blocked from c.since to until.
func (a *api) countBlocked(ctx context.Context, c *wafCount, until time.Time) error {
	read := a.journal
	if read == nil {
		read = diag.Journal
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// Seconds since 1970, which no zone can misread.
	entries, err := read(ctx, diag.JournalOptions{
		Unit: services.ProxyUnit, Lines: diag.MaxJournalLines, Since: "@" + strconv.FormatInt(c.since.Unix(), 10),
	})
	if err != nil {
		return err
	}
	if len(entries) >= diag.MaxJournalLines {
		c.capped = true
	}
	for _, e := range entries {
		ev, ok := services.ParseProxyEvent(e.Message, e.Time)
		if !ok || ev.Verdict != services.VerdictBlocked || ev.Time.Before(c.since) || !ev.Time.Before(until) {
			continue
		}
		site := ev.Site
		if site == "" {
			site = "no site"
		}
		c.sites[site]++
	}
	c.since = until
	return nil
}

// notice is the count as a notice, false when nothing was blocked.
func (c wafCount) notice(loc *time.Location) (notify.Event, bool) {
	total := 0
	sites := make([]string, 0, len(c.sites))
	for site, n := range c.sites {
		total += n
		sites = append(sites, site)
	}
	if total == 0 {
		return notify.Event{}, false
	}
	sort.Slice(sites, func(i, j int) bool {
		if c.sites[sites[i]] != c.sites[sites[j]] {
			return c.sites[sites[i]] > c.sites[sites[j]]
		}
		return sites[i] < sites[j]
	})
	parts := make([]string, len(sites))
	for i, site := range sites {
		parts[i] = fmt.Sprintf("%d to %s", c.sites[site], site)
	}
	count := strconv.Itoa(total) + " requests"
	if total == 1 {
		count = "a request"
	}
	if c.capped {
		count = "at least " + count
	}
	return notify.Event{
		Kind: notify.KindWAF, Level: notify.LevelInfo, Title: "The web application firewall blocked " + count,
		Detail: "Since " + c.from.In(loc).Format("2006-01-02 15:04") + ": " + strings.Join(parts, ", ") +
			". The requests are under Services, Proxy.",
	}, true
}
