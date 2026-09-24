package dnslog

import (
	"log/slog"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/panics"
)

// MaxLists is how many list names can be attributed to. Eight bytes an
// entry holds sixty-four, which is far past what anybody subscribes to.
const MaxLists = 64

// Log keeps what the resolver answered, in a ring bounded by size and by
// age. Everything is in memory: switching it off, clearing it or
// restarting the daemon leaves nothing behind, and nothing here is written
// to a backup, a revision or the journal.
type Log struct {
	// Slog reports a build that failed and a list too many.
	Slog *slog.Logger

	// idx is read on the packet path, so it is swapped rather than locked.
	idx atomic.Pointer[Index]

	mu   sync.Mutex
	on   bool
	size int
	keep time.Duration
	// ring holds the entries in time order from start. It is not allocated
	// whole: it doubles towards size as answers arrive, so a ceiling of a
	// million costs nothing on a router that has answered a thousand.
	ring  []Entry
	start int
	n     int
	seq   uint64
	// total and blocked are kept as answers come and go, so the overview
	// can be polled without walking a million entries under the lock the
	// listener needs.
	total   int
	blocked int
	// since is when the log was switched on or last cleared, which is what
	// the list counts are counted from.
	since time.Time
	// names interns a list name to a bit; warned records the one complaint
	// about a sixty-fifth.
	names  []string
	bits   map[string]int
	warned bool
	counts map[string]ListCount

	subs    map[chan Entry]struct{}
	dropped uint64

	// building is a rebuild in flight; want is the options it should use
	// when it comes round again, so a second apply during a build is not a
	// second build.
	building bool
	want     *dnsblock.Options
	wantFrom *dnsblock.Cache
	// tried is what the last build attempt was for, and when. A build that
	// fails leaves no index, and the watcher comes round every few
	// seconds, so without this a cache that cannot be read would be
	// re-read for ever.
	tried   dnsblock.Options
	triedAt time.Time
}

// RetryIndex is how long to leave a failed index build before trying it
// again with the same inputs.
const RetryIndex = time.Minute

// initialRing is how many places a ring starts with; it doubles from there
// up to the ceiling as answers arrive.
const initialRing = 1024

// ListCount is how often a list carried a blocked name, and how often it
// was the only one that did: what would stop being blocked without it.
type ListCount struct {
	Blocked int `json:"blocked"`
	Alone   int `json:"alone"`
}

// Filter narrows what Query returns.
type Filter struct {
	Name    string       // lowercase substring
	Clients []netip.Addr // any of; empty is every client
	Status  Status       // zero is every status
	List    string       // blocked entries this list carries
	Type    uint16       // zero is every type
	Since   time.Time
	Before  uint64 // seq, exclusive: the page after this one
	Limit   int
}

// Summary is the line above the table: what is held, and who asked for it.
type Summary struct {
	Since      time.Time     `json:"since"`
	Total      int           `json:"total"`
	Blocked    int           `json:"blocked"`
	Clients    int           `json:"clients"`
	Dropped    uint64        `json:"dropped"`
	TopNames   []NameCount   `json:"topNames"`
	TopBlocked []NameCount   `json:"topBlocked"`
	TopClients []ClientCount `json:"topClients"`
}

// NameCount is one name and how often it was asked for.
type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ClientCount is one client, how much it asked and how much was refused.
type ClientCount struct {
	Client  netip.Addr `json:"client"`
	Total   int        `json:"total"`
	Blocked int        `json:"blocked"`
}

// New returns a log that is off.
func New() *Log {
	return &Log{
		bits:   map[string]int{},
		counts: map[string]ListCount{},
		subs:   map[chan Entry]struct{}{},
	}
}

// index is Build in the background: the lists come from the internet, so
// a panic reading one fails the index rather than the daemon.
func (l *Log) index(o dnsblock.Options, c *dnsblock.Cache) (x *Index, err error) {
	defer panics.Into(&err, l.log(), "query log index")
	return Build(o, c)
}

func (l *Log) log() *slog.Logger {
	if l.Slog != nil {
		return l.Slog
	}
	return slog.Default()
}

