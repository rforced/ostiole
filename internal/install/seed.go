package install

import (
	"errors"
	"net/netip"
	"strings"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// A router is handed to systemd-networkd at install time, before anybody
// has configured anything, so the configuration networkd is given is read
// off the router itself: whatever addresses it has now, it keeps. An
// address the kernel marks permanent was put there by a file and becomes
// a static address; one without that flag came from a lease or a router
// advertisement and becomes DHCP or SLAAC, so the switch renews it rather
// than pinning somebody else's lease.

// seedSkip are the link kinds a handover has no business rendering:
// whatever made them keeps their addresses, and networkd would strip
// what it found. Everything else is fair game, including the veth and
// tap devices a container has instead of an interface.
var seedSkip = map[string]bool{"loopback": true, "wireguard": true, "ppp": true}

// SeedNetwork builds a configuration from the router's live addressing,
// one interface per link that carries a global address. It is never
// stored: it exists to be rendered into networkd units so the first apply
// replaces it.
func SeedNetwork() (*model.Config, error) {
	links, err := network.Discover()
	if err != nil {
		return nil, err
	}
	v4gw, v6gw, err := network.DefaultRoutes()
	if err != nil {
		return nil, err
	}
	cfg := model.Starter(model.StarterOptions{})
	for _, l := range links {
		if seedSkip[l.Kind] || l.Master != "" {
			continue
		}
		in, gws := seedInterface(l, v4gw[l.Name], v6gw[l.Name])
		if in == nil {
			continue
		}
		cfg.Interfaces = append(cfg.Interfaces, *in)
		cfg.Gateways = append(cfg.Gateways, gws...)
	}
	if len(cfg.Interfaces) == 0 {
		return nil, errors.New("no interface on this router carries a global address")
	}
	return cfg, nil
}

// seedInterface describes one link the way it is addressed now, with a
// gateway for each family that has a default route and a static address
// to hang it on. It returns nil for a link with no global address.
func seedInterface(l network.Link, gw4, gw6 netip.Addr) (*model.Interface, []model.Gateway) {
	dynamic := map[string]bool{}
	for _, a := range l.DynamicAddresses {
		dynamic[a] = true
	}
	var v4, v6 []string
	anyDynamic4, anyDynamic6 := false, false
	for _, a := range l.Addresses {
		p, err := netip.ParsePrefix(a)
		if err != nil || !p.Addr().IsGlobalUnicast() {
			continue
		}
		if p.Addr().Is4() {
			v4 = append(v4, a)
			anyDynamic4 = anyDynamic4 || dynamic[a]
		} else {
			v6 = append(v6, a)
			anyDynamic6 = anyDynamic6 || dynamic[a]
		}
	}
	if len(v4) == 0 && len(v6) == 0 {
		return nil, nil
	}
	in := &model.Interface{
		Name:        l.Name,
		Description: "Seeded from this router's addresses at install",
		Enabled:     true,
		IPv4:        model.IPv4{Mode: model.AddrNone},
		IPv6:        model.IPv6{Mode: model.AddrNone},
	}
	// A link with an MTU of its own keeps it: networkd would otherwise put
	// it back to the driver's default on the first reconfigure.
	if l.MTU != 0 && l.MTU != 1500 {
		in.MTU = l.MTU
	}
	var gws []model.Gateway
	switch {
	case anyDynamic4:
		in.IPv4.Mode = model.AddrDHCP
	case len(v4) > 0:
		in.IPv4.Mode, in.IPv4.Address = model.AddrStatic, v4[0]
		if gw4.IsValid() {
			gws = append(gws, model.Gateway{
				Name: gatewayName(l.Name, "v4"), Enabled: true, Interface: l.Name, Address: gw4.String(),
			})
		}
	}
	switch {
	case anyDynamic6:
		in.IPv6.Mode = model.AddrSLAAC
	case len(v6) > 0:
		in.IPv6.Mode, in.IPv6.Address = model.AddrStatic, v6[0]
		if gw6.IsValid() {
			gws = append(gws, model.Gateway{
				Name: gatewayName(l.Name, "v6"), Enabled: true, Interface: l.Name, Address: gw6.String(),
			})
		}
	}
	return in, gws
}

// gatewayName turns an interface name into one a configuration accepts:
// lower-case letters, digits and underscores, starting with a letter.
func gatewayName(iface, suffix string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, iface)
	return "gw_" + clean + "_" + suffix
}
