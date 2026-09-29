package traffic

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// How long an answer names an address, and how many are kept.
const (
	namesKept = 24 * time.Hour
	maxNames  = 100_000
)

// Names ties an address a client was answered to the name it asked for,
// so a destination reads as example.com rather than its address. The
// router's DNS answers feed it, while destinations are recorded: every A
// and AAAA of an answer, the question's name for all of them however many
// CNAMEs led there. A client that asked another resolver, or over HTTPS
// or TLS, shows addresses only; nothing is looked up in reverse, since
// that would send the addresses to the upstream resolver.
type Names struct {
	wanted atomic.Bool
	mu     sync.Mutex
	m      map[nameKey]nameSeen
	// queue holds the keys in the order they were last answered, with
	// stale copies skipped when their turn comes.
	queue []queued
}

type nameKey struct{ client, addr netip.Addr }

type nameSeen struct {
	name string
	at   time.Time
}

type queued struct {
	key nameKey
	at  time.Time
}

// Wanted reports whether answers are kept, which the DNS listener asks
// before it reads one for this.
func (n *Names) Wanted() bool { return n.wanted.Load() }

// set switches keeping answers on or off; off forgets them.
func (n *Names) set(on bool) {
	if n.wanted.Swap(on) == on {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.m, n.queue = nil, nil
}

// Answered notes the addresses a client was given for a name.
func (n *Names) Answered(client netip.Addr, name string, addrs []netip.Addr, at time.Time) {
	if !n.Wanted() || name == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.m == nil {
		n.m = map[nameKey]nameSeen{}
	}
	for _, a := range addrs {
		k := nameKey{client.Unmap(), a.Unmap()}
		n.m[k] = nameSeen{name: name, at: at}
		n.queue = append(n.queue, queued{key: k, at: at})
	}
	n.prune(at)
}

// lookup is the name a client asked for that got it addr, or "".
func (n *Names) lookup(client, addr netip.Addr) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.m[nameKey{client.Unmap(), addr.Unmap()}].name
}

// prune lets go of answers older than a day, and of the oldest past the
// bound. The caller holds n.mu.
func (n *Names) prune(now time.Time) {
	cut := now.Add(-namesKept)
	drop := 0
	for drop < len(n.queue) {
		q := n.queue[drop]
		seen, ok := n.m[q.key]
		switch {
		case !ok || !seen.at.Equal(q.at):
			// Answered again since: a later copy is in the queue.
		case seen.at.Before(cut) || len(n.m) > maxNames:
			delete(n.m, q.key)
		default:
			n.queue = n.queue[drop:]
			if cap(n.queue) > 4*len(n.queue)+1024 {
				n.queue = append([]queued(nil), n.queue...)
			}
			return
		}
		drop++
	}
	n.queue = n.queue[:0]
}
