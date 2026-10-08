// Package logring is the log in memory the newer logs share: a ring in the
// order entries were logged, bounded by entries and by days, that grows
// towards its ceiling as entries arrive rather than being allocated whole,
// numbers each entry for good, and hands new ones to subscribers. It is
// what the journal feeds, what the log files write and read back, and what
// a page walks.
package logring

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"ostiole/internal/journalfeed"
	"ostiole/internal/logfile"
	"ostiole/internal/logsearch"
	"ostiole/internal/model"
)

// Stamp is what the ring keeps of every entry: its number, which only
// grows and outlasts a restart, and when it was logged. An entry type
// embeds it, so its line and its row carry both.
type Stamp struct {
	Seq  uint64    `json:"seq,omitempty"`
	Time time.Time `json:"time"`
}

// Stamped gives the ring the Stamp an entry embeds.
func (s *Stamp) Stamped() *Stamp { return s }

// Entry is what a ring of T needs of it: that a pointer to one gives its
// Stamp.
type Entry[T any] interface {
	*T
	Stamped() *Stamp
}

// initialRing is how many places a ring starts with; it doubles from there
// up to the ceiling as entries arrive.
const initialRing = 256

// Ring is a log in memory of T.
type Ring[T any, P Entry[T]] struct {
	mu   sync.Mutex
	size int
	keep time.Duration
	// ring holds the entries in the order they were logged from start.
	ring  []T
	start int
	n     int
	seq   uint64

	subs    map[chan T]struct{}
	dropped uint64
}

// New returns an empty ring of size entries at most, none older than keep;
// zero keep is no age.
func New[T any, P Entry[T]](size int, keep time.Duration) *Ring[T, P] {
	r := &Ring[T, P]{subs: map[chan T]struct{}{}}
	r.Configure(size, keep)
	return r
}

func stamp[T any, P Entry[T]](e *T) *Stamp { return P(e).Stamped() }

// Configure sets the ceiling and how long an entry is kept, keeping the
// newest entries that fit.
func (r *Ring[T, P]) Configure(size int, keep time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keep = keep
	r.expire(time.Now())
	size = max(size, 1)
	if r.size == size {
		return
	}
	r.size = size
	if len(r.ring) <= size {
		return
	}
	held := min(r.n, size)
	next := make([]T, min(size, max(held, initialRing)))
	for i := range held {
		next[i] = r.ring[(r.start+r.n-held+i)%len(r.ring)]
	}
	r.ring, r.start, r.n = next, 0, held
}

// Size is the ceiling the ring is holding to.
func (r *Ring[T, P]) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Add stores e, numbered next, logged now unless it says when, and hands
// it to subscribers without blocking: a slow one loses entries rather than
// holding up what feeds the log.
func (r *Ring[T, P]) Add(e T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := stamp[T, P](&e); s.Time.IsZero() {
		s.Time = time.Now()
	}
	r.send(r.put(e))
}

// AddAt stores e, logged at at: what the journal feeds.
func (r *Ring[T, P]) AddAt(at time.Time, e T) {
	stamp[T, P](&e).Time = at
	r.Add(e)
}

// FillAt stores what the journal held after the newest entry the ring had,
// given newest first.
func (r *Ring[T, P]) FillAt(items []journalfeed.Item[T]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(items) - 1; i >= 0; i-- {
		e := items[i].E
		stamp[T, P](&e).Time = items[i].At
		r.send(r.put(e))
	}
}

// put numbers and stores one entry. The caller holds the lock.
func (r *Ring[T, P]) put(e T) T {
	r.seq++
	stamp[T, P](&e).Seq = r.seq
	r.expire(time.Now())
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
	return e
}

// send hands e to each subscriber that has room. The caller holds the
// lock.
func (r *Ring[T, P]) send(e T) {
	for ch := range r.subs {
		select {
		case ch <- e:
		default:
			r.dropped++
		}
	}
}

// grow gives a full ring more places, doubling up to the ceiling, and puts
// the entries back in order from the front. The caller holds the lock.
func (r *Ring[T, P]) grow() {
	next := make([]T, min(r.size, max(2*len(r.ring), initialRing)))
	for i := range r.n {
		next[i] = r.ring[(r.start+i)%len(r.ring)]
	}
	r.ring, r.start = next, 0
}

// at is the entry i places from the oldest. The caller holds the lock.
func (r *Ring[T, P]) at(i int) *T { return &r.ring[(r.start+i)%len(r.ring)] }

// NewestAt is when the newest entry was logged, zero while there is none.
func (r *Ring[T, P]) NewestAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(time.Now())
	if r.n == 0 {
		return time.Time{}
	}
	return stamp[T, P](r.at(r.n - 1)).Time
}

