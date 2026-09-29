// Package panics keeps a panic in background work from taking the daemon
// down with it. net/http recovers a handler; nothing recovers a goroutine,
// and one panic anywhere ends the process, the management plane with it,
// and the revert of an apply nobody has confirmed yet.
package panics

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// Pauses between the starts of a loop that keeps panicking.
const (
	firstPause = 5 * time.Second
	maxPause   = 5 * time.Minute
)

// Loop runs fn until it returns. A panic is logged with its stack and fn
// starts again after a pause, which doubles while it keeps panicking.
func Loop(ctx context.Context, log *slog.Logger, name string, fn func(context.Context)) {
	loop(ctx, log, name, fn, firstPause, maxPause)
}

func loop(ctx context.Context, log *slog.Logger, name string, fn func(context.Context), first, most time.Duration) {
	pause := first
	for {
		started := time.Now()
		if !panicked(ctx, log, name, fn) {
			return
		}
		// One that ran for a good while before this is not the same fault
		// coming round again.
		if time.Since(started) > most {
			pause = first
		}
		t := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		pause = min(2*pause, most)
	}
}

func panicked(ctx context.Context, log *slog.Logger, name string, fn func(context.Context)) (yes bool) {
	defer func() {
		if p := recover(); p != nil {
			logger(log).Error("panic; starting it again", "in", name, "panic", p, "stack", string(debug.Stack()))
			yes = true
		}
	}()
	fn(ctx)
	return false
}

func logger(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}

// Into turns a panic into the error of the function it is deferred in, so
// the work fails the way any failure does and the daemon carries on:
//
//	defer panics.Into(&err, log, "cron backup")
func Into(err *error, log *slog.Logger, what string) {
	p := recover()
	if p == nil {
		return
	}
	logger(log).Error("panic", "in", what, "panic", p, "stack", string(debug.Stack()))
	*err = fmt.Errorf("%s panicked: %v", what, p)
}

// Recover ends a panic in the goroutine it is deferred in, and logs it:
//
//	defer panics.Recover(log, "certificate order")
func Recover(log *slog.Logger, what string) {
	if p := recover(); p != nil {
		logger(log).Error("panic", "in", what, "panic", p, "stack", string(debug.Stack()))
	}
}

// Drop is Recover for a callback that runs once a packet, whose work is
// dropped. One bad packet can come a thousand times a second, so it logs
// one panic a minute for each what and counts the rest:
//
//	defer panics.Drop(log, "firewall log packet")
func Drop(log *slog.Logger, what string) {
	p := recover()
	if p == nil {
		return
	}
	if n, ok := sample(what, time.Now()); ok {
		logger(log).Error("panic; dropped what it was doing", "in", what, "panic", p, "unlogged", n,
			"stack", string(debug.Stack()))
	}
}

// sampleEvery is how often Drop logs a panic in any one what.
const sampleEvery = time.Minute

var (
	sampleMu sync.Mutex
	logged   = map[string]time.Time{}
	unlogged = map[string]int{}
)

// sample reports whether a panic in what is logged now, and how many went
// unlogged since the last one that was.
func sample(what string, now time.Time) (int, bool) {
	sampleMu.Lock()
	defer sampleMu.Unlock()
	if now.Sub(logged[what]) < sampleEvery {
		unlogged[what]++
		return 0, false
	}
	n := unlogged[what]
	logged[what], unlogged[what] = now, 0
	return n, true
}
