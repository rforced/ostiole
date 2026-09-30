package model

import (
	"net/netip"
	"slices"
	"strings"
)

// TunnelRoute is a network routed into a WireGuard tunnel, and the peer
// that listed it.
type TunnelRoute struct {
	Prefix netip.Prefix
	Peer   string
	// Mapped is a network the peer shows here under another prefix
	// (WireGuardPeer.Theirs): this side has one with the same numbers, so
	// it goes into the tunnel by a mark and a table of its own, never the
	// main table.
	Mapped bool
}

// TunnelRoutes lists the enabled peers' networks that the tunnel's own
// addresses do not already cover, each once. Default routes are left out:
// sending everything down a tunnel is a deliberate act, not a side effect
// of adding a peer.
func (i Interface) TunnelRoutes() []TunnelRoute {
	if i.WireGuard == nil {
		return nil
	}
	own := i.tunnelPrefixes()
	var out []TunnelRoute
	seen := map[netip.Prefix]bool{}
	for _, p := range i.WireGuard.Peers {
		if !p.Enabled {
			continue
		}
		for _, a := range p.AllowedIPs {
			pre, err := ParseAddress(a)
			if err != nil || pre.Bits() == 0 || seen[pre] {
				continue
			}
			if slices.ContainsFunc(own, func(o netip.Prefix) bool { return o.Overlaps(pre) && o.Bits() <= pre.Bits() }) {
				continue
			}
			seen[pre] = true
			out = append(out, TunnelRoute{Prefix: pre, Peer: p.Name, Mapped: p.shows(pre)})
		}
	}
	slices.SortFunc(out, func(a, b TunnelRoute) int { return strings.Compare(a.Prefix.String(), b.Prefix.String()) })
	return out
}

