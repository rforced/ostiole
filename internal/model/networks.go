package model

import "net/netip"

// InternalNetworks are the networks behind enabled interfaces in zones that
// are not external: their static networks, their WireGuard peers' allowed
// addresses other than a default route, and the tailnet.
func (c *Config) InternalNetworks() []netip.Prefix {
	var out []netip.Prefix
	seen := map[netip.Prefix]bool{}
	add := func(p netip.Prefix) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, in := range c.Interfaces {
		if !in.Enabled || in.Zone == "" {
			continue
		}
		if z, ok := c.Zone(in.Zone); !ok || z.External {
			continue
		}
		for _, addr := range []string{in.IPv4.Address, in.IPv6.Address} {
			if p, err := netip.ParsePrefix(addr); err == nil {
				add(p.Masked())
			}
		}
		if in.WireGuard != nil {
			for _, peer := range in.WireGuard.Peers {
				if !peer.Enabled {
					continue
				}
				for _, a := range peer.AllowedIPs {
					if p, err := ParseAddress(a); err == nil && p.Bits() > 0 {
						add(p)
					}
				}
			}
		}
		if in.Tailscale != nil {
			add(tailscaleCGNAT)
			add(tailscaleULA)
		}
	}
	return out
}
