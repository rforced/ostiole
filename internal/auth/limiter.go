package auth

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/atomicfile"
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

// SignInsFile keeps when each address last signed in, so a restart in the
// middle of a flood of failed logins does not shut the owner out with
// everybody else.
const SignInsFile = "signins.json"

type limiter struct {
	mu   sync.Mutex
	hits map[string]*bucket
	all  bucket
	// trusted is when each address last signed in.
	trusted map[string]time.Time
	now     func() time.Time
	// path keeps trusted across a restart; empty keeps it in memory.
	path string
}

type signInsFile struct {
	Addresses map[string]time.Time `json:"addresses"`
}

type bucket struct {
	failures int
	start    time.Time
}

// newLimiter keeps its trusted addresses in dir, or in memory when dir is
// empty.
func newLimiter(dir string, now func() time.Time) *limiter {
	l := &limiter{hits: map[string]*bucket{}, trusted: map[string]time.Time{}, now: now}
	if dir != "" {
		l.path = filepath.Join(dir, SignInsFile)
	}
	l.load()
	return l
}

// load reads the addresses that signed in before a restart. A file that
// will not read costs only that: until they sign in again, they are
// strangers to a flood.
func (l *limiter) load() {
	if l.path == "" {
		return
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var f signInsFile
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	now := l.now()
	for key, last := range f.Addresses {
		if now.Sub(last) <= TrustedFor {
			l.trusted[key] = last
		}
	}
}

// persist writes the trusted addresses out, with the lock held. Like the
// sessions it is best effort: a sign-in does not fail over a full disk.
func (l *limiter) persist() {
	if l.path == "" {
		return
	}
	raw, err := json.Marshal(signInsFile{Addresses: l.trusted})
	if err != nil {
		return
	}
	_ = atomicfile.Write(l.path, raw, 0o600)
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
	l.persist()
}