// Newest is the number of the newest entry taken, zero before the first.
func (r *Ring[T, P]) Newest() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// Held is how many entries the ring holds and when the oldest was logged.
func (r *Ring[T, P]) Held() (int, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(time.Now())
	if r.n == 0 {
		return 0, time.Time{}
	}
	return r.n, stamp[T, P](r.at(0)).Time
}

// Recent returns up to limit entries, newest first; zero is all of them.
func (r *Ring[T, P]) Recent(limit int) []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(time.Now())
	if limit <= 0 || limit > r.n {
		limit = r.n
	}
	out := make([]T, limit)
	for i := range limit {
		out[i] = *r.at(r.n - 1 - i)
	}
	return out
}

// Before implements logsearch.Log: the entries older than seq, newest
// first, copied out under the lock so a search never holds up the log.
func (r *Ring[T, P]) Before(seq uint64, buf []T) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(time.Now())
	i := r.n - 1
	if seq != 0 {
		i = sort.Search(r.n, func(k int) bool { return stamp[T, P](r.at(k)).Seq >= seq }) - 1
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
func (r *Ring[T, P]) After(seq uint64, buf []T) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := sort.Search(r.n, func(k int) bool { return stamp[T, P](r.at(k)).Seq > seq })
	n := 0
	for ; i < r.n && n < len(buf); i++ {
		buf[n] = *r.at(i)
		n++
	}
	return n, i < r.n
}

// Restore fills the ring with entries read back from its files, oldest
// first, with the numbers they were given, keeping what its size and age
// allow. Numbering goes on after the higher of the last of them and
// newest, the highest number in the files, so none is given twice. It
// refuses once an entry has been added: the numbers would go backwards.
func (r *Ring[T, P]) Restore(entries []T, newest uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seq > 0 {
		return errors.New("the log has taken entries already")
	}
	entries = logfile.Ascending(entries, func(e *T) uint64 { return stamp[T, P](e).Seq })
	if len(entries) > r.size {
		entries = entries[len(entries)-r.size:]
	}
	r.seq = newest
	if len(entries) > 0 {
		r.seq = max(r.seq, stamp[T, P](&entries[len(entries)-1]).Seq)
		// The slice becomes the ring, so a big log is not held twice.
		r.ring, r.start, r.n = entries[:len(entries):len(entries)], 0, len(entries)
	}
	r.expire(time.Now())
	return nil
}

// Clear empties the ring. The numbers carry on, so what arrives next is
// newer than anything a page read before.
func (r *Ring[T, P]) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ring, r.start, r.n = nil, 0, 0
}

// Subscribe returns a channel of new entries and a cancel function.
func (r *Ring[T, P]) Subscribe(buffer int) (<-chan T, func()) {
	ch := make(chan T, buffer)
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
func (r *Ring[T, P]) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// expire drops what has aged out. The ring is in the order entries were
// logged, so this is the front of it. The caller holds the lock.
func (r *Ring[T, P]) expire(now time.Time) {
	if r.keep <= 0 {
		return
	}
	cutoff := now.Add(-r.keep)
	for r.n > 0 && stamp[T, P](&r.ring[r.start]).Time.Before(cutoff) {
		var zero T
		r.ring[r.start] = zero
		r.start = (r.start + 1) % len(r.ring)
		r.n--
	}
}

// Query reads the ring newest first from the entry before: those match
// accepts, a page of limit at most, within the search budget.
func (r *Ring[T, P]) Query(ctx context.Context, before uint64, limit int, match func(*T) bool) (logsearch.Page[T], error) {
	return logsearch.Walk(ctx, r, before, limit, logsearch.DefaultBudget, Seq[T, P], At[T, P], match)
}

// Seq is an entry's number.
func Seq[T any, P Entry[T]](e *T) uint64 { return stamp[T, P](e).Seq }

// At is when an entry was logged.
func At[T any, P Entry[T]](e *T) time.Time { return stamp[T, P](e).Time }

// Files describes the ring to the writer that keeps it in files, under
// name in format version: an entry's line is its JSON, number included.
// on says whether the log is kept in a configuration, and days how many
// days it keeps there.
func (r *Ring[T, P]) Files(name string, version int, on func(*model.Config) bool, days func(*model.Config) int) logfile.Log {
	return logfile.Log{
		Name: name, Version: version, On: on, Days: days,
		Newest: r.Newest, Size: r.Size,
		Lines: logfile.Lines(r.After, Seq[T, P], At[T, P], func() func([]byte, *T) []byte {
			return func(buf []byte, e *T) []byte {
				raw, err := json.Marshal(e)
				if err != nil {
					return buf
				}
				return append(buf, raw...)
			}
		}),
	}
}

// Parse reads a line of a ring's files.
func Parse[T any, P Entry[T]](line []byte) (T, time.Time, error) {
	var e T
	if err := json.Unmarshal(line, &e); err != nil {
		var zero T
		return zero, time.Time{}, err
	}
	return e, stamp[T, P](&e).Time, nil
}
