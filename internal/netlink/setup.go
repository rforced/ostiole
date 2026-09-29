package netlink

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"time"

	"golang.org/x/sys/unix"
)

// What follows is for tests, which build a network in a namespace and ask
// the kernel about it. The daemon calls none of it.

// vethInfoPeer is VETH_INFO_PEER, from linux/veth.h.
const vethInfoPeer = 1

// AddVeth creates a veth pair.
func AddVeth(name, peer string) error {
	var e encoder
	e.ifinfomsg(0, 0, 0)
	e.ifname(name)
	e.nest(unix.IFLA_LINKINFO, func() {
		e.attr(unix.IFLA_INFO_KIND, []byte("veth"))
		e.nest(unix.IFLA_INFO_DATA, func() {
			e.nest(vethInfoPeer, func() {
				e.ifinfomsg(0, 0, 0)
				e.ifname(peer)
			})
		})
	})
	return request(unix.RTM_NEWLINK, unix.NLM_F_CREATE|unix.NLM_F_EXCL, &e, nil)
}

// SetLinkDown takes a device down.
func SetLinkDown(index int) error {
	var e encoder
	e.ifinfomsg(index, 0, unix.IFF_UP)
	return request(unix.RTM_NEWLINK, 0, &e, nil)
}

// SetLinkMTU sets a device's MTU.
func SetLinkMTU(index, mtu int) error {
	var e encoder
	e.ifinfomsg(index, 0, 0)
	e.u32(unix.IFLA_MTU, e.n32(mtu, "MTU"))
	return request(unix.RTM_NEWLINK, 0, &e, nil)
}

// SetLinkNamespace moves a device into the network namespace the file
// descriptor ns holds open.
func SetLinkNamespace(index, ns int) error {
	var e encoder
	e.ifinfomsg(index, 0, 0)
	e.u32(unix.IFLA_NET_NS_FD, e.n32(ns, "namespace"))
	return request(unix.RTM_NEWLINK, 0, &e, nil)
}

// AddAddr puts an address on a device. An IPv4 one shorter than /31 gets
// the broadcast address of its prefix, as `ip addr add … brd +` gives it.
func AddAddr(index int, p netip.Prefix) error {
	return changeAddr(unix.RTM_NEWADDR, unix.NLM_F_CREATE|unix.NLM_F_EXCL, index, p)
}

// DeleteAddr takes an address off a device.
func DeleteAddr(index int, p netip.Prefix) error {
	return changeAddr(unix.RTM_DELADDR, 0, index, p)
}

func changeAddr(typ, flags uint16, index int, p netip.Prefix) error {
	a := p.Addr().Unmap()
	fam := unix.AF_INET6
	if a.Is4() {
		fam = unix.AF_INET
	}
	raw := a.AsSlice()
	var e encoder
	e.ifaddrmsg(fam, p.Bits(), index)
	e.attr(unix.IFA_LOCAL, raw)
	e.attr(unix.IFA_ADDRESS, raw)
	if typ == unix.RTM_NEWADDR && a.Is4() && p.Bits() < 31 {
		mask := net.CIDRMask(p.Bits(), 32)
		brd := make([]byte, 4)
		for i := range brd {
			brd[i] = raw[i] | ^mask[i]
		}
		e.attr(unix.IFA_BROADCAST, brd)
	}
	return request(typ, flags, &e, nil)
}

// AddNeighbour adds an entry to the ARP or NDP table.
func AddNeighbour(n Neighbour) error {
	var e encoder
	e.ndmsg(n.Family, n.LinkIndex, n.State, n.Flags)
	e.ip(unix.NDA_DST, n.Family, n.IP)
	if n.HardwareAddr != nil {
		e.attr(unix.NDA_LLADDR, n.HardwareAddr)
	}
	return request(unix.RTM_NEWNEIGH, unix.NLM_F_CREATE|unix.NLM_F_EXCL, &e, nil)
}

// RouteQuery asks where the kernel would send a packet, as `ip route get`
// does.
type RouteQuery struct {
	Dst net.IP
	// Src, Iif and Mark say more about the packet: where it came from, the
	// device it arrived on, and the mark the firewall gave it.
	Src  net.IP
	Iif  int
	Mark uint32
	// IPProto and Sport are its transport and source port, which rules
	// can match.
	IPProto uint8
	Sport   uint16
}

// RouteGet answers a query with the route the kernel chose, and the table
// it came from.
func RouteGet(q RouteQuery) ([]Route, error) {
	fam := family(q.Dst)
	bits := uint8(128)
	if fam == unix.AF_INET {
		bits = 32
	}
	var e encoder
	h := rtmsg{family: e.n8(fam, "family"), dstLen: bits, flags: unix.RTM_F_LOOKUP_TABLE}
	if q.Src != nil {
		h.srcLen = bits
	}
	e.rtmsg(h)
	e.ip(unix.RTA_DST, fam, q.Dst)
	if q.Iif > 0 {
		e.u32(unix.RTA_IIF, e.n32(q.Iif, "link index"))
	}
	if q.Src != nil {
		e.ip(unix.RTA_SRC, fam, q.Src)
	}
	if q.Mark > 0 {
		e.u32(unix.RTA_MARK, q.Mark)
	}
	if q.IPProto != 0 {
		e.attr(unix.RTA_IP_PROTO, []byte{q.IPProto})
	}
	// In network order, as it is in the packet.
	if q.Sport != 0 {
		e.attr(unix.RTA_SPORT, binary.BigEndian.AppendUint16(nil, q.Sport))
	}
	var out []Route
	err := request(unix.RTM_GETROUTE, 0, &e, func(m message) error {
		if m.typ != unix.RTM_NEWROUTE {
			return nil
		}
		r, err := parseRoute(m.data)
		if err == nil {
			out = append(out, r)
		}
		return err
	})
	return out, err
}

