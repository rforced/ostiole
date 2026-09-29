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
	return slices.ContainsFunc(p.AllowedIPs, func(a string) bool {
		pre, err := ParseAddress(a)
		return err == nil && pre.Bits() == 0
	})
}
