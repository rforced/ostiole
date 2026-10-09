// Package memlimit keeps the runtime's memory limit at what the configuration needs.
package memlimit

import (
	"context"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"sync"
	"time"

	"ostiole/internal/dnsblock"
	"ostiole/internal/model"
)

const (
	// Floor is the smallest limit set: a daemon with nothing to keep.
	Floor = 256 << 20
	// Baseline is the daemon without history.
	Baseline = 150_000_000
	// IndexBytes is the names index per name: 8 live and about 20 while it builds.
	IndexBytes = 32
	headroom   = 1.5
	within     = 0.7
	interval   = time.Minute
)

// Derive is the heap the configuration needs on a machine with the budget:
// the daemon, the names index and the logs at the sizes kept, with headroom.
func Derive(cfg *model.Config, budget model.MemoryBudget) int64 {
	if cfg == nil {
		cfg = &model.Config{}
	}
	n := int64(Baseline)
	if cfg.Blocking.Enabled {
		ceiling := cfg.Blocking.MaxDomains
		if ceiling <= 0 {
			ceiling = dnsblock.DefaultMaxDomains
		}
		n += int64(ceiling) * IndexBytes
	}
	n += int64(float64(budget.LogsFullBytes(cfg)) * headroom)
	return max(n, Floor)
}

// Live is the heap's live bytes as of the last collection.
func Live() int64 {
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return int64(min(s[0].Value.Uint64(), math.MaxInt64))
}

// Limiter keeps the runtime's memory limit at what the configuration and
// the live heap need. GOMEMLIMIT in the environment leaves it alone.
type Limiter struct {
	Source func() *model.Config
	Budget model.MemoryBudget
	Log    *slog.Logger

	set  func(int64) int64
	live func() int64
	env  bool

	mu    sync.Mutex
	limit int64
}

// New makes a Limiter over the configuration.
func New(source func() *model.Config, budget model.MemoryBudget, log *slog.Logger) *Limiter {
	return &Limiter{
		Source: source, Budget: budget, Log: log,
		set:  debug.SetMemoryLimit,
		live: Live,
		env:  os.Getenv("GOMEMLIMIT") != "",
	}
}

// Apply sets the limit from the configuration, raised above the live heap
// when that comes within reach, and returns it; zero when the environment owns it.
func (l *Limiter) Apply() int64 {
	if l.env {
		return 0
	}
	want := Derive(l.Source(), l.Budget)
	live := l.live()
	if float64(live) > within*float64(want) {
		want = max(want, int64(float64(live)*headroom))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if want != l.limit {
		l.set(want)
		l.limit = want
		if l.Log != nil {
			l.Log.Info("set the memory limit", "limit", want, "live", live)
		}
	}
	return want
}

// Settle gives freed memory back to the system and applies the limit again.
func (l *Limiter) Settle() {
	debug.FreeOSMemory()
	l.Apply()
}

// Run applies the limit once a minute until ctx ends.
func (l *Limiter) Run(ctx context.Context) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Apply()
		}
	}
}
