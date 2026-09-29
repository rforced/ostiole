package model

import (
	"net/netip"
	"slices"
	"strings"
)

// TunnelRoute is a network routed into a WireGuard tunnel in the main
// table, and the peer that listed it.
type TunnelRoute struct {
	Prefix netip.Prefix
	Peer   string
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
			out = append(out, TunnelRoute{Prefix: pre, Peer: p.Name})
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
