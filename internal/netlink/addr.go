package netlink

import (
	"context"
	"errors"
	"net"
	"slices"
	"time"

	"golang.org/x/sys/unix"
)

// Addr is one address on a link.
type Addr struct {
	LinkIndex int
	// IPNet is the address and its prefix. On a point-to-point link it is
	// the local end as a host address, and Peer is the far end.
	IPNet *net.IPNet
	Peer  *net.IPNet
	// Flags are the kernel's IFA_F_* bits.
	Flags uint32
}

// Addrs lists the addresses of one family on every link: unix.AF_INET,
// unix.AF_INET6, or unix.AF_UNSPEC for both.
func Addrs(fam int) ([]Addr, error) {
	var e encoder
	e.ifaddrmsg(fam, 0, 0)
	msgs, err := dump(unix.RTM_GETADDR, &e)
	if err != nil {
		return nil, err
	}
	var out []Addr
	for _, m := range msgs {
		if m.typ != unix.RTM_NEWADDR || (fam != unix.AF_UNSPEC && len(m.data) > 0 && int(m.data[0]) != fam) {
			continue
		}
		a, ok, err := parseAddr(m.data)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, a)
		}
	}
	return out, nil
}

// parseAddr reads one address. IPv4 sends the local address as IFA_LOCAL
// and the peer as IFA_ADDRESS, the same address twice when there is no
// peer; IPv6 sends IFA_ADDRESS alone unless there is one.
func parseAddr(b []byte) (Addr, bool, error) {
	if len(b) < unix.SizeofIfAddrmsg {
		return Addr{}, false, errors.New("netlink: a short address")
	}
	fam, prefixlen := int(b[0]), int(b[1])
	if fam != unix.AF_INET && fam != unix.AF_INET6 {
		return Addr{}, false, nil
	}
	a := Addr{Flags: uint32(b[2]), LinkIndex: int(native.Uint32(b[4:]))}
	as, err := attrs(b[unix.SizeofIfAddrmsg:])
	if err != nil {
		return Addr{}, false, err
	}
	var local, address net.IP
	for _, at := range as {
		switch at.typ {
		case unix.IFA_ADDRESS:
			address = ip(at.data)
		case unix.IFA_LOCAL:
			local = ip(at.data)
		case unix.IFA_FLAGS:
			a.Flags = u32(at.data)
		}
	}
	prefix := func(x net.IP, ones int) *net.IPNet {
		return &net.IPNet{IP: x, Mask: net.CIDRMask(ones, 8*len(x))}
	}
	switch {
	case local != nil && address != nil && fam == unix.AF_INET && local.Equal(address):
		a.IPNet = prefix(address, prefixlen)
	case local != nil:
		a.IPNet = prefix(local, 8*len(local))
		if address != nil {
			a.Peer = prefix(address, prefixlen)
		}
	case address != nil:
		a.IPNet = prefix(address, prefixlen)
	default:
		return Addr{}, false, nil
	}
	return a, true, nil
}

// WatchAddrs signals each time an address is added to, changed on or
// removed from any link, until ctx is done; then it closes the channel.
// Signals coalesce, so a reader that is busy sees one for many changes,
// and a message the socket had no room for is signalled too: whatever it
// said is unknown.
func WatchAddrs(ctx context.Context) (<-chan struct{}, error) {
	c, err := dial(unix.NETLINK_ROUTE, unix.RTMGRP_IPV4_IFADDR|unix.RTMGRP_IPV6_IFADDR)
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
			case !slices.ContainsFunc(msgs, func(m message) bool {
				return m.typ == unix.RTM_NEWADDR || m.typ == unix.RTM_DELADDR
			}):
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
