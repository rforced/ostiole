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

// Kind separates the jobs Ostiole runs on its own account from the ones
// an operator asked for. Both are shown together, because "what does this
// box do while I am not looking" is one question.
type Kind string

// Job kinds.
const (
	// KindSystem is work Ostiole does whether or not anyone configures it.
	KindSystem Kind = "system"
	// KindUser is a job from the configuration.
	KindUser Kind = "user"
)

// Status is one job as the UI sees it.
type Status struct {
	ID          string `json:"id"`
	Kind        Kind   `json:"kind"`
	Description string `json:"description"`
	// Schedule is the cron expression, or a plain description like "every
	// 5 seconds" for the work that is not on a cron at all.
	Schedule string `json:"schedule"`
	Enabled  bool   `json:"enabled"`
	// Job names what it does, for a user job.
	Job string `json:"job,omitempty"`
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

// Executor runs one job and returns whatever it wants recorded.
type Executor interface {
	Run(ctx context.Context, job model.Cron) (string, error)
}

// SystemJob describes background work Ostiole does itself. It is
// reported, not scheduled: the worker that does it keeps its own timer.
type SystemJob struct {
	ID          string
	Description string
	// Every is how often it happens, for the description.
	Every time.Duration
	// Note replaces the period when the work is not on a fixed clock.
	Note string
}

// Runner ticks once a minute and runs whatever is due.
type Runner struct {
	// Source reads the configuration in force, so a job added through the
	// UI starts running without a restart.
	Source func() *model.Config
	// Exec runs a job.
	Exec Executor
	Log  *slog.Logger
	// System lists the background work to report alongside the user jobs.
	System []SystemJob

	mu      sync.Mutex
	results map[string]*result
}

type result struct {
	lastRun  time.Time
	lastErr  string
	output   string
	duration time.Duration
	running  bool
}

// maxOutput is how much of a job's output is kept. Enough to see what
// went wrong, not enough to fill memory with a chatty script.
const maxOutput = 4000

// NewRunner returns a runner with the standard background work listed.
func NewRunner(source func() *model.Config, exec Executor, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		Source:  source,
		Exec:    exec,
		Log:     log,
		results: map[string]*result{},
		System: []SystemJob{
			{ID: "system:gateways", Description: "Probe each gateway and move the default route off one that stops answering", Every: 5 * time.Second},
			{ID: "system:aliases", Description: "Refresh the blocklists and country ranges that are due", Every: 15 * time.Minute},
			{ID: "system:sessions", Description: "Expire idle web sessions", Note: "as they expire"},
			{ID: "system:firewall-log", Description: "Collect dropped packets from the kernel", Note: "continuously"},
		},
	}
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
			return
		case <-time.After(time.Until(next)):
		}
		r.Tick(ctx, time.Now())
	}
}

// Tick runs every job whose schedule matches the given minute.
func (r *Runner) Tick(ctx context.Context, now time.Time) {
	cfg := r.config()
	if cfg == nil {
		return
	}
	for _, job := range cfg.Crons {
		if !job.Enabled {
			continue
		}
		s, err := Parse(job.Schedule)
		if err != nil {
			r.record(job.ID, now, 0, "", fmt.Errorf("schedule: %w", err))
			continue
		}
		if !s.Matches(now) {
			continue
		}
		r.start(ctx, job)
	}
}

// RunNow runs one job immediately, which is the "run it now" button.
func (r *Runner) RunNow(ctx context.Context, id string) error {
	cfg := r.config()
	if cfg == nil {
		return errors.New("nothing is configured yet")
	}
	job, ok := cfg.Cron(id)
	if !ok {
		return fmt.Errorf("no job called %q", id)
	}
	return r.run(ctx, *job)
}

// start runs a job in the background, skipping it if the previous run is
// still going: a backup that takes longer than its period should not pile
// up on itself.
func (r *Runner) start(ctx context.Context, job model.Cron) {
	r.mu.Lock()
	if res := r.results[job.ID]; res != nil && res.running {
		r.mu.Unlock()
		r.Log.Warn("skipping a scheduled job because the last run has not finished", "job", job.ID)
		return
	}
	r.mu.Unlock()
	go func() {
		if err := r.run(context.WithoutCancel(ctx), job); err != nil {
			r.Log.Warn("a scheduled job failed", "job", job.ID, "err", err)
		}
	}()
}

func (r *Runner) run(ctx context.Context, job model.Cron) error {
	r.mu.Lock()
	res, ok := r.results[job.ID]
	if !ok {
		res = &result{}
		r.results[job.ID] = res
	}
	res.running = true
	r.mu.Unlock()

	started := time.Now()
	output, err := r.Exec.Run(ctx, job)
	r.record(job.ID, started, time.Since(started), output, err)
	return err
}

func (r *Runner) record(id string, started time.Time, took time.Duration, output string, err error) {
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n… (truncated)"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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
}

// Statuses reports the user jobs and the background work together, user
// jobs first.
func (r *Runner) Statuses() []Status {
	out := []Status{}
	now := time.Now()
	if cfg := r.config(); cfg != nil {
		for _, job := range cfg.Crons {
			st := Status{
				ID:          job.ID,
				Kind:        KindUser,
				Description: job.Description,
				Schedule:    job.Schedule,
				Enabled:     job.Enabled,
				Job:         string(job.Job),
			}
			if st.Description == "" {
				st.Description = string(job.Job)
			}
			if s, err := Parse(job.Schedule); err == nil && job.Enabled {
				if next, ok := s.Next(now); ok {
					st.Next = &next
				}
			}
			r.fill(&st)
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	for _, sys := range r.System {
		st := Status{
			ID:          sys.ID,
			Kind:        KindSystem,
			Description: sys.Description,
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
// page can show when the box last did it.
func (r *Runner) Note(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.results[id]
	if !ok {
		res = &result{}
		r.results[id] = res
	}
	res.lastRun = time.Now()
}

func (r *Runner) config() *model.Config {
	if r.Source == nil {
		return nil
	}
	return r.Source()
}
