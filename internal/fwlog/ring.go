package fwlog

import (
	"sync"
)

// initialRing is how many places a ring starts with; it doubles from there
// up to the ceiling as packets arrive.
const initialRing = 1024

// Ring keeps the last N entries and fans new ones out to subscribers. The
// ceiling is configuration, so it changes on an apply; the ring grows into
// it rather than being allocated whole, because a router that is told to
// keep a million packets should not pay for them until it has seen them.
type Ring struct {
	mu sync.Mutex
	// ring holds the entries in arrival order from start, n of them used.
	ring    []Entry
	start   int
	n       int
	size    int
	subs    map[chan Entry]struct{}
	dropped uint64
}

// NewRing returns a ring holding up to size entries.
func NewRing(size int) *Ring {
	r := &Ring{subs: map[chan Entry]struct{}{}}
	r.Configure(size)
	return r
}

// Configure sets the ceiling, keeping the newest entries that fit. A ring
// with room to grow is left alone; it grows as packets arrive.
func (r *Ring) Configure(size int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if size < 1 {
		size = 1
	}
	if r.size == size && r.ring != nil {
		return
	}
	r.size = size
	if r.ring != nil && len(r.ring) <= size {
		return
	}
	held := min(r.n, size)
	next := make([]Entry, min(size, max(held, initialRing)))
	for i := range held {
		next[i] = r.ring[(r.start+r.n-held+i)%len(r.ring)]
	}
	r.ring, r.start, r.n = next, 0, held
}

// Size is the ceiling the ring is holding to.
func (r *Ring) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Add stores e and delivers it to subscribers without blocking; slow
// subscribers lose entries rather than stall the listener.
func (r *Ring) Add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.n < len(r.ring):
		r.ring[(r.start+r.n)%len(r.ring)] = e
		r.n++
	case len(r.ring) < r.size:
		r.grow()
		r.ring[r.n] = e
		r.n++
	default:
		r.ring[r.start] = e
		r.start = (r.start + 1) % len(r.ring)
	}
	for ch := range r.subs {
		select {
		case ch <- e:
		default:
			r.dropped++
		}
	}
}

// grow gives a full ring more places, doubling up to the ceiling, and puts
// the entries back in order from the front. On the way to a million
// entries it runs ten times. The caller holds the lock.
func (r *Ring) grow() {
	next := make([]Entry, min(r.size, max(2*len(r.ring), initialRing)))
	for i := range r.n {
		next[i] = r.ring[(r.start+i)%len(r.ring)]
	}
	r.ring, r.start = next, 0
}

// Recent returns up to limit entries, newest first: the order the API
// serves every log in, and the order the log pages read in.
func (r *Ring) Recent(limit int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit > r.n {
		limit = r.n
	}
	out := make([]Entry, limit)
	// The ring holds them oldest first, so it is filled back to front.
	for i := range limit {
		out[limit-1-i] = r.ring[(r.start+r.n-limit+i)%len(r.ring)]
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
