// Package notify tells an admin what went wrong on the router, by email or
// to a webhook, once the configuration says to (ADR-0024). Notices wait a
// few seconds so that what happens together arrives together, a target
// that fails is tried again for a while, and a flood is held back rather
// than passed on.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// Levels a notice has.
const (
	// LevelWarn is something that needs looking at.
	LevelWarn = "warn"
	// LevelInfo is worth knowing and needs nothing.
	LevelInfo = "info"
	// LevelOK is the end of something an earlier notice reported.
	LevelOK = "ok"
)

// Event is one notice.
type Event struct {
	// Kind is what the mute list names: a dashboard warning's kind, or
	// one of the kinds only a notice has.
	Kind   string    `json:"kind"`
	Level  string    `json:"level"`
	Title  string    `json:"title"`
	Detail string    `json:"detail,omitempty"`
	Time   time.Time `json:"time"`
}

// Kinds that only a notice has; the rest are the dashboard's warnings.
const (
	// KindApplyReverted is an apply whose window ran out.
	KindApplyReverted = "apply-reverted"
	// KindUpdate is a release of Ostiole waiting to be installed.
	KindUpdate = "update-available"
	// KindOSUpdates is package updates this router will not install on
	// its own, a failed install, or a reboot it is waiting for.
	KindOSUpdates = "os-updates"
	// KindFeed is an address list or blocklist that stopped refreshing.
	KindFeed = "feed-stale"
	// KindWAF is the daily count of what the web application firewall
	// blocked.
	KindWAF = "waf"
	// KindTest is the message the test button sends.
	KindTest = "test"
)

// Targets a message goes to.
const (
	TargetEmail   = "email"
	TargetWebhook = "webhook"
)

// Message is what goes to a target: the router's name and the notices
// that arrived together.
type Message struct {
	Router string
	Events []Event
	// Dropped is how many notices before these were lost to a full queue:
	// what a flood the rate limit holds back comes to.
	Dropped int
	// Location is the zone times are written in.
	Location *time.Location
}

// Sender delivers one message to one target.
type Sender interface {
	Send(ctx context.Context, target string, n model.Notifications, m Message) error
}

const (
	// gather is how long the first notice waits for the ones that come
	// with it.
	gather = 10 * time.Second
	// perHour is how many messages a router sends in an hour at most.
	// A flood beyond it is counted, not sent.
	perHour = 12
	// queued is how many notices wait at most; older ones go first.
	queued = 200
	// perMessage is how many notices one message carries.
	perMessage = 20
	// kept is how many sent messages the page lists.
	kept = 30
	// attemptTimeout bounds one attempt at one target.
	attemptTimeout = time.Minute
)

// TargetStatus is how one target has done.
type TargetStatus struct {
	LastSent  *time.Time `json:"lastSent,omitempty"`
	LastError string     `json:"lastError,omitempty"`
	LastTried *time.Time `json:"lastTried,omitempty"`
}

// Record is one message and what became of it.
type Record struct {
	Time   time.Time `json:"time"`
	Events []Event   `json:"events"`
	// Sent lists the targets that took it; Failed says why the others did
	// not, after every attempt.
	Sent    []string          `json:"sent"`
	Failed  map[string]string `json:"failed,omitempty"`
	Dropped int               `json:"dropped,omitempty"`
}

// Status is what the notifications page shows.
type Status struct {
	Targets map[string]TargetStatus `json:"targets"`
	Recent  []Record                `json:"recent"`
	// Waiting is how many notices are queued or being sent.
	Waiting int `json:"waiting"`
}

// Notifier queues notices and sends them where the configuration says.
// Nothing is queued, kept or sent while notifications are off.
type Notifier struct {
	// Config is the configuration in force; nil reads as off.
	Config func() *model.Config
	// Sender delivers a message; nil sends it for real.
	Sender Sender
	Log    *slog.Logger

	// Gather is how long the first notice waits for the ones that come
	// with it; zero is ten seconds.
	Gather time.Duration
	// Retries are the waits before each attempt after the first; nil is
	// 30 seconds, 2 minutes and 10 minutes.
	Retries []time.Duration

	mu      sync.Mutex
	failing map[string]failure
	queue   []Event
	dropped int
	sending int
	sent    []time.Time
	status  Status
	wake    chan struct{}
	once    sync.Once
}

