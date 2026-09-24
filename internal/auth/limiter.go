package auth

import (
	"sync"
	"time"
)

// Login rate limiting: after MaxFailures failed attempts from one address
// within FailureWindow, that address is refused until the window passes.
const (
	MaxFailures   = 5
	FailureWindow = 15 * time.Minute
)

type limiter struct {
	mu   sync.Mutex
	hits map[string]*bucket
	now  func() time.Time
}

type bucket struct {
	failures int
	start    time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{hits: map[string]*bucket{}, now: now}
}

// blocked reports whether key has exhausted its attempts.
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.hits[key]
	if !ok {
		return false
	}
	if l.now().Sub(b.start) > FailureWindow {
		delete(l.hits, key)
		return false
	}
	return b.failures >= MaxFailures
}

func (l *limiter) failure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.hits) > 10000 {
		for k, b := range l.hits {
			if now.Sub(b.start) > FailureWindow {
				delete(l.hits, k)
			}
		}
	}
	b, ok := l.hits[key]
	if !ok || now.Sub(b.start) > FailureWindow {
		l.hits[key] = &bucket{failures: 1, start: now}
		return
	}
	b.failures++
}

func (l *limiter) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
