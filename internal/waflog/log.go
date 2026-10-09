// Package waflog keeps what the reverse proxy's WAF matched in a ring in
// memory, fed from the proxy's journal: each event as the proxy writes it,
// and at start whatever it wrote after the newest event the ring got back
// from its files.
package waflog

import (
	"errors"
	"slices"
	"sort"
	"sync"
	"time"

	"ostiole/internal/journalfeed"
	"ostiole/internal/model"
	"ostiole/internal/wafevent"
)

// Entry is one event and its place in the log. Seq only grows, so a page
// asked for as "before this one" stays put while events arrive.
type Entry struct {
	Seq uint64 `json:"seq,omitempty"`
	// Logged is when the proxy's line reached the journal, and the log is
	// in that order. The proxy logs a request when it ends, so a WebSocket
	// is logged when it closes, long after the Time it opened.
	Logged time.Time `json:"logged"`
	wafevent.Event
}

// initialRing is how many places a ring starts with; it doubles from there
// up to the ceiling as events arrive.
const initialRing = 256

// Log keeps the WAF's events in a ring bounded by size and by age. It is
// the log the pages read, kept in files while the configuration says so;
// the journal only feeds it.
type Log struct {
	mu   sync.Mutex
	size int
	keep time.Duration
	// ring holds the entries in the order they were logged from start. It
	// grows towards size as events arrive rather than being allocated whole.
	ring  []Entry
	start int
	n     int
	seq   uint64

	subs    map[chan Entry]struct{}
	dropped uint64
	// lost is when the newest event the log let go of was logged, for its
	// ceiling or its age: a count from before then is short.
	lost time.Time
}

// New returns an empty log of the default size.
func New() *Log {
	return &Log{size: model.DefaultProxyEventEntries, keep: model.Logging{}.MemoryKeep(), subs: map[chan Entry]struct{}{}}
}

// Configure sets the ceiling and how long an event is kept, keeping the
// newest events that fit.
func (l *Log) Configure(size int, keep time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keep = keep
	l.expire(time.Now())
	if size < 1 {
		size = 1
	}
	if l.size == size {
		return
	}
	l.size = size
	l.fit()
}

// fit trims the ring to its ceiling, keeping the newest. The caller holds the lock.
func (l *Log) fit() {
	if len(l.ring) <= l.size {
		return
	}
	held := min(l.n, l.size)
	if held < l.n {
		l.let(l.at(l.n - held - 1).Logged)
	}
	next := make([]Entry, min(l.size, max(held, initialRing)))
	for i := range held {
		next[i] = l.ring[(l.start+l.n-held+i)%len(l.ring)]
	}
	l.ring, l.start, l.n = next, 0, held
}

// limits is the ceiling and the retention the log holds to.
func (l *Log) limits() (int, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size, l.keep
}

// Size is the ceiling the log is holding to.
func (l *Log) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.size
}

// Add stores ev, which the proxy logged at logged, and hands it to
// subscribers without blocking; a slow one loses events rather than
// stalling the reader.
func (l *Log) Add(logged time.Time, ev wafevent.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.send(l.put(Entry{Logged: logged, Event: ev}))
}

// AddAt stores an event the proxy logged at at: what the journal feeds.
func (l *Log) AddAt(at time.Time, ev wafevent.Event) { l.Add(at, ev) }

// FillAt stores what the journal held after the newest event the log had,
// given newest first.
func (l *Log) FillAt(items []journalfeed.Item[wafevent.Event]) {
	entries := make([]Entry, len(items))
	for i, it := range items {
		entries[i] = Entry{Logged: it.At, Event: it.E}
	}
	l.fill(entries)
}

// fill stores what the journal held after the newest event the log had,
// given newest first. As many as the log holds may mean the journal had
// more, which were not read.
func (l *Log) fill(entries []Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(entries) > 0 && len(entries) >= l.size {
		l.let(entries[len(entries)-1].Logged)
	}
	for _, e := range slices.Backward(entries) {
		l.send(l.put(e))
	}
}

// send hands e to each subscriber that has room. The caller holds the
// lock.
func (l *Log) send(e Entry) {
	for ch := range l.subs {
		select {
		case ch <- e:
		default:
			l.dropped++
		}
	}
}

// NewestAt is when the newest event the log holds was logged, zero while
// it holds none.
func (l *Log) NewestAt() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(time.Now())
	if l.n == 0 {
		return time.Time{}
	}
	return l.at(l.n - 1).Logged
}

// After copies the entries after seq into buf, oldest first, under the
// lock, and says whether newer ones remain: what the writer that keeps the
// log in files reads.
func (l *Log) After(seq uint64, buf []Entry) (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := sort.Search(l.n, func(k int) bool { return l.at(k).Seq > seq })
	n := 0
	for ; i < l.n && n < len(buf); i++ {
		buf[n] = *l.at(i)
		n++
	}
	return n, i < l.n
}

// Newest is the number of the newest entry added, zero before the first.
func (l *Log) Newest() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// Restore fills the log with entries read back from its files, oldest
// first, with the numbers they were given, keeping what its size and age
// allow. Numbering goes on after the higher of the last of them and
// newest, the highest number in the files, so none is given twice. It
// refuses once an entry has been added: the numbers would go backwards.
func (l *Log) Restore(entries []Entry, newest uint64) error {
	s, err := l.Restorer(len(entries))
	if err != nil {
		return err
	}
	for _, e := range entries {
		s.Push(e)
	}
	_, err = s.Done(newest)
	return err
}

