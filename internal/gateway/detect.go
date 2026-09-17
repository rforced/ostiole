package gateway

import (
	"fmt"
	"sort"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
)

// Detected is a default route the kernel already has. Most routers get one
// from DHCP before anyone configures anything, and it is the one carrying
// the traffic, so it belongs on the gateways page whether or not Ostiole
// put it there.
type Detected struct {
	// Address is the next hop.
	Address   string `json:"address"`
	Interface string `json:"interface"`
	Family    string `json:"family"`
	// Metric is the route's metric, which is how the kernel chooses
	// between several.
	Metric int `json:"metric"`
	// Protocol says where the route came from: dhcp, ra, static, kernel.
	Protocol string `json:"protocol"`
	// Configured names the gateway in the configuration that covers this
	// route, empty when nothing does.
	Configured string `json:"configured,omitempty"`
}

// Detect lists the default routes the kernel has, marking the ones a
// configured gateway already covers so the UI can offer the rest.
func Detect(cfg *model.Config) ([]Detected, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	names := map[int]string{}
	for _, l := range links {
		names[l.Attrs().Index] = l.Attrs().Name
	}

	out := []Detected{}
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, err := netlink.RouteList(nil, family)
		if err != nil {
			continue
		}
		for i := range routes {
			route := routes[i]
			if route.Gw == nil || (route.Dst != nil && !isDefault(route.Dst)) {
				continue
			}
			d := Detected{
				Address:   route.Gw.String(),
				Interface: names[route.LinkIndex],
				Family:    "IPv4",
				Metric:    route.Priority,
				Protocol:  routeProtocol(route.Protocol),
			}
			if family == netlink.FAMILY_V6 {
				d.Family = "IPv6"
			}
			d.Configured = coveredBy(cfg, d)
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Metric != out[j].Metric {
			return out[i].Metric < out[j].Metric
		}
		return out[i].Interface < out[j].Interface
	})
	return out, nil
}

// coveredBy finds the configured gateway that owns a detected route: one
// on the same interface, with either the same address or none of its own
// (which means "whatever the network gives us").
func coveredBy(cfg *model.Config, d Detected) string {
	if cfg == nil {
		return ""
	}
	for _, g := range cfg.Gateways {
		if g.Interface != d.Interface {
			continue
		}
		if g.Address == d.Address {
			return g.Name
		}
		if g.Address == "" {
			// A dynamic gateway covers whichever family it learns; without
			// an address there is nothing more to compare.
			return g.Name
		}
	}
	return ""
}

// routeProtocol names where a route came from, in the words people use
// rather than the kernel's numbers.
func routeProtocol(p netlink.RouteProtocol) string {
	switch int(p) {
	case 2:
		return "kernel"
	case 3:
		return "static"
	case 9:
		return "ra"
	case 16:
		return "dhcp"
	case 186:
		return "bgp"
	case 187:
		return "isis"
	case 188:
		return "ospf"
	case 189:
		return "rip"
	}
	if s := p.String(); s != "" {
		return s
	}
	return fmt.Sprintf("proto-%d", int(p))
}

// Suggest turns a detected route into the gateway it would become, so the
// UI can offer to adopt it with one click. Priorities follow the metric
// order of what is already configured.
func Suggest(cfg *model.Config, d Detected) model.Gateway {
	g := model.Gateway{
		Name:      suggestName(cfg, d.Interface),
		Enabled:   true,
		Interface: d.Interface,
		Priority:  len(cfg.Gateways),
	}
	// A route from DHCP or a router advertisement will change; pinning the
	// address it happens to have today would break on the next lease.
	if d.Protocol != "dhcp" && d.Protocol != "ra" {
		g.Address = d.Address
	}
	return g
}

// suggestName picks a name that is free, based on the interface.
func suggestName(cfg *model.Config, iface string) string {
	base := "gw_" + sanitizeName(iface)
	if _, taken := cfg.Gateway(base); !taken {
		return base
	}
	for i := 2; i < 100; i++ {
		name := fmt.Sprintf("%s%d", base, i)
		if _, taken := cfg.Gateway(name); !taken {
			return name
		}
	}
	return base
}

// sanitizeName turns an interface name into something a gateway name
// accepts: lower case letters, digits, and underscores.
func sanitizeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 || out[0] < 'a' || out[0] > 'z' {
		return "gw"
	}
	return string(out)
}
