package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"syscall"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

const (
	ssdpPort    = 1900
	maxDatagram = 9216
)

var (
	mdnsGroup4 = netip.MustParseAddr("224.0.0.251")
	mdnsGroup6 = netip.MustParseAddr("ff02::fb")
	ssdpGroup  = netip.MustParseAddr("239.255.255.250")
	broadcast4 = netip.MustParseAddr("255.255.255.255")
)

// conn is a UDP socket of either family that reads and writes with the interface named.
type conn interface {
	read(b []byte) (n, ifindex int, src netip.AddrPort, dst netip.Addr, err error)
	write(b []byte, ifindex int, dst netip.AddrPort) error
	join(ifi *net.Interface, group netip.Addr) error
	Close() error
}

type conn4 struct{ p *ipv4.PacketConn }

func (c conn4) read(b []byte) (int, int, netip.AddrPort, netip.Addr, error) {
	n, cm, src, err := c.p.ReadFrom(b)
	if err != nil {
		return 0, 0, netip.AddrPort{}, netip.Addr{}, err
	}
	var ifindex int
	var dst netip.Addr
	if cm != nil {
		ifindex = cm.IfIndex
		dst, _ = netip.AddrFromSlice(cm.Dst)
	}
	return n, ifindex, addrPort(src), dst.Unmap(), nil
}

func (c conn4) write(b []byte, ifindex int, dst netip.AddrPort) error {
	_, err := c.p.WriteTo(b, &ipv4.ControlMessage{IfIndex: ifindex}, net.UDPAddrFromAddrPort(dst))
	return err
}

func (c conn4) join(ifi *net.Interface, group netip.Addr) error {
	return c.p.JoinGroup(ifi, &net.UDPAddr{IP: group.AsSlice()})
}

func (c conn4) Close() error { return c.p.Close() }

type conn6 struct{ p *ipv6.PacketConn }

func (c conn6) read(b []byte) (int, int, netip.AddrPort, netip.Addr, error) {
	n, cm, src, err := c.p.ReadFrom(b)
	if err != nil {
		return 0, 0, netip.AddrPort{}, netip.Addr{}, err
	}
	var ifindex int
	var dst netip.Addr
	if cm != nil {
		ifindex = cm.IfIndex
		dst, _ = netip.AddrFromSlice(cm.Dst)
	}
	return n, ifindex, addrPort(src), dst, nil
}

func (c conn6) write(b []byte, ifindex int, dst netip.AddrPort) error {
	_, err := c.p.WriteTo(b, &ipv6.ControlMessage{IfIndex: ifindex}, net.UDPAddrFromAddrPort(dst))
	return err
}

func (c conn6) join(ifi *net.Interface, group netip.Addr) error {
	return c.p.JoinGroup(ifi, &net.UDPAddr{IP: group.AsSlice()})
}

func (c conn6) Close() error { return c.p.Close() }

func addrPort(a net.Addr) netip.AddrPort {
	u, ok := a.(*net.UDPAddr)
	if !ok {
		return netip.AddrPort{}
	}
	ap := u.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap().WithZone(""), ap.Port())
}

// sockOpts are set before bind.
type sockOpts struct {
	broadcast bool
	v6only    bool
}

func listen(network, address string, o sockOpts) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		err := rc.Control(func(fd uintptr) {
			set := func(level, opt, v int) {
				if serr == nil {
					serr = unix.SetsockoptInt(int(fd), level, opt, v)
				}
			}
			set(unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			set(unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			if o.broadcast {
				set(unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
			}
			// Only the groups this socket joined, not every group the host joined anywhere.
			if o.v6only {
				set(unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 1)
				set(unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_ALL, 0)
			} else {
				set(unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
			}
		})
		return errors.Join(err, serr)
	}}
	pc, err := lc.ListenPacket(context.Background(), network, address)
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", address, err)
	}
	return pc, nil
}

// listenGroup4 binds a udp4 socket to a multicast group itself. net's
// ListenPacket widens a group address to the wildcard, and a wildcard
// socket on 1900 takes the unicast searches meant for a miniupnpd that
// bound after it: the kernel hands unicast to the first reuseport group it
// meets, not the best match.
func listenGroup4(group netip.Addr, port int) (net.PacketConn, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("socket for %s: %w", group, err)
	}
	sa := &unix.SockaddrInet4{Port: port, Addr: group.As4()}
	err = errors.Join(
		unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1),
		unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1),
		unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0),
		unix.Bind(fd, sa),
	)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind %s:%d: %w", group, port, err)
	}
	f := os.NewFile(uintptr(fd), group.String())
	defer func() { _ = f.Close() }()
	pc, err := net.FilePacketConn(f)
	if err != nil {
		return nil, fmt.Errorf("bind %s:%d: %w", group, port, err)
	}
	return pc, nil
}

// listen4 binds a udp4 socket with packet info on, its multicast loopback off and the TTLs given.
// A multicast address is bound as itself, not widened to the wildcard.
func listen4(address string, o sockOpts, hops, mcastHops int) (conn4, error) {
	var pc net.PacketConn
	var err error
	if ap, perr := netip.ParseAddrPort(address); perr == nil && ap.Addr().Is4() && ap.Addr().IsMulticast() {
		pc, err = listenGroup4(ap.Addr(), int(ap.Port()))
	} else {
		pc, err = listen("udp4", address, o)
	}
	if err != nil {
		return conn4{}, err
	}
	p := ipv4.NewPacketConn(pc)
	err = errors.Join(
		p.SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true),
		p.SetMulticastLoopback(false),
		p.SetTTL(hops),
		p.SetMulticastTTL(mcastHops),
	)
	if err != nil {
		_ = p.Close()
		return conn4{}, fmt.Errorf("set up %s: %w", address, err)
	}
	return conn4{p}, nil
}

// listen6 is listen4 for udp6, V6ONLY.
func listen6(address string, hops int) (conn6, error) {
	pc, err := listen("udp6", address, sockOpts{v6only: true})
	if err != nil {
		return conn6{}, err
	}
	p := ipv6.NewPacketConn(pc)
	err = errors.Join(
		p.SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true),
		p.SetMulticastLoopback(false),
		p.SetHopLimit(hops),
		p.SetMulticastHopLimit(hops),
	)
	if err != nil {
		_ = p.Close()
		return conn6{}, fmt.Errorf("set up %s: %w", address, err)
	}
	return conn6{p}, nil
}
