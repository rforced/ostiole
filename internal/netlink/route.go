package netlink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// Route is one route.
type Route struct {
	Family int
	// Table is the routing table. A request that leaves it zero means the
	// main one.
	Table int
	// Dst is where the route leads: 0.0.0.0/0 or ::/0 for a default route
	// read from the kernel, which sends none.
	Dst       *net.IPNet
	Gw        net.IP
	LinkIndex int
	// Src is the source address the route prefers: a lease's own, on the
	// route networkd adds for it.
	Src net.IP
	// Priority is the metric.
	Priority int
	Protocol Protocol
	Scope    uint8
	Tos      uint8
	// Type is unix.RTN_*. A request that leaves it zero adds a unicast
	// route.
	Type int
	// Flags are the kernel's RTNH_F_* and RTM_F_* bits. Only RTNH_F_ONLINK
	// and RTNH_F_PERVASIVE go back out: linkdown, dead and the rest are the
	// kernel's to set, and it refuses a route that carries them.
	Flags int
	// MultiPath holds the next hops of a route with several, which has no
	// Gw or LinkIndex of its own.
	MultiPath []Nexthop
	// kept holds what the kernel said of the route that nothing here reads
	// but a copy of it has to carry: its metrics (MTU, hop limit, …), realm,
	// encapsulation, and IPv6 router preference. Failover moves a route by
	// adding a copy and deleting the original.
	kept []attr
}

// keptAttrs are the attributes a route read from the kernel carries back
// out as they came.
var keptAttrs = map[uint16]bool{
	unix.RTA_METRICS: true, unix.RTA_FLOW: true, unix.RTA_ENCAP: true, unix.RTA_ENCAP_TYPE: true,
	unix.RTA_VIA: true, unix.RTA_NEWDST: true, unix.RTA_PREF: true,
}

// Nexthop is one of a multipath route's next hops.
type Nexthop struct {
	LinkIndex int
	Gw        net.IP
}

// sentFlags are the route flags a request carries.
const sentFlags = unix.RTNH_F_ONLINK | unix.RTNH_F_PERVASIVE

// Protocol says who put a route in the kernel: unix.RTPROT_*.
type Protocol uint8

var protocolNames = map[Protocol]string{
	unix.RTPROT_UNSPEC: "unspec", unix.RTPROT_REDIRECT: "redirect", unix.RTPROT_KERNEL: "kernel",
	unix.RTPROT_BOOT: "boot", unix.RTPROT_STATIC: "static", unix.RTPROT_GATED: "gated",
	unix.RTPROT_RA: "ra", unix.RTPROT_MRT: "mrt", unix.RTPROT_ZEBRA: "zebra",
	unix.RTPROT_BIRD: "bird", unix.RTPROT_DNROUTED: "dnrouted", unix.RTPROT_XORP: "xorp",
	unix.RTPROT_NTK: "ntk", unix.RTPROT_DHCP: "dhcp", unix.RTPROT_MROUTED: "mrouted",
	unix.RTPROT_BABEL: "babel", unix.RTPROT_BGP: "bgp", unix.RTPROT_ISIS: "isis",
	unix.RTPROT_OSPF: "ospf", unix.RTPROT_RIP: "rip", unix.RTPROT_EIGRP: "eigrp",
}

// String names a protocol as iproute2 does, and numbers one it has no
// name for.
func (p Protocol) String() string {
	if s, ok := protocolNames[p]; ok {
		return s
	}
	return strconv.Itoa(int(p))
}

// RouteFilter narrows a listing. The zero value lists the main table,
// which is what `ip route` shows.
type RouteFilter struct {
	// Table lists one table instead of main.
	Table int
	// AllTables lists every table.
	AllTables bool
	// LinkIndex keeps the routes that leave through one link. A multipath
	// route names no link of its own, so it is never among them.
	LinkIndex int
}

// Routes lists the routes of one family: unix.AF_INET, unix.AF_INET6, or
// unix.AF_UNSPEC for both. Routes the kernel cloned are left out.
func Routes(fam int, f RouteFilter) ([]Route, error) {
	var e encoder
	e.rtmsg(rtmsg{family: e.n8(fam, "family")})
	msgs, err := dump(unix.RTM_GETROUTE, &e)
	if err != nil {
		return nil, err
	}
	table := f.Table
	if table == 0 {
		table = unix.RT_TABLE_MAIN
	}
	var out []Route
	for _, m := range msgs {
		if m.typ != unix.RTM_NEWROUTE {
			continue
		}
		r, err := parseRoute(m.data)
		if err != nil {
			return nil, err
		}
		switch {
		case r.Family != unix.AF_INET && r.Family != unix.AF_INET6:
		case fam != unix.AF_UNSPEC && r.Family != fam:
		case r.Flags&unix.RTM_F_CLONED != 0:
		case !f.AllTables && r.Table != table:
		case f.LinkIndex != 0 && r.LinkIndex != f.LinkIndex:
		default:
			out = append(out, r)
		}
	}
	return out, nil
}

