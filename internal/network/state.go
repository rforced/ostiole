package network

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
)

// Link is the live state of one network interface.
type Link struct {
	Name    string `json:"name"`
	Index   int    `json:"index"`
	Kind    string `json:"kind"` // ethernet, loopback, vlan, bridge, bond, tun, wireguard, …
	MAC     string `json:"mac,omitempty"`
	Up      bool   `json:"up"`      // administratively up
	Carrier bool   `json:"carrier"` // operationally up
	MTU     int    `json:"mtu"`
	Parent  string `json:"parent,omitempty"`
	// Master is the bridge or bond this link is enslaved to, if any.
	Master    string   `json:"master,omitempty"`
	VLANID    int      `json:"vlanId,omitempty"`
	Addresses []string `json:"addresses"`
	// Traffic counted by the kernel since the link came up.
	RXBytes   uint64 `json:"rxBytes"`
	TXBytes   uint64 `json:"txBytes"`
	RXPackets uint64 `json:"rxPackets"`
	TXPackets uint64 `json:"txPackets"`
}

// helper reports whether a link is one Ostiole made for itself rather
// than an interface anybody configured: the device that carries the
// traffic arriving on a shaped interface so the kernel has somewhere to
// queue it leaving.
//
// It is left out of everything Discover feeds, which is every "what
// interfaces does this router have" question the product asks: the
// interfaces page, the setup wizard's picker, the dashboard summary, the
// metrics, and the names the self-signed certificate covers. None of them
// wants a device that carries no traffic of its own, cannot be given an
// address or a zone, and would double-count a download if it were
// measured. Offering it to the wizard as a candidate LAN is the worst of
// those.
//
// Both markers have to line up. A device somebody else called ifb0 is
// theirs, and is reported as honestly as any other unmanaged link.
func helper(l netlink.Link) bool {
	return l.Type() == "ifb" && strings.HasPrefix(l.Attrs().Name, model.IFBPrefix)
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
		if helper(l) {
			continue
		}
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
		if a.MasterIndex != 0 {
			li.Master = byIndex[a.MasterIndex]
		}
		if v, ok := l.(*netlink.Vlan); ok {
			li.VLANID = v.VlanId
		}
		if s := a.Statistics; s != nil {
			li.RXBytes, li.TXBytes = s.RxBytes, s.TxBytes
			li.RXPackets, li.TXPackets = s.RxPackets, s.TxPackets
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