var errTaken = errors.New("the WAF events have taken entries already")

// Restorer fills a ring of its own from the files, for Done to hand the log whole.
type Restorer struct {
	l     *Log
	ring  []Entry
	start int
	n     int
	size  int
	last  uint64
	lost  time.Time
}

// Restorer starts a read-back sized for count entries; it refuses once an entry is added.
func (l *Log) Restorer(count int) (*Restorer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seq > 0 {
		return nil, errTaken
	}
	return &Restorer{l: l, ring: make([]Entry, min(l.size, max(count, 0))), size: l.size}, nil
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
		if t := s.ring[s.start].Logged; t.After(s.lost) {
			s.lost = t
		}
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

// Done hands the log what came back, as Restore does, and says how many it keeps.
func (s *Restorer) Done(newest uint64) (int, error) {
	l := s.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seq > 0 {
		return 0, errTaken
	}
	l.seq = max(newest, s.last)
	l.let(s.lost)
	if s.n > 0 {
		l.ring, l.start, l.n = s.ring, s.start, s.n
		l.fit()
	}
	l.expire(time.Now())
	return l.n, nil
}

// put stores one entry, giving it the next place. The caller holds the
// lock.
func (l *Log) put(e Entry) Entry {
	l.seq++
	e.Seq = l.seq
	l.expire(time.Now())
	switch {
	case l.n < len(l.ring):
		l.ring[(l.start+l.n)%len(l.ring)] = e
		l.n++
	case len(l.ring) < l.size:
		l.grow()
		l.ring[l.n] = e
		l.n++
	default:
		l.let(l.ring[l.start].Logged)
		l.ring[l.start] = e
		l.start = (l.start + 1) % len(l.ring)
	}
	return e
}

// let notes that the log let go of an event logged at t. The caller holds
// the lock.
func (l *Log) let(t time.Time) {
	if t.After(l.lost) {
		l.lost = t
	}
}

// grow gives a full ring more places, doubling up to the ceiling, and puts
// the entries back in order from the front. The caller holds the lock.
func (l *Log) grow() {
	next := make([]Entry, min(l.size, max(2*len(l.ring), initialRing)))
	for i := range l.n {
		next[i] = l.ring[(l.start+i)%len(l.ring)]
	}
	l.ring, l.start = next, 0
}

// Recent returns up to limit entries, newest first.
func (l *Log) Recent(limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(time.Now())
	if limit <= 0 || limit > l.n {
		limit = l.n
	}
	out := make([]Entry, limit)
	for i := range limit {
		out[i] = l.ring[(l.start+l.n-1-i)%len(l.ring)]
	}
	return out
}

// at is the entry i places from the oldest. The caller holds the lock.
func (l *Log) at(i int) *Entry { return &l.ring[(l.start+i)%len(l.ring)] }

// Before implements logsearch.Log: the events older than seq, newest
// first, copied out under the lock so a search never holds up the reader.
func (l *Log) Before(seq uint64, buf []Entry) (int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(time.Now())
	i := l.n - 1
	if seq != 0 {
		i = sort.Search(l.n, func(k int) bool { return l.at(k).Seq >= seq }) - 1
	}
	n := 0
	for ; i >= 0 && n < len(buf); i-- {
		buf[n] = *l.at(i)
		n++
	}
	return n, i >= 0
}

// Between hands fn each event logged from from until until, and reports
// whether the log still holds every one: false once it has let go of an
// event from then, for its ceiling or its age. fn runs under the lock, so
// it only counts.
func (l *Log) Between(from, until time.Time, fn func(*Entry)) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(time.Now())
	for i := range l.n {
		if e := l.at(i); !e.Logged.Before(from) && e.Logged.Before(until) {
			fn(e)
		}
	}
	return l.lost.Before(from)
}

// Held is how many events the log holds and when the oldest of them was
// logged.
func (l *Log) Held() (int, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(time.Now())
	if l.n == 0 {
		return 0, time.Time{}
	}
	return l.n, l.ring[l.start].Logged
}

// Clear empties the log. The numbers carry on, so what arrives next is
// newer than anything a page read before, and what it held counts as let
// go, so a count reaching back over it is short.
func (l *Log) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n > 0 {
		l.let(l.at(l.n - 1).Logged)
	}
	l.ring, l.start, l.n = nil, 0, 0
}

// Subscribe returns a channel of new entries and a cancel function.
func (l *Log) Subscribe(n int) (<-chan Entry, func()) {
	ch := make(chan Entry, n)
	l.mu.Lock()
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
}

// Dropped counts entries subscribers were too slow to receive.
func (l *Log) Dropped() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dropped
}

// expire drops what has aged out. The ring is in the order events were
// logged, so this is the front of it. The caller holds the lock.
func (l *Log) expire(now time.Time) {
	if l.keep <= 0 {
		return
	}
	cutoff := now.Add(-l.keep)
	for l.n > 0 && l.ring[l.start].Logged.Before(cutoff) {
		l.let(l.ring[l.start].Logged)
		l.ring[l.start] = Entry{}
		l.start = (l.start + 1) % len(l.ring)
		l.n--
	}
}
