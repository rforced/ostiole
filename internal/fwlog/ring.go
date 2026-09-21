package fwlog

import (
	"sync"
)

// Ring keeps the last N entries and fans new ones out to subscribers.
type Ring struct {
	mu      sync.Mutex
	entries []Entry
	next    int
	full    bool
	subs    map[chan Entry]struct{}
	dropped uint64
}

// NewRing returns a ring holding up to size entries.
func NewRing(size int) *Ring {
	if size < 1 {
		size = 1
	}
	return &Ring{entries: make([]Entry, size), subs: map[chan Entry]struct{}{}}
}

// Add stores e and delivers it to subscribers without blocking; slow
// subscribers lose entries rather than stall the listener.
func (r *Ring) Add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
	for ch := range r.subs {
		select {
		case ch <- e:
		default:
			r.dropped++
		}
	}
}

// Recent returns up to limit entries, newest first: the order the API
// serves every log in, and the order the log pages read in.
func (r *Ring) Recent(limit int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = len(r.entries)
	}
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Entry, limit)
	start := r.next - limit
	if start < 0 {
		start += len(r.entries)
	}
	// The ring holds them oldest first, so it is filled back to front.
	for i := range limit {
		out[limit-1-i] = r.entries[(start+i)%len(r.entries)]
	}
	return out
}

// Subscribe returns a channel of new entries and a cancel function.
func (r *Ring) Subscribe(buffer int) (<-chan Entry, func()) {
	ch := make(chan Entry, buffer)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

// Dropped counts entries subscribers were too slow to receive.
func (r *Ring) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}