// RouteUpdate is a route the kernel announced it added or removed.
type RouteUpdate struct {
	Route
	Deleted bool
}

// WatchRoutes reports the route changes of both families until ctx is
// done, then closes the channel.
func WatchRoutes(ctx context.Context) (<-chan RouteUpdate, error) {
	c, err := dial(unix.NETLINK_ROUTE, unix.RTMGRP_IPV4_ROUTE|unix.RTMGRP_IPV6_ROUTE)
	if err != nil {
		return nil, err
	}
	out := make(chan RouteUpdate)
	stop := context.AfterFunc(ctx, func() { _ = c.f.SetReadDeadline(time.Now()) })
	go func() {
		defer close(out)
		defer func() { _ = c.close() }()
		defer stop()
		for {
			msgs, err := c.receive()
			if errors.Is(err, unix.ENOBUFS) {
				continue
			}
			if err != nil {
				return
			}
			for _, m := range msgs {
				if m.typ != unix.RTM_NEWROUTE && m.typ != unix.RTM_DELROUTE {
					continue
				}
				r, err := parseRoute(m.data)
				if err != nil {
					continue
				}
				select {
				case out <- RouteUpdate{Route: r, Deleted: m.typ == unix.RTM_DELROUTE}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// WireGuardPeerConfig is a peer as a test sets it.
type WireGuardPeerConfig struct {
	PublicKey  []byte
	Endpoint   netip.AddrPort
	AllowedIPs []netip.Prefix
}

// SetWireGuard gives a WireGuard device its key, port and peers, as
// `wg set` does.
func SetWireGuard(name string, privateKey []byte, port uint16, peers []WireGuardPeerConfig) error {
	c, err := dial(unix.NETLINK_GENERIC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = c.close() }()
	family, err := genlFamily(c, unix.WG_GENL_NAME)
	if err != nil {
		return err
	}
	var e encoder
	e.genlmsghdr(unix.WG_CMD_SET_DEVICE)
	e.attr(unix.WGDEVICE_A_IFNAME, append([]byte(name), 0))
	e.attr(unix.WGDEVICE_A_PRIVATE_KEY, privateKey)
	e.attr(unix.WGDEVICE_A_LISTEN_PORT, native.AppendUint16(nil, port))
	e.nest(unix.WGDEVICE_A_PEERS|unix.NLA_F_NESTED, func() {
		for i, p := range peers {
			e.nest(uint16(i)|unix.NLA_F_NESTED, func() {
				e.attr(unix.WGPEER_A_PUBLIC_KEY, p.PublicKey)
				if p.Endpoint.IsValid() {
					e.attr(unix.WGPEER_A_ENDPOINT, sockaddrOf(p.Endpoint))
				}
				e.nest(unix.WGPEER_A_ALLOWEDIPS|unix.NLA_F_NESTED, func() {
					for j, pre := range p.AllowedIPs {
						e.nest(uint16(j)|unix.NLA_F_NESTED, func() {
							fam := uint16(unix.AF_INET6)
							if pre.Addr().Is4() {
								fam = unix.AF_INET
							}
							e.attr(unix.WGALLOWEDIP_A_FAMILY, native.AppendUint16(nil, fam))
							e.attr(unix.WGALLOWEDIP_A_IPADDR, pre.Addr().AsSlice())
							e.attr(unix.WGALLOWEDIP_A_CIDR_MASK, []byte{byte(pre.Bits())}) //nolint:gosec // a prefix length, 0 to 128
						})
					}
				})
			})
		}
	})
	if e.err != nil {
		return e.err
	}
	return c.exchange(family, unix.NLM_F_ACK, e.b, nil)
}

// sockaddrOf writes an endpoint as the struct sockaddr_in or sockaddr_in6
// WireGuard takes.
func sockaddrOf(ap netip.AddrPort) []byte {
	a := ap.Addr()
	if a.Is4() {
		b := native.AppendUint16(nil, unix.AF_INET)
		b = binary.BigEndian.AppendUint16(b, ap.Port())
		b = append(b, a.AsSlice()...)
		return append(b, make([]byte, 8)...)
	}
	b := native.AppendUint16(nil, unix.AF_INET6)
	b = binary.BigEndian.AppendUint16(b, ap.Port())
	b = append(b, 0, 0, 0, 0)
	b = append(b, a.AsSlice()...)
	return append(b, 0, 0, 0, 0)
}
