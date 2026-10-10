package discovery

import (
	"crypto/sha256"
	"net/netip"
	"time"
)

const (
	slotCount    = 100
	slotLife     = 2 * time.Second
	dedupeWindow = 100 * time.Millisecond
	linkCap      = 1000
	overrunFor   = time.Minute
)

// slot holds a legacy query's client while its answers come back.
type slot struct {
	used    bool
	id      uint16
	client  netip.AddrPort
	link    string
	expires time.Time
}

// slotTable is the legacy query table of one reply socket; a slot's number is the ID sent.
type slotTable struct {
	slots [slotCount]slot
	next  uint16
}

// take gives a free slot to a query, false when every slot is in use.
func (t *slotTable) take(id uint16, client netip.AddrPort, link string, now time.Time) (uint16, bool) {
	for i := range uint16(slotCount) {
		n := (t.next + i) % slotCount
		s := &t.slots[n]
		if s.used && now.Before(s.expires) {
			continue
		}
		*s = slot{used: true, id: id, client: client, link: link, expires: now.Add(slotLife)}
		t.next = (n + 1) % slotCount
		return n, true
	}
	return 0, false
}

// lookup finds the slot a reply's ID names. It stays for further answers until it expires.
func (t *slotTable) lookup(n uint16, now time.Time) (slot, bool) {
	if int(n) >= slotCount {
		return slot{}, false
	}
	s := t.slots[n]
	if !s.used || !now.Before(s.expires) {
		return slot{}, false
	}
	return s, true
}

func (t *slotTable) sweep(now time.Time) {
	for i := range t.slots {
		if t.slots[i].used && !now.Before(t.slots[i].expires) {
			t.slots[i] = slot{}
		}
	}
}

// proxy searches for one SSDP client from a port of its own.
type proxy struct {
	port    int
	client  netip.AddrPort
	link    string
	expires time.Time
	conn    conn
}

// proxyTable holds the search proxies by client.
type proxyTable struct {
	first, last int
	next        int
	byClient    map[netip.AddrPort]*proxy
}

func newProxyTable(first, last int) *proxyTable {
	return &proxyTable{first: first, last: last, next: first, byClient: map[netip.AddrPort]*proxy{}}
}

// free lists the ports not in use, starting after the one handed out last.
func (t *proxyTable) free() []int {
	if t.first <= 0 || t.last < t.first {
		return nil
	}
	used := map[int]bool{}
	for _, p := range t.byClient {
		used[p.port] = true
	}
	n := t.last - t.first + 1
	var out []int
	for i := range n {
		port := t.first + (t.next-t.first+i)%n
		if !used[port] {
			out = append(out, port)
		}
	}
	return out
}

func (t *proxyTable) add(p *proxy) {
	t.byClient[p.client] = p
	t.next = p.port + 1
	if t.next > t.last {
		t.next = t.first
	}
}

// expired removes and returns the proxies past their time.
func (t *proxyTable) expired(now time.Time) []*proxy {
	var out []*proxy
	for k, p := range t.byClient {
		if !now.Before(p.expires) {
			out = append(out, p)
			delete(t.byClient, k)
		}
	}
	return out
}

// searchLife is how long a proxy stays after a search: MX + 1 s, MX counted 1 to 5.
func searchLife(mx int) time.Duration {
	switch {
	case mx <= 0:
		mx = 1
	case mx > 5:
		mx = 5
	}
	return time.Duration(mx+1) * time.Second
}

type dedupeKey struct {
	sum     [sha256.Size]byte
	ifindex int
}

// dedupe remembers payloads per interface for dedupeWindow.
type dedupe map[dedupeKey]time.Time

// seen reports whether the payload came on ifindex within the window, and remembers it if not.
func (d dedupe) seen(payload []byte, ifindex int, now time.Time) bool {
	k := dedupeKey{sha256.Sum256(payload), ifindex}
	if at, ok := d[k]; ok && now.Sub(at) < dedupeWindow {
		return true
	}
	d[k] = now
	return false
}

func (d dedupe) mark(payload []byte, ifindex int, now time.Time) {
	d[dedupeKey{sha256.Sum256(payload), ifindex}] = now
}

func (d dedupe) sweep(now time.Time) {
	for k, at := range d {
		if now.Sub(at) >= dedupeWindow {
			delete(d, k)
		}
	}
}

// bucket is a token bucket of linkCap a second, burst linkCap.
type bucket struct {
	tokens float64
	last   time.Time
}

func newBucket(now time.Time) *bucket { return &bucket{tokens: linkCap, last: now} }

func (b *bucket) take(now time.Time) bool {
	if d := now.Sub(b.last); d > 0 {
		b.tokens = min(linkCap, b.tokens+d.Seconds()*linkCap)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