func (n *Notifier) init() {
	n.once.Do(func() {
		n.wake = make(chan struct{}, 1)
		n.status.Targets = map[string]TargetStatus{}
		n.failing = map[string]failure{}
		if n.Gather == 0 {
			n.Gather = gather
		}
		if n.Retries == nil {
			n.Retries = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}
		}
	})
}

func (n *Notifier) log() *slog.Logger {
	if n.Log == nil {
		return slog.Default()
	}
	return n.Log
}

func (n *Notifier) sender() Sender {
	if n.Sender == nil {
		return Delivery{}
	}
	return n.Sender
}

// settings is the notifications block in force, and whether it sends.
func (n *Notifier) settings() (model.Notifications, string, *time.Location, bool) {
	var cfg *model.Config
	if n.Config != nil {
		cfg = n.Config()
	}
	if cfg == nil || !cfg.Notifications.Enabled {
		return model.Notifications{}, "", nil, false
	}
	return cfg.Notifications, routerName(cfg), cfg.System.Location(), true
}

func routerName(cfg *model.Config) string {
	if cfg != nil && cfg.System.Hostname != "" {
		return cfg.System.Hostname
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "router"
}

// Notify queues a notice, unless notifications are off or its kind is
// muted. It never blocks.
func (n *Notifier) Notify(e Event) {
	n.init()
	settings, _, _, on := n.settings()
	if !on || settings.Muted(e.Kind) {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Level == "" {
		e.Level = LevelWarn
	}
	n.mu.Lock()
	if len(n.queue) == queued {
		n.queue = n.queue[1:]
		n.dropped++
	}
	n.queue = append(n.queue, e)
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Run sends what is queued until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	n.init()
	var later <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.wake:
		case <-later:
		}
		later = nil
		if !sleep(ctx, n.Gather) {
			return
		}
		for {
			more, wait := n.flush(ctx)
			if wait > 0 {
				later = time.After(wait)
			}
			if !more {
				break
			}
		}
	}
}

// flush sends one message from the queue. It reports whether more wait
// that can go now, or how long the rate limit holds them.
func (n *Notifier) flush(ctx context.Context) (more bool, wait time.Duration) {
	settings, router, loc, on := n.settings()
	n.mu.Lock()
	if !on {
		// Switched off while they waited: nothing of them is kept.
		n.queue, n.dropped = nil, 0
		n.mu.Unlock()
		return false, 0
	}
	now := time.Now()
	n.sent = slices.DeleteFunc(n.sent, func(t time.Time) bool { return now.Sub(t) >= time.Hour })
	if len(n.queue) == 0 {
		n.mu.Unlock()
		return false, 0
	}
	if len(n.sent) >= perHour {
		wait = time.Hour - now.Sub(n.sent[0])
		n.mu.Unlock()
		return false, wait
	}
	take := min(len(n.queue), perMessage)
	events := slices.Clone(n.queue[:take])
	n.queue = n.queue[take:]
	dropped := n.dropped
	n.dropped = 0
	n.sent = append(n.sent, now)
	n.sending += len(events)
	more = len(n.queue) > 0
	n.mu.Unlock()

	m := Message{Router: router, Events: events, Dropped: dropped, Location: loc}
	rec := n.deliver(ctx, settings, m)
	n.mu.Lock()
	n.sending -= len(events)
	n.status.Recent = append([]Record{rec}, n.status.Recent...)
	if len(n.status.Recent) > kept {
		n.status.Recent = n.status.Recent[:kept]
	}
	n.mu.Unlock()
	return more, 0
}

// targets are the ones the settings send to.
func targets(settings model.Notifications) []string {
	var out []string
	if settings.Email.Enabled {
		out = append(out, TargetEmail)
	}
	if settings.Webhook.Enabled {
		out = append(out, TargetWebhook)
	}
	return out
}

// deliver sends m to every target at once, each trying again on its own
// until its attempts run out.
func (n *Notifier) deliver(ctx context.Context, settings model.Notifications, m Message) Record {
	rec := Record{Time: time.Now(), Events: m.Events, Sent: []string{}, Dropped: m.Dropped}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, target := range targets(settings) {
		wg.Go(func() {
			used, err := n.attempts(ctx, target, settings, m)
			n.mu.Lock()
			if err == nil || errors.Is(err, errStopped) {
				delete(n.failing, target)
			} else {
				n.failing[target] = failure{settings: targetKey(used, target), err: err.Error()}
			}
			n.mu.Unlock()
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				rec.Sent = append(rec.Sent, target)
				return
			}
			if rec.Failed == nil {
				rec.Failed = map[string]string{}
			}
			rec.Failed[target] = err.Error()
			if !errors.Is(err, errStopped) {
				n.log().Warn("could not send a notification", "target", target, "err", err)
			}
		})
	}
	wg.Wait()
	slices.Sort(rec.Sent)
	return rec
}

