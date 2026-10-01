package netlink

import (
	"errors"
	"fmt"
	"net"
	"slices"

	"golang.org/x/sys/unix"
)

// Link is one network device.
type Link struct {
	Index int
	Name  string
	// Kind is the driver's name for the device: vlan, bridge, bond, tun,
	// wireguard, ifb, … Empty for a physical device, which has none.
	Kind         string
	Flags        net.Flags
	MTU          int
	HardwareAddr net.HardwareAddr
	// ParentIndex is the device this one is stacked on, a VLAN's link.
	ParentIndex int
	// MasterIndex is the bridge or bond this one is enslaved to.
	MasterIndex int
	OperState   OperState
	VLANID      int
	// Statistics are counted since the kernel made the device; nil when
	// it sent none.
	Statistics *LinkStatistics
}

// OperState is a device's operational state, RFC 2863's as the kernel
// reports it.
type OperState uint8

// The operational states, IF_OPER_* in linux/if.h.
const (
	OperUnknown OperState = iota
	OperNotPresent
	OperDown
	OperLowerLayerDown
	OperTesting
	OperDormant
	OperUp
)

// LinkStatistics are a device's counters.
type LinkStatistics struct {
	RxPackets, TxPackets uint64
	RxBytes, TxBytes     uint64
	RxErrors, TxErrors   uint64
}

// Links lists every device.
func Links() ([]Link, error) {
	var e encoder
	e.ifinfomsg(0, 0, 0)
	msgs, err := dump(unix.RTM_GETLINK, &e)
	if err != nil {
		return nil, err
	}
	out := make([]Link, 0, len(msgs))
	for _, m := range msgs {
		if m.typ != unix.RTM_NEWLINK {
			continue
		}
		l, err := parseLink(m.data)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// LinkByName finds a device by name. One the kernel does not have is
// ErrLinkNotFound.
func LinkByName(name string) (Link, error) {
	// No device has a name this long, and the kernel refuses to look.
	if len(name) >= unix.IFNAMSIZ {
		return Link{}, fmt.Errorf("%w: %s", ErrLinkNotFound, name)
	}
	var e encoder
	e.ifinfomsg(0, 0, 0)
	e.ifname(name)
	var link Link
	found := false
	err := request(unix.RTM_GETLINK, 0, &e, func(m message) error {
		if m.typ != unix.RTM_NEWLINK {
			return nil
		}
		l, err := parseLink(m.data)
		link, found = l, err == nil
		return err
	})
	if errors.Is(err, unix.ENODEV) || (err == nil && !found) {
		return Link{}, fmt.Errorf("%w: %s", ErrLinkNotFound, name)
	}
	if err != nil {
		return Link{}, err
	}
	return link, nil
}

// AddLink creates a device of a kind that needs nothing but a name: an
// ifb, or a dummy.
func AddLink(name, kind string) error {
	var e encoder
	e.ifinfomsg(0, 0, 0)
	e.ifname(name)
	e.nest(unix.IFLA_LINKINFO, func() { e.attr(unix.IFLA_INFO_KIND, []byte(kind)) })
	return request(unix.RTM_NEWLINK, unix.NLM_F_CREATE|unix.NLM_F_EXCL, &e, nil)
}

// SetLinkUp brings a device up.
func SetLinkUp(index int) error {
	var e encoder
	e.ifinfomsg(index, unix.IFF_UP, unix.IFF_UP)
	return request(unix.RTM_NEWLINK, 0, &e, nil)
}

// DeleteLink removes a device.
func DeleteLink(index int) error {
	var e encoder
	e.ifinfomsg(index, 0, 0)
	return request(unix.RTM_DELLINK, 0, &e, nil)
}

func parseLink(b []byte) (Link, error) {
	if len(b) < unix.SizeofIfInfomsg {
		return Link{}, errors.New("netlink: a short link")
	}
	l := Link{
		Index: int(native.Uint32(b[4:])),
		Flags: linkFlags(native.Uint32(b[8:])),
	}
	as, err := attrs(b[unix.SizeofIfInfomsg:])
	if err != nil {
		return Link{}, err
	}
	var stats32, stats64, info []byte
	for _, a := range as {
		switch a.typ {
		case unix.IFLA_IFNAME:
			l.Name = cstring(a.data)
		case unix.IFLA_MTU:
			l.MTU = int(u32(a.data))
		case unix.IFLA_ADDRESS:
			// Loopback's is all zeroes, which is no address at all.
			if slices.ContainsFunc(a.data, func(b byte) bool { return b != 0 }) {
				l.HardwareAddr = net.HardwareAddr(append([]byte(nil), a.data...))
			}
		case unix.IFLA_LINK:
			l.ParentIndex = int(u32(a.data))
		case unix.IFLA_MASTER:
			l.MasterIndex = int(u32(a.data))
		case unix.IFLA_OPERSTATE:
			if len(a.data) > 0 {
				l.OperState = OperState(a.data[0])
			}
		case unix.IFLA_STATS:
			stats32 = a.data
		case unix.IFLA_STATS64:
			stats64 = a.data
		case unix.IFLA_LINKINFO:
			info = a.data
		}
	}
	switch {
	case len(stats64) >= 48:
		s := LinkStatistics{}
		for i, f := range s.fields() {
			*f = native.Uint64(stats64[8*i:])
		}
		l.Statistics = &s
	case len(stats32) >= 24:
		s := LinkStatistics{}
		for i, f := range s.fields() {
			*f = uint64(native.Uint32(stats32[4*i:]))
		}
		l.Statistics = &s
	}
	if info != nil {
		if err := l.parseInfo(info); err != nil {
			return Link{}, err
		}
	}
	return l, nil
}

// fields are the counters in the order both struct rtnl_link_stats and
// struct rtnl_link_stats64 start with.
func (s *LinkStatistics) fields() []*uint64 {
	return []*uint64{
		&s.RxPackets, &s.TxPackets, &s.RxBytes, &s.TxBytes,
		&s.RxErrors, &s.TxErrors,
	}
}

// parseInfo reads IFLA_LINKINFO: the kind, and a VLAN's id. What the data
// holds depends on the kind, so the kind is read first.
func (l *Link) parseInfo(b []byte) error {
	as, err := attrs(b)
	if err != nil {
		return err
	}
	var data []byte
	for _, a := range as {
		switch a.typ {
		case unix.IFLA_INFO_KIND:
			l.Kind = cstring(a.data)
		case unix.IFLA_INFO_DATA:
			data = a.data
		}
	}
	if l.Kind != "vlan" || data == nil {
		return nil
	}
	as, err = attrs(data)
	if err != nil {
		return err
	}
	for _, a := range as {
		if a.typ == unix.IFLA_VLAN_ID && len(a.data) >= 2 {
			l.VLANID = int(native.Uint16(a.data))
		}
	}
	return nil
}

// linkFlagBits pair the kernel's IFF_* bits with the standard library's.
var linkFlagBits = []struct {
	bit  uint32
	flag net.Flags
}{
	{unix.IFF_UP, net.FlagUp},
	{unix.IFF_BROADCAST, net.FlagBroadcast},
	{unix.IFF_LOOPBACK, net.FlagLoopback},
	{unix.IFF_POINTOPOINT, net.FlagPointToPoint},
	{unix.IFF_MULTICAST, net.FlagMulticast},
	{unix.IFF_RUNNING, net.FlagRunning},
}

func linkFlags(raw uint32) net.Flags {
	var f net.Flags
	for _, b := range linkFlagBits {
		if raw&b.bit != 0 {
			f |= b.flag
		}
	}
	return f
}
