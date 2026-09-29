package notify

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// fakeSender records what each target was sent, and fails as told.
type fakeSender struct {
	mu   sync.Mutex
	sent map[string][]Message
	// fail is how many attempts at a target fail before one works.
	fail map[string]int
	// sending runs as each attempt starts.
	sending func(target string)
}

func (f *fakeSender) Send(_ context.Context, target string, _ model.Notifications, m Message) error {
	if f.sending != nil {
		f.sending(target)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail[target] > 0 {
		f.fail[target]--
		return errors.New(target + " is down")
	}
	if f.sent == nil {
		f.sent = map[string][]Message{}
	}
	f.sent[target] = append(f.sent[target], m)
	return nil
}

func (f *fakeSender) messages(target string) []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent[target]...)
}

// config sends to both targets, muting what it is given.
func config(mute ...string) *model.Config {
	cfg := &model.Config{}
	cfg.System.Hostname = "fw"
	cfg.Notifications = model.Notifications{
		Enabled: true,
		Email:   model.NotifyEmail{Enabled: true},
		Webhook: model.NotifyWebhook{Enabled: true},
		Mute:    mute,
	}
	return cfg
}

// notifier runs a notifier over cfg that gathers for a moment and tries
// again at once.
func notifier(t *testing.T, cfg *model.Config, send *fakeSender) (*Notifier, func()) {
	t.Helper()
	var mu sync.Mutex
	n := &Notifier{
		Config: func() *model.Config {
			mu.Lock()
			defer mu.Unlock()
			return cfg
		},
		Log:     slog.New(slog.DiscardHandler),
		Sender:  send,
		Gather:  20 * time.Millisecond,
		Retries: []time.Duration{time.Millisecond, time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		n.Run(ctx)
		close(done)
	}()
	return n, func() {
		cancel()
		<-done
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// What happens together arrives together, at every target.
func TestNoticesThatComeTogetherGoInOneMessage(t *testing.T) {
	t.Parallel()
	send := &fakeSender{}
	n, stop := notifier(t, config(), send)
	defer stop()
	n.Notify(Event{Kind: "gateway-down", Title: "Gateway wan is down"})
	n.Notify(Event{Kind: "service-down", Title: "DNS is not running"})
	waitFor(t, func() bool { return len(send.messages(TargetWebhook)) == 1 && len(send.messages(TargetEmail)) == 1 })
	m := send.messages(TargetEmail)[0]
	if len(m.Events) != 2 || m.Router != "fw" || m.Events[0].Level != LevelWarn || m.Events[0].Time.IsZero() {
		t.Errorf("message = %+v", m)
	}
	waitFor(t, func() bool { return len(n.Status().Recent) == 1 })
	st := n.Status()
	if rec := st.Recent[0]; len(rec.Sent) != 2 || rec.Failed != nil {
		t.Errorf("record = %+v", rec)
	}
	if st.Targets[TargetEmail].LastSent == nil || st.Waiting != 0 {
		t.Errorf("status = %+v", st)
	}
}

// Nothing is queued, kept or sent while notifications are off, and a
// muted kind is not either.
func TestNothingGoesOutWhenOffOrMuted(t *testing.T) {
	t.Parallel()
	off := config()
	off.Notifications.Enabled = false
	send := &fakeSender{}
	n, stop := notifier(t, off, send)
	n.Notify(Event{Kind: "gateway-down", Title: "Gateway wan is down"})
	time.Sleep(60 * time.Millisecond)
	stop()
	if st := n.Status(); len(send.messages(TargetEmail)) != 0 || st.Waiting != 0 {
		t.Errorf("sent %v while off, %d waiting", send.messages(TargetEmail), st.Waiting)
	}

	muted := &fakeSender{}
	n, stop = notifier(t, config("waf"), muted)
	defer stop()
	n.Notify(Event{Kind: "waf", Title: "blocked"})
	n.Notify(Event{Kind: "gateway-down", Title: "Gateway wan is down"})
	waitFor(t, func() bool { return len(muted.messages(TargetWebhook)) == 1 })
	if m := muted.messages(TargetWebhook)[0]; len(m.Events) != 1 || m.Events[0].Kind != "gateway-down" {
		t.Errorf("message = %+v", m)
	}
}

// A target that is down is tried again, and one that is up is not held
// up by it.
func TestATargetThatFailsIsTriedAgain(t *testing.T) {
	t.Parallel()
	send := &fakeSender{fail: map[string]int{TargetEmail: 2}}
	n, stop := notifier(t, config(), send)
	defer stop()
	n.Notify(Event{Kind: "drive-failing", Title: "Drive sda reports it is failing"})
	waitFor(t, func() bool { return len(send.messages(TargetEmail)) == 1 })
	waitFor(t, func() bool { return len(n.Status().Recent) == 1 })
	st := n.Status()
	if st.Targets[TargetEmail].LastError != "" || len(st.Recent[0].Sent) != 2 {
		t.Errorf("status = %+v", st)
	}

	give := &fakeSender{fail: map[string]int{TargetEmail: 10}}
	n, stop2 := notifier(t, config(), give)
	defer stop2()
	n.Notify(Event{Kind: "drive-failing", Title: "Drive sda reports it is failing"})
	waitFor(t, func() bool { return len(n.Status().Recent) == 1 })
	rec := n.Status().Recent[0]
	if rec.Failed[TargetEmail] != "email is down" || len(rec.Sent) != 1 || rec.Sent[0] != TargetWebhook {
		t.Errorf("record = %+v", rec)
	}
}

// A flood is held to a dozen messages an hour; beyond that the notices
// wait, and the oldest go once the queue is full and are counted.
func TestAFloodIsHeldBack(t *testing.T) {
	t.Parallel()
	n := &Notifier{Config: func() *model.Config { return config() }, Sender: &fakeSender{}}
	n.init()
	now := time.Now()
	for range perHour {
		n.sent = append(n.sent, now.Add(-time.Minute))
	}
	for range queued + 5 {
		n.Notify(Event{Kind: "gateway-down", Title: "Gateway wan is down"})
	}
	more, wait := n.flush(context.Background())
	if more || wait < 58*time.Minute || wait > time.Hour {
		t.Errorf("flush while limited = %v, %v", more, wait)
	}
	if st := n.Status(); st.Waiting != queued {
		t.Errorf("waiting = %d, want %d", st.Waiting, queued)
	}
	n.sent = nil
	more, wait = n.flush(context.Background())
	if !more || wait != 0 {
		t.Errorf("flush once the hour allows = %v, %v", more, wait)
	}
	if rec := n.Status().Recent[0]; len(rec.Events) != perMessage || rec.Dropped != 5 {
		t.Errorf("first message after the flood: %d notices, %d dropped", len(rec.Events), rec.Dropped)
	}
}

// Turned off while notices wait, the router keeps none of them.
func TestSwitchingOffDropsWhatWaits(t *testing.T) {
	t.Parallel()
	cfg := config()
	var mu sync.Mutex
	n := &Notifier{Config: func() *model.Config { mu.Lock(); defer mu.Unlock(); return cfg }, Sender: &fakeSender{}}
	n.Notify(Event{Kind: "gateway-down", Title: "Gateway wan is down"})
	mu.Lock()
	cfg = config()
	cfg.Notifications.Enabled = false
	mu.Unlock()
	if more, _ := n.flush(context.Background()); more || n.Status().Waiting != 0 {
		t.Error("notices kept after notifications went off")
	}
}

// The test goes to the targets the form has on, whether notifications
// are on yet or not.
func TestTestSendsToTheTargetsOn(t *testing.T) {
	t.Parallel()
	send := &fakeSender{fail: map[string]int{TargetWebhook: 1}}
	n := &Notifier{Sender: send}
	settings := config().Notifications
	settings.Enabled = false
	res, err := n.Test(context.Background(), settings, config())
	if err != nil {
		t.Fatal(err)
	}
	if res[TargetEmail] != nil || res[TargetWebhook] == nil {
		t.Errorf("results = %v", res)
	}
	if m := send.messages(TargetEmail); len(m) != 1 || m[0].Events[0].Kind != KindTest {
		t.Errorf("sent = %+v", m)
	}
	if _, err := n.Test(context.Background(), model.Notifications{}, nil); !errors.Is(err, ErrNoTarget) {
		t.Errorf("test with nowhere to send = %v", err)
	}
}

// A failure is a target's until its settings change or a message gets
// through, and a message is not tried again once its target is off.
func TestAFailureFollowsTheTargetsSettings(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	cfg := config()
	current := func() *model.Config { mu.Lock(); defer mu.Unlock(); return cfg }
	send := &fakeSender{fail: map[string]int{TargetWebhook: 100}}
	n := &Notifier{Config: current, Sender: send, Log: slog.New(slog.DiscardHandler), Retries: []time.Duration{}}
	n.Notify(Event{Kind: "gateway-down", Title: "Gateway wan is down"})
	n.flush(context.Background())
	settings := current().Notifications
	if f := n.Failing(settings); f[TargetWebhook] != "webhook is down" || f[TargetEmail] != "" {
		t.Errorf("failing = %v", f)
	}
	fixed := settings
	fixed.Webhook.URL = "https://hooks.example.net/right"
	if f := n.Failing(fixed); len(f) != 0 {
		t.Errorf("failing after the URL changed = %v", f)
	}

	// Switched off between attempts: given up on, and not counted as a
	// failure of the target.
	n.Retries = []time.Duration{time.Millisecond}
	n.Notify(Event{Kind: "gateway-down", Title: "Gateway wan is down"})
	send.sending = func(target string) {
		if target != TargetWebhook {
			return
		}
		mu.Lock()
		cfg = config()
		cfg.Notifications.Webhook.Enabled = false
		mu.Unlock()
	}
	tries := func() int { send.mu.Lock(); defer send.mu.Unlock(); return send.fail[TargetWebhook] }
	before := tries()
	n.flush(context.Background())
	if used := before - tries(); used != 1 {
		t.Errorf("attempts after switching off = %d, want the first only", used)
	}
	if f := n.Failing(settings); len(f) != 0 {
		t.Errorf("failing after switching off = %v", f)
	}
	if rec := n.Status().Recent[0]; !strings.Contains(rec.Failed[TargetWebhook], "switched off") {
		t.Errorf("record = %+v", rec)
	}

	n.Forget()
	if st := n.Status(); len(st.Recent) != 0 || len(st.Targets) != 0 {
		t.Errorf("status after Forget = %+v", st)
	}
}
