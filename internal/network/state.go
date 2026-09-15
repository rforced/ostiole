package network

import (
	"fmt"
	"net"
	"sort"

	"github.com/vishvananda/netlink"
)

// Link is the live state of one network interface.
type Link struct {
	Name      string   `json:"name"`
	Index     int      `json:"index"`
	Kind      string   `json:"kind"` // ethernet, loopback, vlan, bridge, bond, tun, wireguard, …
	MAC       string   `json:"mac,omitempty"`
	Up        bool     `json:"up"`      // administratively up
	Carrier   bool     `json:"carrier"` // operationally up
	MTU       int      `json:"mtu"`
	Parent    string   `json:"parent,omitempty"`
	VLANID    int      `json:"vlanId,omitempty"`
	Addresses []string `json:"addresses"`
}

// Discover lists interfaces and their addresses from the kernel. It works
// without privileges.
func Discover() ([]Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	byIndex := map[int]string{}
	for _, l := range links {
		byIndex[l.Attrs().Index] = l.Attrs().Name
	}
	out := make([]Link, 0, len(links))
	for _, l := range links {
		a := l.Attrs()
		li := Link{
			Name:      a.Name,
			Index:     a.Index,
			Kind:      kind(l),
			Up:        a.Flags&net.FlagUp != 0,
			Carrier:   carrier(a),
			MTU:       a.MTU,
			Addresses: []string{},
		}
		if len(a.HardwareAddr) > 0 {
			li.MAC = a.HardwareAddr.String()
		}
		if a.ParentIndex != 0 {
			li.Parent = byIndex[a.ParentIndex]
		}
		if v, ok := l.(*netlink.Vlan); ok {
			li.VLANID = v.VlanId
		}
		addrs, err := netlink.AddrList(l, netlink.FAMILY_ALL)
		if err == nil {
			for _, ad := range addrs {
				if ad.IPNet != nil {
					li.Addresses = append(li.Addresses, ad.IPNet.String())
				}
			}
		}
		out = append(out, li)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

// carrier reports operational up. Tunnels and loopback report an unknown
// operstate even when usable, so treat unknown-but-admin-up as up, like
// `ip link` does.
func carrier(a *netlink.LinkAttrs) bool {
	switch a.OperState {
	case netlink.OperUp:
		return true
	case netlink.OperUnknown:
		return a.Flags&net.FlagUp != 0
	}
	return false
}

func kind(l netlink.Link) string {
	if l.Attrs().Flags&net.FlagLoopback != 0 {
		return "loopback"
	}
	switch t := l.Type(); t {
	case "device":
		return "ethernet"
	default:
		return t
	}
}