// tunnelPrefixes are the networks of the tunnel's own addresses, which the
// kernel routes into it as connected routes.
func (i Interface) tunnelPrefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, addr := range []string{i.IPv4.Address, i.IPv6.Address} {
		if p, err := netip.ParsePrefix(addr); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

// Peer finds the WireGuard peer a rule names, written "<tunnel>/<peer>",
// and the tunnel it belongs to.
func (c *Config) Peer(ref string) (*Interface, *WireGuardPeer, bool) {
	tunnel, name, ok := strings.Cut(ref, "/")
	if !ok {
		return nil, nil, false
	}
	for i := range c.Interfaces {
		in := &c.Interfaces[i]
		if in.Name != tunnel || in.WireGuard == nil {
			continue
		}
		for j := range in.WireGuard.Peers {
			if in.WireGuard.Peers[j].Name == name {
				return in, &in.WireGuard.Peers[j], true
			}
		}
	}
	return nil, nil, false
}

// PeerRef writes the name a rule gives a peer.
func PeerRef(tunnel, peer string) string { return tunnel + "/" + peer }

// KeepaliveWithoutEndpoint names the enabled peers on enabled tunnels that
// have a keepalive but no endpoint, as a rule names them. Such a peer calls
// in, and once it leaves the router keeps shaking hands with the address it
// last called from.
func (c *Config) KeepaliveWithoutEndpoint() []string {
	var out []string
	for _, in := range c.Interfaces {
		if !in.Enabled || in.WireGuard == nil {
			continue
		}
		for _, p := range in.WireGuard.Peers {
			if p.Enabled && p.Keepalive > 0 && p.Endpoint == "" {
				out = append(out, PeerRef(in.Name, p.Name))
			}
		}
	}
	return out
}

// TakesDefaultRoute reports whether the peer's allowed addresses include
// 0.0.0.0/0 or ::/0, which make it the way to the whole internet.
func (p WireGuardPeer) TakesDefaultRoute() bool {
	v4, v6 := p.defaultRoutes()
	return v4 || v6
}

// defaultRoutes reports which families' default routes the peer takes.
func (p WireGuardPeer) defaultRoutes() (v4, v6 bool) {
	for _, a := range p.AllowedIPs {
		if pre, err := ParseAddress(a); err == nil && pre.Bits() == 0 {
			v4 = v4 || pre.Addr().Is4()
			v6 = v6 || !pre.Addr().Is4()
		}
	}
	return v4, v6
}

// TunnelGateway reports whether g sends what rules route through it into
// a WireGuard tunnel rather than to a next hop: its interface is a tunnel
// and it names no address. Such a gateway never carries the default route.
func (c *Config) TunnelGateway(g Gateway) bool {
	if g.Address != "" {
		return false
	}
	in, ok := c.Interface(g.Interface)
	return ok && in.WireGuard != nil
}

// TunnelFamilies reports the families a tunnel gateway carries: those its
// tunnel has an address in and an enabled peer takes the default route
// of. Traffic of the other family is refused, never sent out elsewhere.
func (c *Config) TunnelFamilies(g Gateway) (v4, v6 bool) {
	in, ok := c.Interface(g.Interface)
	if !ok || in.WireGuard == nil {
		return false, false
	}
	for _, p := range in.WireGuard.Peers {
		if p.Enabled {
			p4, p6 := p.defaultRoutes()
			v4, v6 = v4 || p4, v6 || p6
		}
	}
	return v4 && in.IPv4.Mode == AddrStatic && in.IPv4.Address != "",
		v6 && in.IPv6.Mode == AddrStatic && in.IPv6.Address != ""
}

// PrefixMap is a NetMap read: the network the hosts are on and the prefix
// they go by.
type PrefixMap struct {
	Real, Shown netip.Prefix
}

// ParseNetMaps reads the maps that parse, skipping the rest; validation
// says what is wrong with those.
func ParseNetMaps(maps []NetMap) []PrefixMap {
	var out []PrefixMap
	for _, m := range maps {
		network, err1 := netip.ParsePrefix(m.Network)
		shown, err2 := netip.ParsePrefix(m.As)
		if err1 != nil || err2 != nil || !network.Addr().Is4() || !shown.Addr().Is4() || network.Bits() != shown.Bits() {
			continue
		}
		out = append(out, PrefixMap{Real: network.Masked(), Shown: shown.Masked()})
	}
	return out
}

// ShownRoutes are the prefixes the tunnel's enabled peers show their
// networks under here. They are routed into the tunnel like its peers'
// networks, so the router can answer them, and its own connections to
// them are mapped back to the real networks on the way out.
func (i Interface) ShownRoutes() []netip.Prefix {
	if i.WireGuard == nil {
		return nil
	}
	var out []netip.Prefix
	for _, p := range i.WireGuard.Peers {
		if !p.Enabled {
			continue
		}
		for _, m := range ParseNetMaps(p.Theirs) {
			out = append(out, m.Shown)
		}
	}
	return out
}

// shows reports whether the peer shows a network of its own under another
// prefix.
func (p WireGuardPeer) shows(pre netip.Prefix) bool {
	return slices.ContainsFunc(ParseNetMaps(p.Theirs), func(m PrefixMap) bool { return m.Real == pre.Masked() })
}

// TranslateTarget is a tunnel whose peers show networks under other
// prefixes. Its mark sends traffic for their real networks into it by a
// table of its own, since this side has networks with the same numbers.
type TranslateTarget struct {
	Tunnel string
	Mark   uint32
	Table  int
	// Networks are the real networks behind the tunnel's peers.
	Networks []netip.Prefix
}

// TranslateTargets numbers the tunnels that show networks after the
// policy targets, in the order the configuration has them.
func (c *Config) TranslateTargets() []TranslateTarget {
	n := 0
	for _, t := range c.PolicyTargets() {
		n = int(t.Mark >> PolicyMarkShift)
	}
	var out []TranslateTarget
	for _, in := range c.Interfaces {
		if !in.Enabled || in.WireGuard == nil {
			continue
		}
		var nets []netip.Prefix
		for _, r := range in.TunnelRoutes() {
			if r.Mapped {
				nets = append(nets, r.Prefix)
			}
		}
		if len(nets) == 0 {
			continue
		}
		n++
		for reservedPolicyNumbers[n] {
			n++
		}
		if n > MaxPolicyTargets {
			break
		}
		out = append(out, TranslateTarget{
			Tunnel: in.Name, Mark: uint32(n) << PolicyMarkShift, Table: PolicyTableBase + n, Networks: nets,
		})
	}
	return out
}