// Configure sizes the ring and the retention, clears everything when the
// log is switched off, and rebuilds the index when the lists or the
// exceptions changed.
func (l *Log) Configure(q model.QueryLog, o dnsblock.Options, c *dnsblock.Cache) {
	l.mu.Lock()
	if !q.Enabled {
		wasOn := l.on
		l.on = false
		l.reset()
		// The index goes with it, so switching back on builds a fresh one
		// rather than waiting out the retry.
		l.tried, l.triedAt = dnsblock.Options{}, time.Time{}
		l.mu.Unlock()
		if wasOn {
			l.idx.Store(nil)
			l.log().Info("query log switched off; what it held is gone")
		}
		return
	}
	if !l.on {
		l.on = true
		l.since = time.Now()
	}
	l.keep = q.Retention()
	l.resize(q.Size())
	stale := l.stale(o)
	l.mu.Unlock()

	if stale {
		l.Reindex(o, c)
	}
}

// stale reports whether the index has to be built for these inputs: it has
// never been built for them, or the last build failed and long enough has
// passed to try again. The caller holds the lock.
func (l *Log) stale(o dnsblock.Options) bool {
	if l.triedAt.IsZero() || !sameIndexInputs(l.tried, o) {
		return true
	}
	return l.idx.Load() == nil && time.Since(l.triedAt) >= RetryIndex
}

// sameIndexInputs compares what the index is built from. The ceiling on
// merged names is not one of them: it decides what the resolver is given,
// not what the lists hold.
func sameIndexInputs(a, b dnsblock.Options) bool {
	a.Max, b.Max = 0, 0
	return reflect.DeepEqual(a, b)
}

// Reindex builds a new index in the background and swaps it in. A build
// that fails is logged and the index that is in place stays; a build asked
// for while one is running is folded into it.
func (l *Log) Reindex(o dnsblock.Options, c *dnsblock.Cache) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.on {
		return
	}
	l.want, l.wantFrom = &o, c
	if l.building {
		return
	}
	l.building = true
	go l.build()
}

func (l *Log) build() {
	for {
		l.mu.Lock()
		o, c := l.want, l.wantFrom
		l.want, l.wantFrom = nil, nil
		if o == nil || !l.on {
			l.building = false
			l.mu.Unlock()
			return
		}
		l.tried, l.triedAt = *o, time.Now()
		l.mu.Unlock()

		started := time.Now()
		x, err := l.index(*o, c)
		if err != nil {
			l.log().Warn("could not index the blocklists for the query log; blocked answers keep the attribution they had", "err", err)
			continue
		}
		// A log switched off while this was building keeps nothing, the
		// index included: switching it on again builds a fresh one.
		l.mu.Lock()
		on := l.on
		l.mu.Unlock()
		if !on {
			continue
		}
		l.idx.Store(x)
		l.log().Info("query log index built", "names", x.Names(), "lists", len(o.Lists), "took", time.Since(started))
	}
}

// Index is what the listener classifies against; nil while the first build
// is still running.
func (l *Log) Index() *Index { return l.idx.Load() }

// Enabled reports whether answers are being kept.
func (l *Log) Enabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.on
}

// Since is when the log was switched on or last cleared.
func (l *Log) Since() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.since
}

// Add stores e, interns the lists that carried its name, evicts by size
// and by age, counts, and fans out to subscribers without blocking.
// Nothing is kept while the log is off, whatever the kernel sends.
func (l *Log) Add(e Entry, lists []string) {
	// Deferred: the listener recovers a panic here and carries on, which
	// a lock left held would turn into a hang.
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.on || l.size == 0 {
		return
	}
	l.seq++
	e.Seq = l.seq
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Lists = l.intern(lists)
	l.expire(e.Time)
	switch {
	case l.n < len(l.ring):
		l.ring[(l.start+l.n)%len(l.ring)] = e
		l.n++
	case len(l.ring) < l.size:
		l.grow()
		l.ring[l.n] = e
		l.n++
	default:
		l.uncount(l.ring[l.start])
		l.ring[l.start] = e
		l.start = (l.start + 1) % len(l.ring)
	}
	l.count(e)
	if e.Status == StatusBlocked {
		alone := e.Lists != 0 && e.Lists&(e.Lists-1) == 0
		for i, name := range l.names {
			if e.Lists&(1<<uint(i)) == 0 {
				continue
			}
			c := l.counts[name]
			c.Blocked++
			if alone {
				c.Alone++
			}
			l.counts[name] = c
		}
	}
	for ch := range l.subs {
		select {
		case ch <- e:
		default:
			l.dropped++
		}
	}
}

