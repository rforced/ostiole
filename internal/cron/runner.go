package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// Origin separates the crons Ostiole runs on its own account from the
// ones an operator asked for. Both are shown together, because "what does
// this router do while I am not looking" is one question.
type Origin string

// Cron origins.
const (
	// OriginSystem is work Ostiole does whether or not anyone configures it.
	OriginSystem Origin = "system"
	// OriginUser is a cron from the configuration.
	OriginUser Origin = "user"
)

// Status is one cron as the UI sees it.
type Status struct {
	ID          string `json:"id"`
	Origin      Origin `json:"origin"`
	Description string `json:"description"`
	// Schedule is the cron expression, or a plain description like "every
	// 5 seconds" for the work that is not on a cron at all.
	Schedule string `json:"schedule"`
	Enabled  bool   `json:"enabled"`
	// Kind names what it does, for a cron from the configuration.
	Kind string `json:"kind,omitempty"`
	// Next is when it runs next, empty when that is not a fixed time.
	Next *time.Time `json:"next,omitempty"`
	// LastRun, LastError, and LastOutput describe the most recent attempt.
	LastRun     *time.Time `json:"lastRun,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	LastOutput  string     `json:"lastOutput,omitempty"`
	LastSeconds float64    `json:"lastSeconds,omitempty"`
	// Running is true while it is going.
	Running bool `json:"running"`
}

// Executor runs one cron and returns whatever it wants recorded.
type Executor interface {
	Run(ctx context.Context, c model.Cron) (string, error)
}

// SystemCron describes background work Ostiole does itself. It is
// reported, not scheduled: the worker that does it keeps its own timer.
type SystemCron struct {
	ID          string
	Description string
	// Describe, when set, replaces Description with one read from the
	// configuration, so work that only half applies to this router says
	// which half it is doing.
	Describe func(cfg *model.Config) string
	// Every is how often it happens, for the description.
	Every time.Duration
	// Note replaces the period when the work is not on a fixed clock.
	Note string
}

// describe is Describe where there is one and a configuration to read,
// and the fixed text otherwise.
func (s SystemCron) describe(cfg *model.Config) string {
	if s.Describe != nil && cfg != nil {
		return s.Describe(cfg)
	}
	return s.Description
}

// Runner ticks once a minute and runs whatever is due.
type Runner struct {
	// Source reads the configuration in force, so a cron added through the
	// UI starts running without a restart.
	Source func() *model.Config
	// Exec runs a cron.
	Exec Executor
	Log  *slog.Logger
	// System lists the background work to report alongside the operator's
	// crons.
	System []SystemCron
	// Dir is where the results are kept between runs. Empty remembers
	// nothing, which is a test or a binary with nowhere to write.
	Dir string

	mu      sync.Mutex
	results map[string]*result
	// dirty marks work that noted itself and has not been written out.
	dirty bool
	// saveMu orders the writes to disk; see save.
	saveMu sync.Mutex
}

type result struct {
	lastRun  time.Time
	lastErr  string
	output   string
	duration time.Duration
	running  bool
}

// maxOutput is how much of a cron's output is kept. Enough to see what
// went wrong, not enough to fill memory with a chatty script.
const maxOutput = 4000

// NewRunner returns a runner with the standard background work listed.
// Everything the daemon starts on a timer of its own belongs here: the
// page is the whole answer to "what does this router do while nobody is
// watching", so work missing from this list is work nobody knows about.
// The ids match what the workers pass to Note.
//
// dir is where the results are kept, so a restart does not report a
// nightly check as never having run; empty remembers nothing.
func NewRunner(source func() *model.Config, exec Executor, log *slog.Logger, dir string) *Runner {
	if log == nil {
		log = slog.Default()
	}
	r := &Runner{
		Source:  source,
		Exec:    exec,
		Log:     log,
		Dir:     dir,
		results: map[string]*result{},
		System: []SystemCron{
			{
				ID: "system:gateways",
				// The probes run whatever the gateway count is, so the
				// fixed text is the half that is always true.
				Description: gatewayProbeOnly,
				Describe:    describeGateways,
				Every:       5 * time.Second,
			},
			{ID: "system:aliases", Description: "Refresh the address lists and country ranges that are due", Every: 15 * time.Minute},
			{ID: "system:blocklists", Description: "Refresh the DNS blocklists that are due and hand them to the resolver", Every: 15 * time.Minute},
			{ID: "system:sessions", Description: "Expire idle web sessions", Note: "as they expire"},
			{ID: "system:firewall-log", Description: "Collect dropped packets from the kernel", Note: "continuously"},
			{ID: "system:query-log", Description: "Collect the resolver's answers from the kernel", Note: "continuously"},
			{ID: "system:drives", Description: "Ask each drive whether it is failing", Every: time.Hour},
		},
	}
	r.load()
	return r
}

// What the gateway probe loop does, with and without a second gateway to
// move traffic to.
const (
	gatewayFailover  = "Probe each gateway and move traffic off one that stops answering"
	gatewayProbeOnly = "Probe the gateway and report whether it answers"
)

// describeGateways says what the probe loop is actually doing. One gateway
// has nowhere to fail over to, so the page promises only the probing.
func describeGateways(cfg *model.Config) string {
	if cfg.CanFailover() {
		return gatewayFailover
	}
	return gatewayProbeOnly
}

// Run ticks until the context is cancelled. It aligns to the start of
// each minute, because a cron that fires at 04:00:59 is a cron that
// sometimes fires twice and sometimes not at all.
func (r *Runner) Run(ctx context.Context) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			r.flush()
			return
		case <-time.After(time.Until(next)):
		}
		r.Tick(ctx, time.Now())
		// The work that only notes itself is written out here rather than
		// on every note, because the gateway probe notes itself every five
		// seconds and the disk should not hear about all of them.
		r.flush()
	}
}

// scheduled is everything this router runs on a timer: the operator's crons
// and the ones the update settings imply.
func scheduled(cfg *model.Config) []model.Cron {
	all := make([]model.Cron, 0, len(cfg.Crons)+4)
	all = append(all, cfg.Crons...)
	return append(all, cfg.DerivedCrons()...)
}

// zoned reads an instant in the router's timezone, so "0 4 * * *" fires at
// four in the morning as the operator's clock has it rather than as the
// process happened to be started.
func zoned(cfg *model.Config, t time.Time) time.Time {
	if cfg == nil {
		return t
	}
	return t.In(cfg.System.Location())
}

// Tick runs every cron whose schedule matches the given minute.
func (r *Runner) Tick(ctx context.Context, now time.Time) {
	cfg := r.config()
	if cfg == nil {
		return
	}
	now = zoned(cfg, now)
	for _, c := range scheduled(cfg) {
		if !c.Enabled {
			continue
		}
		s, err := Parse(c.Schedule)
		if err != nil {
			r.record(c.ID, now, 0, "", fmt.Errorf("schedule: %w", err))
			continue
		}
		if !s.Matches(now) {
			continue
		}
		r.start(ctx, c)
	}
}

// RunNow runs one cron immediately, which is the "run it now" button.
func (r *Runner) RunNow(ctx context.Context, id string) error {
	cfg := r.config()
	if cfg == nil {
		return errors.New("nothing is configured yet")
	}
	c, ok := cfg.Cron(id)
	if !ok {
		return fmt.Errorf("no cron called %q", id)
	}
	return r.run(ctx, *c)
}

// start runs a cron in the background, skipping it if the previous run is
// still going: a backup that takes longer than its period should not pile
// up on itself.
func (r *Runner) start(ctx context.Context, c model.Cron) {
	r.mu.Lock()
	if res := r.results[c.ID]; res != nil && res.running {
		r.mu.Unlock()
		r.Log.Warn("skipping a scheduled cron because the last run has not finished", "cron", c.ID)
		return
	}
	r.mu.Unlock()
	go func() {
		if err := r.run(context.WithoutCancel(ctx), c); err != nil {
			r.Log.Warn("a scheduled cron failed", "cron", c.ID, "err", err)
		}
	}()
}

func (r *Runner) run(ctx context.Context, c model.Cron) error {
	r.mu.Lock()
	res, ok := r.results[c.ID]
	if !ok {
		res = &result{}
		r.results[c.ID] = res
	}
	res.running = true
	r.mu.Unlock()

	started := time.Now()
	output, err := r.Exec.Run(ctx, c)
	r.record(c.ID, started, time.Since(started), output, err)
	return err
}

func (r *Runner) record(id string, started time.Time, took time.Duration, output string, err error) {
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n… (truncated)"
	}
	r.mu.Lock()
	res, ok := r.results[id]
	if !ok {
		res = &result{}
		r.results[id] = res
	}
	res.running = false
	res.lastRun = started
	res.duration = took
	res.output = output
	res.lastErr = ""
	if err != nil {
		res.lastErr = err.Error()
	}
	r.mu.Unlock()
	// A scheduled run happens daily at most, and is the whole reason this
	// is kept, so it goes to disk now rather than waiting for a tick.
	r.save()
}

// Statuses reports the operator's crons and the background work
// together, the operator's first.
func (r *Runner) Statuses() []Status {
	out := []Status{}
	cfg := r.config()
	now := zoned(cfg, time.Now())
	if cfg != nil {
		for _, c := range cfg.Crons {
			st := Status{
				ID:          c.ID,
				Origin:      OriginUser,
				Description: c.Description,
				Schedule:    c.Schedule,
				Enabled:     c.Enabled,
				Kind:        string(c.Kind),
			}
			if st.Description == "" {
				st.Description = string(c.Kind)
			}
			if s, err := Parse(c.Schedule); err == nil && c.Enabled {
				if next, ok := s.Next(now); ok {
					st.Next = &next
				}
			}
			r.fill(&st)
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	// The update crons are Ostiole's own work, but on a schedule the
	// operator chose, so they are reported with one.
	if cfg != nil {
		for _, c := range cfg.DerivedCrons() {
			st := Status{
				ID:          c.ID,
				Origin:      OriginSystem,
				Description: c.Description,
				Schedule:    c.Schedule,
				Enabled:     c.Enabled,
				Kind:        string(c.Kind),
			}
			if s, err := Parse(c.Schedule); err == nil && c.Enabled {
				if next, ok := s.Next(now); ok {
					st.Next = &next
				}
			}
			r.fill(&st)
			out = append(out, st)
		}
	}

	for _, sys := range r.System {
		st := Status{
			ID:          sys.ID,
			Origin:      OriginSystem,
			Description: sys.describe(cfg),
			Schedule:    sys.Note,
			Enabled:     true,
		}
		if st.Schedule == "" {
			st.Schedule = "every " + sys.Every.String()
		}
		r.fill(&st)
		out = append(out, st)
	}
	return out
}

func (r *Runner) fill(st *Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.results[st.ID]
	if !ok {
		return
	}
	if !res.lastRun.IsZero() {
		last := res.lastRun
		st.LastRun = &last
	}
	st.LastError = res.lastErr
	st.LastOutput = res.output
	st.LastSeconds = res.duration.Seconds()
	st.Running = res.running
}

// Note records that a piece of background work just happened, so the
// page can show when the router last did it.
func (r *Runner) Note(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.results[id]
	if !ok {
		res = &result{}
		r.results[id] = res
	}
	res.lastRun = time.Now()
	r.dirty = true
}

func (r *Runner) config() *model.Config {
	if r.Source == nil {
		return nil
	}
	return r.Source()
}