// errStopped is a message given up on because its target was switched
// off while it waited to try again.
var errStopped = errors.New("not sent: switched off before it got through")

// attempts tries a target until it takes the message or the attempts run
// out. Each attempt goes where the configuration says at the time, so a
// target put right is used and one switched off is not tried again. It
// returns the settings the last attempt used.
func (n *Notifier) attempts(ctx context.Context, target string, settings model.Notifications, m Message) (model.Notifications, error) {
	for i := 0; ; i++ {
		err := n.attempt(ctx, target, settings, m)
		if err == nil || i == len(n.Retries) || !sleep(ctx, n.Retries[i]) {
			return settings, err
		}
		now, _, _, on := n.settings()
		if !on || !slices.Contains(targets(now), target) {
			return settings, errStopped
		}
		settings = now
	}
}

func (n *Notifier) attempt(ctx context.Context, target string, settings model.Notifications, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	err := n.sender().Send(ctx, target, settings, m)
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.status.Targets[target]
	st.LastTried = &now
	if err == nil {
		st.LastSent, st.LastError = &now, ""
	} else {
		st.LastError = err.Error()
	}
	n.status.Targets[target] = st
	return err
}

// failure is a target whose last message did not get through, and the
// settings it was sent with.
type failure struct {
	settings string
	err      string
}

// targetKey identifies what a target was set to.
func targetKey(n model.Notifications, target string) string {
	var part any = n.Webhook
	if target == TargetEmail {
		part = n.Email
	}
	raw, _ := json.Marshal(part)
	return string(raw)
}

// Failing names the targets settings switch on whose last message did
// not get through, with why, while they are still set the way it was
// sent. Changing a target's settings clears its failure.
func (n *Notifier) Failing(settings model.Notifications) map[string]string {
	n.init()
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string]string{}
	for _, target := range targets(settings) {
		if f, ok := n.failing[target]; ok && f.settings == targetKey(settings, target) {
			out[target] = f.err
		}
	}
	return out
}

// Forget drops everything: what waits, what went out and how each target
// did. Turning notifications off does this, so a router that sends
// nothing keeps nothing either.
func (n *Notifier) Forget() {
	n.init()
	n.mu.Lock()
	defer n.mu.Unlock()
	n.queue, n.dropped, n.sent = nil, 0, nil
	n.status.Recent = nil
	n.status.Targets = map[string]TargetStatus{}
	n.failing = map[string]failure{}
}

// Status says how each target has done and what went out lately.
func (n *Notifier) Status() Status {
	n.init()
	n.mu.Lock()
	defer n.mu.Unlock()
	out := Status{Targets: map[string]TargetStatus{}, Recent: slices.Clone(n.status.Recent), Waiting: len(n.queue) + n.sending}
	for k, v := range n.status.Targets {
		out.Targets[k] = v
	}
	if out.Recent == nil {
		out.Recent = []Record{}
	}
	return out
}

// ErrNoTarget says a test had nowhere to go.
var ErrNoTarget = errors.New("turn on email or a webhook first")

// Test sends one message to each target settings has switched on, once
// each and at once, whether notifications are on or not: the point is to
// find out before they are.
func (n *Notifier) Test(ctx context.Context, settings model.Notifications, cfg *model.Config) (map[string]error, error) {
	list := targets(settings)
	if len(list) == 0 {
		return nil, ErrNoTarget
	}
	var loc *time.Location
	if cfg != nil {
		loc = cfg.System.Location()
	}
	m := Message{Router: routerName(cfg), Location: loc, Events: []Event{{
		Kind: KindTest, Level: LevelInfo, Time: time.Now(),
		Title: "Notifications from " + routerName(cfg) + " arrive here",
	}}}
	out := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, target := range list {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
			defer cancel()
			err := n.sender().Send(ctx, target, settings, m)
			mu.Lock()
			out[target] = err
			mu.Unlock()
		})
	}
	wg.Wait()
	return out, nil
}

// sleep waits d, and reports false when ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