// AddRoute adds a route. One the kernel already has is an error.
func AddRoute(r Route) error {
	return changeRoute(unix.RTM_NEWROUTE, unix.NLM_F_CREATE|unix.NLM_F_EXCL, r)
}

// AppendRoute adds a route beside any the kernel keys the same way.
func AppendRoute(r Route) error {
	return changeRoute(unix.RTM_NEWROUTE, unix.NLM_F_CREATE|unix.NLM_F_APPEND, r)
}

// ReplaceRoute adds a route, or replaces the one the kernel keys the same
// way.
func ReplaceRoute(r Route) error {
	return changeRoute(unix.RTM_NEWROUTE, unix.NLM_F_CREATE|unix.NLM_F_REPLACE, r)
}

// DeleteRoute removes a route. The kernel matches on what the route sets,
// so one read from the kernel removes itself and nothing beside it.
func DeleteRoute(r Route) error {
	return changeRoute(unix.RTM_DELROUTE, 0, r)
}

func changeRoute(typ, flags uint16, r Route) error {
	var e encoder
	e.route(typ == unix.RTM_NEWROUTE, r)
	return request(typ, flags, &e, nil)
}

// rtmsg is struct rtmsg. struct fib_rule_hdr has the same layout, with the
// action where the type is.
type rtmsg struct {
	family, dstLen, srcLen, tos, table, protocol, scope, typ uint8
	flags                                                    uint32
}

func (e *encoder) rtmsg(h rtmsg) {
	e.b = append(e.b, h.family, h.dstLen, h.srcLen, h.tos, h.table, h.protocol, h.scope, h.typ)
	e.b = native.AppendUint32(e.b, h.flags)
}

// route adds a route's header and attributes. They are what the library
// before this package sent, so the kernel reads the same requests: an add
// defaults to the boot protocol and a unicast route, a delete matches any.
func (e *encoder) route(add bool, r Route) {
	fam := e.routeFamily(r)
	h := rtmsg{
		family:   e.n8(fam, "family"),
		protocol: uint8(r.Protocol),
		scope:    r.Scope,
		tos:      r.Tos,
		typ:      e.n8(r.Type, "route type"),
		flags:    e.n32(r.Flags&sentFlags, "route flags"),
	}
	switch {
	case r.Table == 0:
		h.table = unix.RT_TABLE_MAIN
	case r.Table < 256:
		h.table = e.n8(r.Table, "table")
	default:
		// A table past the header's byte goes in RTA_TABLE alone.
		h.table = unix.RT_TABLE_UNSPEC
	}
	if add && h.protocol == 0 {
		h.protocol = unix.RTPROT_BOOT
	}
	if add && h.typ == 0 {
		h.typ = unix.RTN_UNICAST
	}
	if r.Dst != nil {
		ones, _ := r.Dst.Mask.Size()
		h.dstLen = e.n8(ones, "prefix length")
	}
	e.rtmsg(h)
	if r.Dst != nil {
		e.ip(unix.RTA_DST, fam, r.Dst.IP)
	}
	if r.Gw != nil {
		e.ip(unix.RTA_GATEWAY, fam, r.Gw)
	}
	if r.Src != nil {
		e.ip(unix.RTA_PREFSRC, fam, r.Src)
	}
	for _, a := range r.kept {
		e.attr(a.typ, a.data)
	}
	if len(r.MultiPath) > 0 {
		e.nest(unix.RTA_MULTIPATH, func() {
			for _, hop := range r.MultiPath {
				// struct rtnexthop: its length, flags and hops, then the
				// link, then the hop's own attributes.
				start := len(e.b)
				e.b = append(e.b, 0, 0, 0, 0)
				e.b = native.AppendUint32(e.b, e.n32(hop.LinkIndex, "link index"))
				if hop.Gw != nil {
					e.ip(unix.RTA_GATEWAY, fam, hop.Gw)
				}
				e.patchLength(start)
			}
		})
	}
	if r.Table > 0 {
		e.u32(unix.RTA_TABLE, e.n32(r.Table, "table"))
	}
	if r.Priority > 0 {
		e.u32(unix.RTA_PRIORITY, e.n32(r.Priority, "metric"))
	}
	if r.LinkIndex > 0 {
		e.u32(unix.RTA_OIF, e.n32(r.LinkIndex, "link index"))
	}
}

