package fwlog

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// initialRing is how many places a ring starts with; it doubles from there
// up to the ceiling as packets arrive.
const initialRing = 1024

// Ring keeps the last N entries, none older than it is told to keep, and
// fans new ones out to subscribers. The ceiling is configuration, so it
// changes on an apply; the ring grows into it rather than being allocated
// whole, because a router that is told to keep a million packets should not
// pay for them until it has seen them.
type Ring struct {
	mu sync.Mutex
	// ring holds the entries in arrival order from start, n of them used.
	ring    []Entry
	start   int
	n       int
	size    int
	keep    time.Duration
	seq     uint64
	subs    map[chan Entry]struct{}
	dropped uint64
}

// NewRing returns a ring holding up to size entries, of any age.
func NewRing(size int) *Ring {
	r := &Ring{subs: map[chan Entry]struct{}{}}
	r.Configure(size, 0)
	return r
}

// Configure sets the ceiling and how long an entry is kept, zero for ever,
// keeping the newest entries that fit. A ring with room to grow is left
// alone; it grows as packets arrive.
func (r *Ring) Configure(size int, keep time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keep = keep
	r.expire(time.Now())
	if size < 1 {
		size = 1
	}
	if r.size == size && r.ring != nil {
		return
	}
	r.size = size
	r.fit()
}

// fit trims the ring to its ceiling, keeping the newest. The caller holds the lock.
func (r *Ring) fit() {
	if r.ring != nil && len(r.ring) <= r.size {
		return
	}
	held := min(r.n, r.size)
	next := make([]Entry, min(r.size, max(held, initialRing)))
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
	r.expire(time.Now())
	r.seq++
	e.Seq = r.seq
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
	r.expire(time.Now())
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

// at is the entry i places from the oldest. The caller holds the lock.
func (r *Ring) at(i int) *Entry { return &r.ring[(r.start+i)%len(r.ring)] }

// Before implements logsearch.Log: the entries older than seq, newest
// first, copied out under the lock so a search never holds up the logging.
func (r *Ring) Before(seq uint64, buf []Entry) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(time.Now())
	i := r.n - 1
	if seq != 0 {
		i = sort.Search(r.n, func(k int) bool { return r.at(k).Seq >= seq }) - 1
	}
	n := 0
	for ; i >= 0 && n < len(buf); i-- {
		buf[n] = *r.at(i)
		n++
	}
	return n, i >= 0
}

// After copies the entries after seq into buf, oldest first, under the
// lock, and says whether newer ones remain: what the writer that keeps the
// log in files reads.
func (r *Ring) After(seq uint64, buf []Entry) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := sort.Search(r.n, func(k int) bool { return r.at(k).Seq > seq })
	n := 0
	for ; i < r.n && n < len(buf); i++ {
		buf[n] = *r.at(i)
		n++
	}
	return n, i < r.n
}

// Newest is the number of the newest entry added, zero before the first.
func (r *Ring) Newest() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// Restore fills the ring with entries read back from its files, oldest
// first, with the numbers they were given, keeping what its size and age
// allow. Numbering goes on after the higher of the last of them and
// newest, the highest number in the files, so none is given twice. It
// refuses once an entry has been added: the numbers would go backwards.
func (r *Ring) Restore(entries []Entry, newest uint64) error {
	s, err := r.Restorer(len(entries))
	if err != nil {
		return err
	}
	for _, e := range entries {
		s.Push(e)
	}
	_, err = s.Done(newest)
	return err
}

var errTaken = errors.New("the firewall log has taken entries already")

// Restorer fills a ring of its own from the files, for Done to hand the ring whole.
type Restorer struct {
	r     *Ring
	ring  []Entry
	start int
	n     int
	size  int
	last  uint64
}

// Restorer starts a read-back sized for count entries; it refuses once an entry is added.
func (r *Ring) Restorer(count int) (*Restorer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seq > 0 {
		return nil, errTaken
	}
	return &Restorer{r: r, ring: make([]Entry, min(r.size, max(count, 0))), size: r.size}, nil
}

// Push adds e unless its number does not rise, the oldest giving way at the size.
func (s *Restorer) Push(e Entry) {
	if e.Seq <= s.last {
		return
	}
	s.last = e.Seq
	switch {
	case s.n < len(s.ring):
		s.ring[(s.start+s.n)%len(s.ring)] = e
		s.n++
	case len(s.ring) < s.size:
		s.grow()
		s.ring[s.n] = e
		s.n++
	default:
		s.ring[s.start] = e
		s.start = (s.start + 1) % len(s.ring)
	}
}

func (s *Restorer) grow() {
	next := make([]Entry, min(s.size, max(2*len(s.ring), initialRing)))
	n := copy(next, s.ring[s.start:])
	copy(next[n:], s.ring[:s.start])
	s.ring, s.start = next, 0
}

// Done hands the ring what came back, as Restore does, and says how many it keeps.
func (s *Restorer) Done(newest uint64) (int, error) {
	r := s.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seq > 0 {
		return 0, errTaken
	}
	r.seq = max(newest, s.last)
	if s.n > 0 {
		r.ring, r.start, r.n = s.ring, s.start, s.n
		r.fit()
	}
	r.expire(time.Now())
	return r.n, nil
}

// Clear empties the ring. The numbers carry on, so what arrives next is
// newer than anything a page read before.
func (r *Ring) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ring, r.start, r.n = make([]Entry, min(r.size, initialRing)), 0, 0
}

// Held is how many packets the ring holds and when the oldest arrived.
func (r *Ring) Held() (int, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(time.Now())
	if r.n == 0 {
		return 0, time.Time{}
	}
	return r.n, r.at(0).Time
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

// expire drops what has aged out. Entries arrive in time order, so this is
// the front of the ring. The caller holds the lock.
func (r *Ring) expire(now time.Time) {
	if r.keep <= 0 {
		return
	}
	cutoff := now.Add(-r.keep)
	for r.n > 0 && r.ring[r.start].Time.Before(cutoff) {
		r.ring[r.start] = Entry{}
		r.start = (r.start + 1) % len(r.ring)
		r.n--
	}
}
