package server

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/services"
)

// namerTTL is how long a namer is kept. The leases file and the neighbour
// table are read for it, and a page of two hundred rows must not read them
// two hundred times.
const namerTTL = 10 * time.Second

// namer puts a name to a client address the way the leases page would: a
// static lease's hostname or description, a dynamic lease's hostname, a
// host override, and for an address with no lease (SLAAC, mostly) the
// lease that shares its neighbour's hardware address.
//
// Names are put on rows as they are read, never stored with them: a
// renamed device shows its new name on old rows, and a query log entry
// stays small enough to keep two hundred thousand of.
type namer struct {
	byAddr map[netip.Addr]string
	byMAC  map[string]string
	// neigh is the hardware address the kernel has for an address, which
	// is how an address with no lease is tied to one that has.
	neigh map[netip.Addr]string
}

// namers caches the last one built.
type namers struct {
	mu sync.Mutex
	at time.Time
	n  *namer
}

// namer builds one from the leases, the configuration and the neighbour
// table, or returns the one built in the last ten seconds.
func (a *api) namer() *namer {
	a.devices.mu.Lock()
	defer a.devices.mu.Unlock()
	if a.devices.n != nil && time.Since(a.devices.at) < namerTTL {
		return a.devices.n
	}
	n := &namer{
		byAddr: map[netip.Addr]string{},
		byMAC:  map[string]string{},
		neigh:  map[netip.Addr]string{},
	}
	// Least specific first: a static lease's own name beats a dynamic
	// one, and both beat a host override pointing at the same address.
	cfg := (*model.Config)(nil)
	if a.engine != nil {
		cfg = a.engine.Effective()
	}
	if cfg != nil {
		local := model.NormalizeDomain(cfg.Services.DNS.Domain)
		for _, h := range cfg.Services.DNS.HostOverrides {
			n.put(h.IP, deviceName(h, local))
		}
		for _, l := range cfg.Services.DHCP.StaticLeases {
			name := l.Hostname
			if name == "" {
				name = l.Description
			}
			if name == "" {
				continue
			}
			if mac := strings.ToLower(strings.TrimSpace(l.MAC)); mac != "" {
				n.byMAC[mac] = name
			}
		}
		for _, h := range services.SystemHosts(cfg) {
			n.put(h.IP, h.Hostname)
		}
	}
	if a.services != nil {
		if leases, err := a.services.ReadLeases(); err == nil {
			for _, l := range leases {
				if l.Hostname == "" {
					continue
				}
				n.put(l.IP, l.Hostname)
				if mac := strings.ToLower(l.MAC); mac != "" {
					if _, ok := n.byMAC[mac]; !ok {
						n.byMAC[mac] = l.Hostname
					}
				}
			}
		}
	}
	if len(n.byMAC) > 0 {
		if list, err := diag.Neighbours(); err == nil {
			for _, nb := range list {
				addr, err := netip.ParseAddr(nb.Address)
				if err != nil || nb.MAC == "" {
					continue
				}
				n.neigh[addr.Unmap()] = strings.ToLower(nb.MAC)
			}
		}
	}
	a.devices.n, a.devices.at = n, time.Now()
	return n
}

// deviceName is what a host override calls its address: the bare label
// in the local domain, as a lease's name is, and the full name anywhere
// else, since bare it resolves to nothing.
func deviceName(h model.HostOverride, local string) string {
	if h.Local(local) {
		return h.Hostname
	}
	return h.FQDN(local)
}

func (n *namer) put(raw, name string) {
	addr, err := netip.ParseAddr(raw)
	if err != nil || name == "" {
		return
	}
	n.byAddr[addr.Unmap()] = name
}

// Name is what to call the device at addr, or empty when nothing knows.
func (n *namer) Name(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	addr = addr.Unmap()
	if name, ok := n.byAddr[addr]; ok {
		return name
	}
	// A SLAAC address has no lease of its own, but the card behind it
	// usually has one for its IPv4 address.
	if mac, ok := n.neigh[addr]; ok {
		return n.byMAC[mac]
	}
	return ""
}

// Match is the addresses a search means: the one it names exactly, or
// every one whose device name contains it. An address nothing knows a
// name for still matches itself, so a search by address works before a
// lease exists.
func (n *namer) Match(q string) []netip.Addr {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil
	}
	if addr, err := netip.ParseAddr(q); err == nil {
		return []netip.Addr{addr.Unmap()}
	}
	want := strings.ToLower(q)
	seen := map[netip.Addr]bool{}
	var out []netip.Addr
	add := func(addr netip.Addr, name string) {
		if name == "" || seen[addr] || !strings.Contains(strings.ToLower(name), want) {
			return
		}
		seen[addr] = true
		out = append(out, addr)
	}
	for addr, name := range n.byAddr {
		add(addr, name)
	}
	for addr, mac := range n.neigh {
		add(addr, n.byMAC[mac])
	}
	return out
}
