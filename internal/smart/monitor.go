package smart

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// checkTimeout bounds one drive's verdict. A drive that does not answer a
// health command in half a minute is not going to.
const checkTimeout = 30 * time.Second

// firstCheck is how long after start-up the drives are first asked, so
// the poll does not compete with everything else a boot is doing.
const firstCheck = time.Minute

// Monitor asks every drive for its verdict on a timer and remembers the
// answers, so a failing drive is a warning on the dashboard before anyone
// opens Diagnostics.
type Monitor struct {
	Client *Client
	Every  time.Duration // default time.Hour
	// OnTick, when set, is called after every pass, so the crons page can
	// say when the router last asked.
	OnTick func()
	Log    *slog.Logger

	mu   sync.Mutex
	last map[string]Health
}

// Run checks a minute after start-up and then on the interval, until the
// context is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	every := m.Every
	if every <= 0 {
		every = time.Hour
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(firstCheck):
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check asks every drive the scan finds whether it is failing. A drive
// that is asleep or does not answer keeps the verdict it last gave; a
// drive that is gone is forgotten.
func (m *Monitor) Check(ctx context.Context) {
	defer func() {
		if m.OnTick != nil {
			m.OnTick()
		}
	}()
	if m.Client == nil {
		return
	}
	// The scan opens every device, so a drive that hangs can hang it too;
	// bounded, or the poll would never tick again.
	scanCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	devs, err := m.Client.Scan(scanCtx)
	cancel()
	if err != nil {
		m.logger().Debug("could not scan the drives", "err", err)
		return
	}
	next := make(map[string]Health, len(devs))
	for _, d := range devs {
		next[d.Name] = m.check(ctx, d)
	}
	m.mu.Lock()
	m.last = next
	m.mu.Unlock()
}

func (m *Monitor) check(ctx context.Context, d Device) Health {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	h, err := m.Client.Health(ctx, d)
	if err == nil && !h.Skipped {
		return h
	}
	if err != nil {
		m.logger().Debug("could not read a drive's health", "drive", d.Name, "err", err)
		// A drive that would not answer has not said it is failing, and
		// must never be reported as though it had.
		h = Health{Device: d, Skipped: true, Checked: time.Now()}
	}
	// Nothing new to say: the last verdict still stands, and a drive
	// asleep at the top of the hour is not news.
	if prev, ok := m.previous(d.Name); ok {
		return prev
	}
	return h
}

func (m *Monitor) previous(name string) (Health, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.last[name]
	return h, ok
}

// Latest is every drive's last verdict, by name.
func (m *Monitor) Latest() []Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Health, 0, len(m.last))
	for _, h := range m.last {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Failing is the drives that said they are failing. A drive that was
// never read is not one of them.
func (m *Monitor) Failing() []Health {
	var out []Health
	for _, h := range m.Latest() {
		if !h.Passed && !h.Skipped {
			out = append(out, h)
		}
	}
	return out
}

func (m *Monitor) logger() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}