// intern turns list names into bits, adding names it has not seen. The
// table holds sixty-four; a name past that is not attributed, and is
// complained about once rather than on every packet.
func (l *Log) intern(lists []string) uint64 {
	var mask uint64
	for _, name := range lists {
		i, ok := l.bits[name]
		if !ok {
			if len(l.names) >= MaxLists {
				if !l.warned {
					l.warned = true
					l.log().Warn("more than 64 block lists, so the query log cannot say which one blocked a name", "list", name)
				}
				continue
			}
			i = len(l.names)
			l.names = append(l.names, name)
			l.bits[name] = i
		}
		mask |= 1 << uint(i)
	}
	return mask
}

// ListNames is the list names a mask stands for.
func (l *Log) ListNames(mask uint64) []string {
	if mask == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for i, name := range l.names {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, name)
		}
	}
	return out
}

// Totals is what the dashboard shows: how much is held, how much of it was
// refused, and how old the oldest answer is. The counts are kept as
// answers come and go rather than walked for, so a dashboard that polls
// every few seconds does not hold up the listener.
func (l *Log) Totals() (total, blocked int, oldest time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// A quiet log ages out here rather than on the next answer.
	l.expire(time.Now())
	if l.n > 0 {
		oldest = l.ring[l.start].Time
	}
	return l.total, l.blocked, oldest
}

// ListCounts is how many blocked answers each list carried since the log
// was switched on or last cleared, and when that was. It outlives eviction
// from the ring.
func (l *Log) ListCounts() (map[string]ListCount, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]ListCount, len(l.counts))
	for k, v := range l.counts {
		out[k] = v
	}
	return out, l.since
}

// Query returns the matches newest first, and how many matched in all.
func (l *Log) Query(f Filter) ([]Entry, int) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	name := strings.ToLower(strings.TrimSpace(f.Name))

	l.mu.Lock()
	defer l.mu.Unlock()
	var wanted uint64
	if f.List != "" {
		i, ok := l.bits[f.List]
		if !ok {
			return []Entry{}, 0
		}
		wanted = 1 << uint(i)
	}
	cutoff := l.cutoff(time.Now())
	out := make([]Entry, 0, min(limit, l.n))
	total := 0
	for i := l.n - 1; i >= 0; i-- {
		e := l.ring[(l.start+i)%len(l.ring)]
		if e.Time.Before(cutoff) {
			break // older still, and the ring is in order
		}
		if !matches(e, f, name, wanted) {
			continue
		}
		total++
		if f.Before != 0 && e.Seq >= f.Before {
			continue
		}
		if len(out) < limit {
			out = append(out, e)
		}
	}
	return out, total
}