// routeFamily is the family a route's addresses are in, or the one it
// names when it has none.
func (e *encoder) routeFamily(r Route) int {
	addrs := []net.IP{r.Gw, r.Src}
	if r.Dst != nil {
		addrs = append(addrs, r.Dst.IP)
	}
	for _, hop := range r.MultiPath {
		addrs = append(addrs, hop.Gw)
	}
	fam := 0
	for _, a := range addrs {
		if a == nil {
			continue
		}
		if f := family(a); fam != 0 && f != fam {
			e.fail("a route with addresses of both families")
		} else {
			fam = f
		}
	}
	switch {
	case fam != 0:
		return fam
	case r.Family != 0:
		return r.Family
	}
	return unix.AF_INET
}

func parseRoute(b []byte) (Route, error) {
	if len(b) < unix.SizeofRtMsg {
		return Route{}, errors.New("netlink: a short route")
	}
	r := Route{
		Family:   int(b[0]),
		Tos:      b[3],
		Table:    int(b[4]),
		Protocol: Protocol(b[5]),
		Scope:    b[6],
		Type:     int(b[7]),
		Flags:    int(native.Uint32(b[8:])),
	}
	dstLen := int(b[1])
	as, err := attrs(b[unix.SizeofRtMsg:])
	if err != nil {
		return Route{}, err
	}
	for _, a := range as {
		switch a.typ {
		case unix.RTA_DST:
			if d := ip(a.data); d != nil {
				r.Dst = &net.IPNet{IP: d, Mask: net.CIDRMask(dstLen, 8*len(d))}
			}
		case unix.RTA_GATEWAY:
			r.Gw = ip(a.data)
		case unix.RTA_PREFSRC:
			r.Src = ip(a.data)
		case unix.RTA_OIF:
			r.LinkIndex = int(u32(a.data))
		case unix.RTA_PRIORITY:
			r.Priority = int(u32(a.data))
		case unix.RTA_TABLE:
			// The header holds a table past 255 as RT_TABLE_COMPAT.
			r.Table = int(u32(a.data))
		case unix.RTA_MULTIPATH:
			if r.MultiPath, err = parseNexthops(a.data); err != nil {
				return Route{}, err
			}
		default:
			if keptAttrs[a.typ] {
				r.kept = append(r.kept, attr{typ: a.typ, data: append([]byte(nil), a.data...)})
			}
		}
	}
	// A default route comes without a destination, and is read as its
	// family's zero prefix, as iproute2 prints it.
	if r.Dst == nil {
		switch r.Family {
		case unix.AF_INET:
			r.Dst = &net.IPNet{IP: make(net.IP, net.IPv4len), Mask: net.CIDRMask(dstLen, 32)}
		case unix.AF_INET6:
			r.Dst = &net.IPNet{IP: make(net.IP, net.IPv6len), Mask: net.CIDRMask(dstLen, 128)}
		}
	}
	return r, nil
}

// parseNexthops reads RTA_MULTIPATH: a run of struct rtnexthop, each
// followed by the hop's own attributes.
func parseNexthops(b []byte) ([]Nexthop, error) {
	var out []Nexthop
	for len(b) >= 8 {
		n := int(native.Uint16(b))
		if n < 8 || n > len(b) {
			return nil, fmt.Errorf("netlink: a next hop of %d bytes in %d", n, len(b))
		}
		hop := Nexthop{LinkIndex: int(native.Uint32(b[4:]))}
		as, err := attrs(b[8:n])
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			if a.typ == unix.RTA_GATEWAY {
				hop.Gw = ip(a.data)
			}
		}
		out = append(out, hop)
		b = b[min(align(n), len(b)):]
	}
	return out, nil
}

// WatchRouteDeletes signals each time the kernel removes a route that
// keep accepts, until ctx is done; then it closes the channel. Signals
// coalesce, and a message the socket had no room for is signalled too,
// since whatever it said is unknown.
func WatchRouteDeletes(ctx context.Context, keep func(Route) bool) (<-chan struct{}, error) {
	c, err := dial(unix.NETLINK_ROUTE, unix.RTMGRP_IPV4_ROUTE|unix.RTMGRP_IPV6_ROUTE)
	if err != nil {
		return nil, err
	}
	out := make(chan struct{}, 1)
	stop := context.AfterFunc(ctx, func() { _ = c.f.SetReadDeadline(time.Now()) })
	go func() {
		defer close(out)
		defer func() { _ = c.close() }()
		defer stop()
		for {
			msgs, err := c.receive()
			switch {
			case errors.Is(err, unix.ENOBUFS):
			case err != nil:
				return
			case !deletesAny(msgs, keep):
				continue
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out, nil
}

// deletesAny reports whether msgs remove a route keep accepts.
func deletesAny(msgs []message, keep func(Route) bool) bool {
	for _, m := range msgs {
		if m.typ != unix.RTM_DELROUTE {
			continue
		}
		if r, err := parseRoute(m.data); err == nil && keep(r) {
			return true
		}
	}
	return false
}
