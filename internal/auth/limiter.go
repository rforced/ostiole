package auth

import (
	"net/netip"
	"sync"
	"time"
)

// Login rate limiting: after MaxFailures failed attempts from one address
// within FailureWindow, that address is refused until the window passes.
// An IPv6 client is its /64, which is what one host can pick addresses
// from. Once MaxFailuresAll attempts have failed within the window from
// anywhere, only addresses that signed in within TrustedFor may try.
const (
	MaxFailures    = 5
	MaxFailuresAll = 30
	FailureWindow  = 15 * time.Minute
	TrustedFor     = 30 * 24 * time.Hour
)

type limiter struct {
	mu   sync.Mutex
	hits map[string]*bucket
	all  bucket
	// trusted is when each address last signed in.
	trusted map[string]time.Time
	now     func() time.Time
}

type bucket struct {
	failures int
	start    time.Time
}

func newLimiter(now func() time.Time) *limiter {
	return &limiter{hits: map[string]*bucket{}, trusted: map[string]time.Time{}, now: now}
}

// limitKey is what an address is counted as: itself, or its /64.
func limitKey(remote string) string {
	addr, err := netip.ParseAddr(remote)
	if err != nil {
		return remote
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	p, err := addr.Prefix(64)
	if err != nil {
		return remote
	}
	return p.String()
}

// blocked reports whether remote may not try now.
func (l *limiter) blocked(remote string) bool {
	key := limitKey(remote)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if b, ok := l.hits[key]; ok {
		if now.Sub(b.start) > FailureWindow {
			delete(l.hits, key)
		} else if b.failures >= MaxFailures {
			return true
		}
	}
	if now.Sub(l.all.start) > FailureWindow || l.all.failures < MaxFailuresAll {
		return false
	}
	last, ok := l.trusted[key]
	return !ok || now.Sub(last) > TrustedFor
}

func (l *limiter) failure(remote string) {
	key := limitKey(remote)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.all.start) > FailureWindow {
		l.all = bucket{start: now}
	}
	l.all.failures++
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

func (l *limiter) success(remote string) {
	key := limitKey(remote)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	delete(l.hits, key)
	if len(l.trusted) > 1000 {
		for k, last := range l.trusted {
			if now.Sub(last) > TrustedFor {
				delete(l.trusted, k)
			}
		}
	}
	l.trusted[key] = now
}