func matches(e Entry, f Filter, name string, wanted uint64) bool {
	switch {
	case name != "" && !strings.Contains(e.Name, name):
		return false
	case f.Status != StatusNone && e.Status != f.Status:
		return false
	case f.Type != 0 && e.Type != f.Type:
		return false
	case wanted != 0 && (e.Status != StatusBlocked || e.Lists&wanted == 0):
		return false
	case !f.Since.IsZero() && e.Time.Before(f.Since):
		return false
	}
	if len(f.Clients) > 0 {
		var hit bool
		for _, c := range f.Clients {
			if c == e.Client {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// Summary describes what is held: totals, and the busiest names and
// clients. top is how many of each to name.
func (l *Log) Summary(top int) Summary {
	if top <= 0 {
		top = 10
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s := Summary{Since: l.since, Dropped: l.dropped, TopNames: []NameCount{}, TopBlocked: []NameCount{}, TopClients: []ClientCount{}}
	cutoff := l.cutoff(time.Now())
	names := map[string]int{}
	blocked := map[string]int{}
	clients := map[netip.Addr]*ClientCount{}
	for i := range l.n {
		e := l.ring[(l.start+i)%len(l.ring)]
		if e.Time.Before(cutoff) {
			continue
		}
		if s.Total == 0 && !e.Time.IsZero() {
			// The oldest answer still held is what "since" means to a
			// reader looking at a ring that has already turned over.
			s.Since = e.Time
		}
		s.Total++
		names[e.Name]++
		c := clients[e.Client]
		if c == nil {
			c = &ClientCount{Client: e.Client}
			clients[e.Client] = c
		}
		c.Total++
		if e.Status == StatusBlocked {
			s.Blocked++
			blocked[e.Name]++
			c.Blocked++
		}
	}
	s.Clients = len(clients)
	s.TopNames = topNames(names, top)
	s.TopBlocked = topNames(blocked, top)
	for _, c := range clients {
		s.TopClients = append(s.TopClients, *c)
	}
	sort.Slice(s.TopClients, func(i, j int) bool {
		if s.TopClients[i].Total != s.TopClients[j].Total {
			return s.TopClients[i].Total > s.TopClients[j].Total
		}
		return s.TopClients[i].Client.Less(s.TopClients[j].Client)
	})
	if len(s.TopClients) > top {
		s.TopClients = s.TopClients[:top]
	}
	return s
}

func topNames(counts map[string]int, top int) []NameCount {
	out := make([]NameCount, 0, len(counts))
	for name, n := range counts {
		out = append(out, NameCount{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > top {
		out = out[:top]
	}
	return out
}

// Clear drops every entry and every count. The log stays on, and stays
// the size it was configured to be.
func (l *Log) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	size := l.size
	l.reset()
	if l.on {
		l.resize(size)
		l.since = time.Now()
	}
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

// reset empties the ring, the counts and the intern table. The caller
// holds the lock.
func (l *Log) reset() {
	l.ring, l.start, l.n = nil, 0, 0
	l.size = 0
	l.total, l.blocked = 0, 0
	l.names, l.bits, l.warned = nil, map[string]int{}, false
	l.counts = map[string]ListCount{}
	l.since = time.Time{}
	l.dropped = 0
}

// resize sets the ceiling to n and keeps the newest entries that fit. A
// ring that has room to grow is left alone: it grows as answers arrive. The
// caller holds the lock.
func (l *Log) resize(n int) {
	if n < 1 {
		n = 1
	}
	if l.size == n && l.ring != nil {
		return
	}
	l.size = n
	if l.ring != nil && len(l.ring) <= n {
		return
	}
	held := min(l.n, n)
	for i := range l.n - held {
		l.uncount(l.ring[(l.start+i)%len(l.ring)])
	}
	next := make([]Entry, min(n, max(held, initialRing)))
	for i := range held {
		next[i] = l.ring[(l.start+l.n-held+i)%len(l.ring)]
	}
	l.ring, l.start, l.n = next, 0, held
}

// grow gives a full ring more places, doubling up to the ceiling, and puts
// the entries back in order from the front. On the way to a million
// entries it runs ten times; allocating the million up front would cost
// its memory the moment the log was switched on. The caller holds the lock.
func (l *Log) grow() {
	next := make([]Entry, min(l.size, max(2*len(l.ring), initialRing)))
	for i := range l.n {
		next[i] = l.ring[(l.start+i)%len(l.ring)]
	}
	l.ring, l.start = next, 0
}

func (l *Log) count(e Entry) {
	l.total++
	if e.Status == StatusBlocked {
		l.blocked++
	}
}

func (l *Log) uncount(e Entry) {
	l.total--
	if e.Status == StatusBlocked {
		l.blocked--
	}
}

// cutoff is the oldest time still kept. The caller holds the lock.
func (l *Log) cutoff(now time.Time) time.Time {
	if l.keep <= 0 {
		return time.Time{}
	}
	return now.Add(-l.keep)
}

// expire drops what has aged out. The ring is in time order, so this is
// the front of it. The caller holds the lock.
func (l *Log) expire(now time.Time) {
	cutoff := l.cutoff(now)
	for l.n > 0 && l.ring[l.start].Time.Before(cutoff) {
		l.uncount(l.ring[l.start])
		l.ring[l.start] = Entry{}
		l.start = (l.start + 1) % len(l.ring)
		l.n--
	}
}
