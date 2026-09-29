package server

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/services"
)

// lease is a DHCP lease with what dnsmasq's file leaves out, from the
// configuration in force and the kernel.
type lease struct {
	services.Lease
	// Interface is where the lease was handed out: the one whose network
	// holds an IPv4 address, or whose prefix holds an IPv6 one.
	Interface string `json:"interface,omitempty"`
	// Static is set when a static lease pins this MAC to this address.
	Static bool `json:"static"`
	// Description is the static lease's for this MAC.
	Description string `json:"description,omitempty"`
	// Renewed is when the lease was last handed out or renewed: its
	// expiry less its pool's lease time, as the file keeps only the expiry.
	Renewed time.Time `json:"renewed,omitzero"`
	// Seen is when the client last answered, from the neighbour table, and
	// Online says that was within onlineWithin.
	Seen   time.Time `json:"seen,omitzero"`
	Online bool      `json:"online,omitempty"`
}

// onlineWithin is how recently a client must have answered to count as
// online. The router asks only when it has traffic for a client, so one in
// use answers every minute or so.
const onlineWithin = 10 * time.Minute

func (a *api) dhcpLeases(w http.ResponseWriter, _ *http.Request) error {
	links, _ := network.Discover()
	rows, err := a.leases(links)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rows)
	return nil
}

// leases reads the lease file and fills in each row.
func (a *api) leases(links []network.Link) ([]lease, error) {
	if a.services == nil {
		return []lease{}, nil
	}
	file, err := a.services.ReadLeases()
	if err != nil {
		return nil, err
	}
	var cfg *model.Config
	if a.engine != nil {
		cfg = a.engine.Effective()
	}
	neighbours, _ := diag.Neighbours()
	return leaseRows(cfg, file, links, neighbours, time.Now()), nil
}

// leaseRows fills in the rows for the leases in the file. The config may
// be nil, and then only the neighbour table has anything to add.
func leaseRows(cfg *model.Config, file []services.Lease, links []network.Link, neighbours []diag.Neighbour, now time.Time) []lease {
	// How long each interface's pool leases for, by family. An infinite
	// pool is left out: its leases have no expiry to count back from.
	lengths := map[int]map[string]time.Duration{4: {}, 6: {}}
	statics := map[string]model.StaticLease{}
	var prefixes []namedPrefix
	if cfg != nil {
		for _, sc := range cfg.ActiveDHCP() {
			if d, ok := model.LeaseDuration(sc.LeaseTime); ok {
				lengths[4][sc.Interface] = d
			}
		}
		served := map[string]bool{}
		for _, sc := range cfg.ActiveDHCPv6() {
			served[sc.Interface] = true
			if d, ok := model.LeaseDuration(sc.LeaseTime); ok {
				lengths[6][sc.Interface] = d
			}
		}
		prefixes = v6Prefixes(links, served)
		for _, s := range cfg.Services.DHCP.StaticLeases {
			statics[strings.ToLower(s.MAC)] = s
		}
	}
	heard := map[netip.Addr][]diag.Neighbour{}
	for _, n := range neighbours {
		if ip, err := netip.ParseAddr(n.Address); err == nil && !n.Seen.IsZero() {
			heard[ip] = append(heard[ip], n)
		}
	}

	out := make([]lease, 0, len(file))
	for _, l := range file {
		row := lease{Lease: l}
		ip, err := netip.ParseAddr(l.IP)
		if err != nil {
			out = append(out, row)
			continue
		}
		if l.Family == 4 && cfg != nil {
			row.Interface, _ = cfg.InterfaceFor(ip)
		} else if l.Family == 6 {
			row.Interface = prefixOf(prefixes, ip)
		}
		if d, ok := lengths[l.Family][row.Interface]; ok && !l.Expires.IsZero() {
			// A pool whose time was cut since would put it in the future.
			if r := l.Expires.Add(-d); !r.After(now) {
				row.Renewed = r
			}
		}
		if s, ok := statics[strings.ToLower(l.MAC)]; ok && l.MAC != "" {
			row.Description = s.Description
			pinned, err := netip.ParseAddr(s.IP)
			row.Static = err == nil && pinned == ip
		}
		for _, n := range heard[ip] {
			if row.Interface != "" && n.Interface != row.Interface {
				continue
			}
			// The address at another MAC is somebody else now.
			if l.Family == 4 && !strings.EqualFold(n.MAC, l.MAC) {
				continue
			}
			if n.Seen.After(row.Seen) {
				row.Seen = n.Seen
			}
		}
		row.Online = !row.Seen.IsZero() && now.Sub(row.Seen) <= onlineWithin
		out = append(out, row)
	}
	return out
}

type namedPrefix struct {
	name   string
	prefix netip.Prefix
}

// v6Prefixes are the global IPv6 prefixes on the links a DHCPv6 server
// runs on. The configuration names no prefix: it follows the interface.
func v6Prefixes(links []network.Link, served map[string]bool) []namedPrefix {
	var out []namedPrefix
	for _, l := range links {
		if !served[l.Name] {
			continue
		}
		for _, a := range l.Addresses {
			p, err := netip.ParsePrefix(a)
			if err != nil || !p.Addr().Is6() || p.Addr().IsLinkLocalUnicast() {
				continue
			}
			out = append(out, namedPrefix{l.Name, p.Masked()})
		}
	}
	return out
}

func prefixOf(prefixes []namedPrefix, ip netip.Addr) string {
	for _, p := range prefixes {
		if p.prefix.Contains(ip) {
			return p.name
		}
	}
	return ""
}

// recentLeases orders leases by when they were last handed out or
// renewed, newest first. One with no such time, as it never expires or
// sits in no pool, follows them by expiry.
func recentLeases(rows []lease, limit int) []lease {
	out := slices.Clone(rows)
	slices.SortStableFunc(out, func(a, b lease) int {
		if c := laterFirst(a.Renewed, b.Renewed); c != 0 {
			return c
		}
		return laterFirst(a.Expires, b.Expires)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// laterFirst orders the later of two times first, and a zero time last.
func laterFirst(a, b time.Time) int {
	switch {
	case a.Equal(b):
		return 0
	case a.IsZero():
		return 1
	case b.IsZero():
		return -1
	case a.After(b):
		return -1
	default:
		return 1
	}
}
