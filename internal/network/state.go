package network

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
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
	Master string `json:"master,omitempty"`
	VLANID int    `json:"vlanId,omitempty"`
	// Wireless marks a link on a wifi device, and Phy names the device.
	Wireless  bool     `json:"wireless,omitempty"`
	Phy       string   `json:"phy,omitempty"`
	Addresses []string `json:"addresses"`
	// DynamicAddresses are the ones in Addresses that a lease or a router
	// advertisement put there rather than a configuration file, which is
	// how a seeded interface knows to ask for DHCP instead of pinning
	// somebody else's lease as a static address.
	DynamicAddresses []string `json:"dynamicAddresses,omitempty"`
	// Traffic the kernel counted since it made the link.
	RXBytes   uint64 `json:"rxBytes"`
	TXBytes   uint64 `json:"txBytes"`
	RXPackets uint64 `json:"rxPackets"`
	TXPackets uint64 `json:"txPackets"`
	// Errors the kernel counted since it made the link, which the traffic
	// counter turns into the last 72 hours'. Either rising on a wired port
	// is a bad cable, a duplex mismatch, or a driver that cannot keep up.
	RXErrors uint64 `json:"-"`
	TXErrors uint64 `json:"-"`
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
	return l.Kind == "ifb" && strings.HasPrefix(l.Name, model.IFBPrefix)
}

// withoutRadios drops each card's own interface: the first the kernel made
// on its phy, which the networks hostapd serves on the card come after. It
// is left out for the reason the shaping helper is: it carries no traffic,
// takes no address and belongs to no zone. The card is on the Wireless page.
func withoutRadios(links []Link) []Link {
	first := map[string]int{}
	for _, l := range links {
		if idx, ok := first[l.Phy]; l.Phy != "" && (!ok || l.Index < idx) {
			first[l.Phy] = l.Index
		}
	}
	out := links[:0]
	for _, l := range links {
		if l.Phy != "" && first[l.Phy] == l.Index {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Discover lists interfaces and their addresses from the kernel. It works
// without privileges.
func Discover() ([]Link, error) {
	links, err := netlink.Links()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	byIndex := map[int]string{}
	for _, l := range links {
		byIndex[l.Index] = l.Name
	}
	// One dump holds every link's addresses. Without it the links are
	// still worth reporting.
	addrs := map[int][]netlink.Addr{}
	if all, err := netlink.Addrs(unix.AF_UNSPEC); err == nil {
		for _, a := range all {
			addrs[a.LinkIndex] = append(addrs[a.LinkIndex], a)
		}
	}
	out := make([]Link, 0, len(links))
	for _, l := range links {
		if helper(l) {
			continue
		}
		li := Link{
			Name:      l.Name,
			Index:     l.Index,
			Kind:      kind(l),
			Up:        l.Flags&net.FlagUp != 0,
			Carrier:   carrier(l),
			MTU:       l.MTU,
			VLANID:    l.VLANID,
			Addresses: []string{},
		}
		if len(l.HardwareAddr) > 0 {
			li.MAC = l.HardwareAddr.String()
		}
		if l.ParentIndex != 0 {
			li.Parent = byIndex[l.ParentIndex]
		}
		if l.MasterIndex != 0 {
			li.Master = byIndex[l.MasterIndex]
		}
		if phy, err := os.ReadFile("/sys/class/net/" + l.Name + "/phy80211/name"); err == nil {
			li.Wireless, li.Phy = true, strings.TrimSpace(string(phy))
		}
		if s := l.Statistics; s != nil {
			li.RXBytes, li.TXBytes = s.RxBytes, s.TxBytes
			li.RXPackets, li.TXPackets = s.RxPackets, s.TxPackets
			li.RXErrors, li.TXErrors = s.RxErrors, s.TxErrors
		}
		for _, ad := range addrs[l.Index] {
			li.Addresses = append(li.Addresses, ad.IPNet.String())
			if ad.Flags&unix.IFA_F_PERMANENT == 0 {
				li.DynamicAddresses = append(li.DynamicAddresses, ad.IPNet.String())
			}
		}
		out = append(out, li)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	out = withoutRadios(out)
	return out, nil
}

// DefaultRoutes lists the default gateway on each interface, keyed by
// interface name, one map per family. An address with no way out is not a
// WAN, which is the difference a seeded configuration turns on.
func DefaultRoutes() (v4, v6 map[string]netip.Addr, err error) {
	links, err := netlink.Links()
	if err != nil {
		return nil, nil, fmt.Errorf("list links: %w", err)
	}
	byIndex := map[int]string{}
	for _, l := range links {
		byIndex[l.Index] = l.Name
	}
	routes, err := netlink.Routes(unix.AF_UNSPEC, netlink.RouteFilter{})
	if err != nil {
		return nil, nil, fmt.Errorf("list routes: %w", err)
	}
	v4, v6 = map[string]netip.Addr{}, map[string]netip.Addr{}
	for _, r := range routes {
		// A default route has no destination prefix, or one of length
		// zero; anything else is a route to somewhere in particular.
		if r.Dst != nil {
			if ones, _ := r.Dst.Mask.Size(); ones != 0 {
				continue
			}
		}
		gw, ok := netip.AddrFromSlice(r.Gw)
		if !ok {
			continue
		}
		name := byIndex[r.LinkIndex]
		if name == "" {
			continue
		}
		gw = gw.Unmap()
		into := v6
		if gw.Is4() {
			into = v4
		}
		if _, seen := into[name]; !seen {
			into[name] = gw
		}
	}
	return v4, v6, nil
}

// carrier reports operational up. Tunnels and loopback report an unknown
// operstate even when usable, so treat unknown-but-admin-up as up, like
// `ip link` does.
func carrier(l netlink.Link) bool {
	switch l.OperState {
	case netlink.OperUp:
		return true
	case netlink.OperUnknown:
		return l.Flags&net.FlagUp != 0
	}
	return false
}

// kind names a link's driver; a physical device has none.
func kind(l netlink.Link) string {
	switch {
	case l.Flags&net.FlagLoopback != 0:
		return "loopback"
	case l.Kind == "":
		return "ethernet"
	}
	return l.Kind
}
