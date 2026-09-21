package cron

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// persisted is one cron's last attempt as it is kept on disk.
//
// It is the reportable half of result: whatever was running when the
// daemon stopped is not running now, so that flag is not written and
// comes back false.
type persisted struct {
	LastRun time.Time `json:"lastRun,omitzero"`
	// LastError is why it failed, if it did.
	LastError string `json:"lastError,omitempty"`
	// Output is what it printed, already truncated to maxOutput.
	Output string `json:"output,omitempty"`
	// Seconds is how long it took.
	Seconds float64 `json:"seconds,omitempty"`
}

// path sits beside sessions.json and the rest of the small state.
func (r *Runner) path() string { return filepath.Join(r.Dir, "crons.json") }

// load reads what the last process left behind. Without it a restart
// reports work the router did at four in the morning as never having
// happened, which is the opposite of what this page is for.
func (r *Runner) load() {
	if r.Dir == "" {
		return
	}
	raw, err := os.ReadFile(r.path())
	if err != nil {
		return
	}
	var saved map[string]persisted
	if err := json.Unmarshal(raw, &saved); err != nil {
		r.Log.Warn("could not read the cron results; starting empty", "err", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range saved {
		if p.LastRun.IsZero() {
			continue
		}
		r.results[id] = &result{
			lastRun:  p.LastRun,
			lastErr:  p.LastError,
			output:   p.Output,
			duration: time.Duration(p.Seconds * float64(time.Second)),
		}
	}
}

// live is every cron id this router still knows about, so the results of
// one an operator deleted do not sit in the file for ever. It is nil
// while there is no configuration to check against, which keeps a save
// during startup from emptying the file.
func (r *Runner) live() map[string]bool {
	cfg := r.config()
	if cfg == nil {
		return nil
	}
	ids := make(map[string]bool, len(cfg.Crons)+len(r.System)+4)
	for _, c := range scheduled(cfg) {
		ids[c.ID] = true
	}
	for _, s := range r.System {
		ids[s.ID] = true
	}
	return ids
}

// save writes the results out. The disk write happens outside mu,
// because Statuses waits on that one and a slow disk is not worth an API
// stall.
//
// saveMu covers the snapshot and the write together, so the last file on
// disk is the newest state rather than whichever writer happened to
// finish second. The two update checks share a schedule, so finishing at
// the same moment is the normal case here, not a rare one.
func (r *Runner) save() {
	if r.Dir == "" {
		return
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()

	keep := r.live()

	r.mu.Lock()
	r.dirty = false
	saved := make(map[string]persisted, len(r.results))
	for id, res := range r.results {
		if res.lastRun.IsZero() || (keep != nil && !keep[id]) {
			continue
		}
		saved[id] = persisted{
			LastRun:   res.lastRun,
			LastError: res.lastErr,
			Output:    res.output,
			Seconds:   res.duration.Seconds(),
		}
	}
	r.mu.Unlock()

	raw, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return
	}
	// Written beside and renamed over, so a router that loses power
	// mid-write keeps the last good copy rather than half of this one.
	// 0600: what this router does while nobody is watching is nobody
	// else's business.
	tmp := r.path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		r.Log.Warn("could not write the cron results", "err", err)
		return
	}
	if err := os.Rename(tmp, r.path()); err != nil {
		r.Log.Warn("could not replace the cron results", "err", err)
		_ = os.Remove(tmp)
	}
}

// flush writes out work that only noted itself, if any has since the
// last write.
func (r *Runner) flush() {
	r.mu.Lock()
	dirty := r.dirty
	r.mu.Unlock()
	if dirty {
		r.save()
	}
}
