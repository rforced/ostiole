package nft

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
)

// A SYN carrying IPv4 options is what reached CVE-2026-98432's
// use-after-free. The kernel runs the ruleset, a LAN host's SYNs head for
// the internet, and only the plain one leaves, translated to the WAN's
// address. The IGMP report sent first carries Router Alert, an option too,
// and is not counted.
func TestRawChainDropsTCPWithIPOptionsInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	ruleset, err := Render(loadConfig(t, "testdata/minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	lan, peer := vethPair(t, "eth1", "peer1", "192.168.1.1/24")
	wan := netnstest.Dummy(t, "eth0", "198.51.100.2/24")
	if err := netlink.AddRoute(netlink.Route{LinkIndex: wan.Index, Gw: net.ParseIP("198.51.100.1")}); err != nil {
		t.Fatal(err)
	}
	forwardIPv4(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	out := capture(t, wan)

	host, dst := net.ParseIP("192.168.1.10"), net.ParseIP("203.0.113.5")
	recordRoute := []byte{7, 7, 4, 0, 0, 0, 0, 0}
	timestamp := []byte{68, 8, 5, 0, 0, 0, 0, 0}
	report := []byte{0x22, 0, 0, 0, 0, 0, 0, 1, 4, 0, 0, 0, 239, 1, 2, 3}
	binary.BigEndian.PutUint16(report[2:], checksum(report))
	igmp := ipv4Frame(net.HardwareAddr{1, 0, 0x5e, 0, 0, 0x16}, peer.HardwareAddr, syscall.IPPROTO_IGMP,
		host, net.ParseIP("224.0.0.22"), []byte{0x94, 4, 0, 0}, report)
	for _, frame := range [][]byte{
		igmp,
		withIPOptions(synFrame(lan.HardwareAddr, peer.HardwareAddr, host, dst, 40001, 443), recordRoute),
		withIPOptions(synFrame(lan.HardwareAddr, peer.HardwareAddr, host, dst, 40002, 443), timestamp),
		synFrame(lan.HardwareAddr, peer.HardwareAddr, host, dst, 40003, 443),
	} {
		sendFrame(t, peer, lan, frame)
	}

	var left []tcpSegment
	for _, f := range departures(t, out, func(f []byte) bool {
		s, ok := parseTCP(f)
		return ok && s.sport == 40003
	}) {
		if s, ok := parseTCP(f); ok && s.dport == 443 {
			left = append(left, s)
		}
	}
	if len(left) != 1 || left[0].sport != 40003 || !left[0].src.Equal(net.ParseIP("198.51.100.2")) {
		t.Errorf("left by the WAN: %+v, want only the plain SYN, from the WAN's address", left)
	}
	if n := waitForCounter(ctx, t, x, RawChain+"/"+kindIPOptions, 2); n != 2 {
		t.Errorf("%s counted %d packets, want the two SYNs", kindIPOptions, n)
	}
}

// An inside link's IPv6 sources are held to its routes, and a type 0
// routing header is dropped from anyone. The kernel runs the ruleset with
// a LAN on 2001:db8:1::/64 with a router behind it for 2001:db8:66::/48:
// the LAN's host and the network behind the router reach the WAN, a
// source from another network does not, and neither does the host's
// routed packet.
// Neighbour discovery from the unspecified address and from link-local
// addresses is not counted.
func TestRawChainHoldsIPv6SourcesToTheirLinkInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	cfg := loadConfig(t, "testdata/minimal.json")
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "eth1" {
			cfg.Interfaces[i].IPv6 = model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:1::1/64"}
		}
	}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddVeth("eth1", "peer1"); err != nil {
		t.Fatalf("add veth: %v", err)
	}
	for path, value := range map[string]string{
		"/proc/sys/net/ipv6/conf/all/forwarding":  "1",
		"/proc/sys/net/ipv6/conf/eth1/accept_dad": "0",
	} {
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	lan, peer := netnstest.Link(t, "eth1"), netnstest.Link(t, "peer1")
	for _, l := range []netlink.Link{lan, peer} {
		if err := netlink.SetLinkUp(l.Index); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.AddAddr(lan.Index, netip.MustParsePrefix("2001:db8:1::1/64")); err != nil {
		t.Fatal(err)
	}
	wan := netnstest.Dummy(t, "eth0", "2001:db8:ff::2/64")
	if err := netlink.AddRoute(netlink.Route{Family: syscall.AF_INET6, LinkIndex: wan.Index, Gw: net.ParseIP("2001:db8:ff::1")}); err != nil {
		t.Fatal(err)
	}
	_, behind, _ := net.ParseCIDR("2001:db8:66::/48")
	if err := netlink.AddRoute(netlink.Route{Family: syscall.AF_INET6, Dst: behind, LinkIndex: lan.Index, Gw: net.ParseIP("2001:db8:1::20")}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	out := capture(t, wan)

	host, far := net.ParseIP("2001:db8:1::10"), net.ParseIP("2001:db8:ff::99")
	solicit := []byte{135, 0, 0, 0, 0, 0, 0, 0}
	solicit = append(solicit, host...)
	// A type 0 routing header with one address left to visit.
	rh0 := append([]byte{syscall.IPPROTO_UDP, 2, 0, 1, 0, 0, 0, 0}, far...)
	for _, frame := range [][]byte{
		udp6Frame(lan.HardwareAddr, peer.HardwareAddr, net.ParseIP("2001:db8:77::10"), far, nil, 40001),
		ipv6Frame(net.HardwareAddr{0x33, 0x33, 0xff, 0, 0, 0x10}, peer.HardwareAddr, net.IPv6unspecified,
			net.ParseIP("ff02::1:ff00:10"), syscall.IPPROTO_ICMPV6, 255, solicit),
		ipv6Frame(net.HardwareAddr{0x33, 0x33, 0, 0, 0, 1}, peer.HardwareAddr, net.ParseIP("fe80::10"),
			net.ParseIP("ff02::1"), syscall.IPPROTO_ICMPV6, 255, []byte{136, 0, 0, 0, 0, 0, 0, 0}),
		udp6Frame(lan.HardwareAddr, peer.HardwareAddr, host, net.ParseIP("2001:db8:ff::98"), rh0, 40002),
		udp6Frame(lan.HardwareAddr, peer.HardwareAddr, net.ParseIP("2001:db8:66::10"), far, nil, 40004),
		udp6Frame(lan.HardwareAddr, peer.HardwareAddr, host, far, nil, 40003),
	} {
		sendFrame(t, peer, lan, frame)
	}

	var left []string
	for _, f := range departures(t, out, func(f []byte) bool {
		src, sport, ok := udp6Source(f)
		return ok && src.Equal(host) && sport == 40003
	}) {
		if src, sport, ok := udp6Source(f); ok {
			left = append(left, net.JoinHostPort(src.String(), strconv.Itoa(int(sport))))
		}
	}
	if !slices.Equal(left, []string{"[2001:db8:66::10]:40004", "[2001:db8:1::10]:40003"}) {
		t.Errorf("left by the WAN: %q, want only the datagrams from the LAN and the network behind it", left)
	}
	for key, want := range map[string]uint64{kindSpoofed: 1, kindRoutingHeader: 1} {
		if n := waitForCounter(ctx, t, x, RawChain+"/"+key, want); n != want {
			t.Errorf("%s counted %d packets, want %d", key, n, want)
		}
	}
}

// ipv4Frame builds an Ethernet frame carrying one IPv4 packet with the
// given options, whole 32-bit words, and a hop limit of one.
func ipv4Frame(dstMAC, srcMAC net.HardwareAddr, proto byte, src, dst net.IP, opts, payload []byte) []byte {
	ip := make([]byte, 20+len(opts))
	ip[0] = 0x40 | byte(len(ip)/4)
	binary.BigEndian.PutUint16(ip[2:], uint16(len(ip)+len(payload)))
	ip[8], ip[9] = 1, proto
	copy(ip[12:], src.To4())
	copy(ip[16:], dst.To4())
	copy(ip[20:], opts)
	binary.BigEndian.PutUint16(ip[10:], checksum(ip))
	frame := append(append(append([]byte{}, dstMAC...), srcMAC...), 0x08, 0x00)
	return append(append(frame, ip...), payload...)
}

// withIPOptions puts options, whole 32-bit words, into the IPv4 header of
// a frame tcpFrame built. The TCP checksum does not cover the IP header's
// length, so it stays right.
func withIPOptions(frame, opts []byte) []byte {
	ip := append(append([]byte{}, frame[14:34]...), opts...)
	ip[0] = 0x40 | byte(len(ip)/4)
	binary.BigEndian.PutUint16(ip[2:], uint16(len(ip)+len(frame)-34))
	ip[10], ip[11] = 0, 0
	binary.BigEndian.PutUint16(ip[10:], checksum(ip))
	return append(append(append([]byte{}, frame[:14]...), ip...), frame[34:]...)
}

// ipv6Frame builds an Ethernet frame carrying one IPv6 packet whose first
// next header is next.
func ipv6Frame(dstMAC, srcMAC net.HardwareAddr, src, dst net.IP, next, hops byte, payload []byte) []byte {
	ip := make([]byte, 40)
	ip[0] = 0x60
	binary.BigEndian.PutUint16(ip[4:], uint16(len(payload)))
	ip[6], ip[7] = next, hops
	copy(ip[8:], src.To16())
	copy(ip[24:], dst.To16())
	frame := append(append(append([]byte{}, dstMAC...), srcMAC...), 0x86, 0xdd)
	return append(append(frame, ip...), payload...)
}

// udp6Frame builds an Ethernet frame carrying a UDP datagram to port 53,
// behind a routing header when rh is given, with a checksum conntrack
// accepts.
func udp6Frame(dstMAC, srcMAC net.HardwareAddr, src, dst net.IP, rh []byte, sport uint16) []byte {
	if rh == nil {
		return udp6Behind(dstMAC, srcMAC, src, dst, syscall.IPPROTO_UDP, nil, sport)
	}
	return udp6Behind(dstMAC, srcMAC, src, dst, syscall.IPPROTO_ROUTING, rh, sport)
}

// udp6Behind is udp6Frame behind any one extension header, which next
// names.
func udp6Behind(dstMAC, srcMAC net.HardwareAddr, src, dst net.IP, next byte, ext []byte, sport uint16) []byte {
	udp := make([]byte, 8+4)
	binary.BigEndian.PutUint16(udp[0:], sport)
	binary.BigEndian.PutUint16(udp[2:], 53)
	binary.BigEndian.PutUint16(udp[4:], uint16(len(udp)))
	pseudo := append(append(append([]byte{}, src.To16()...), dst.To16()...), 0, 0, 0, byte(len(udp)), 0, 0, 0, syscall.IPPROTO_UDP)
	binary.BigEndian.PutUint16(udp[6:], checksum(append(pseudo, udp...)))
	return ipv6Frame(dstMAC, srcMAC, src, dst, next, 64, append(append([]byte{}, ext...), udp...))
}

// mldQuery builds an MLDv1 general query from src to every node, as a
// querier sends it: hop limit one, Router Alert in a hop-by-hop header,
// answers asked for within 50 ms.
func mldQuery(srcMAC net.HardwareAddr, src net.IP) []byte {
	all := net.ParseIP("ff02::1")
	icmp := make([]byte, 24)
	icmp[0] = 130
	binary.BigEndian.PutUint16(icmp[4:], 50)
	pseudo := append(append(append([]byte{}, src.To16()...), all.To16()...), 0, 0, 0, byte(len(icmp)), 0, 0, 0, syscall.IPPROTO_ICMPV6)
	binary.BigEndian.PutUint16(icmp[2:], checksum(append(pseudo, icmp...)))
	hbh := []byte{syscall.IPPROTO_ICMPV6, 0, 5, 2, 0, 0, 1, 0}
	return ipv6Frame(net.HardwareAddr{0x33, 0x33, 0, 0, 0, 1}, srcMAC, src, all, syscall.IPPROTO_HOPOPTS, 1, append(hbh, icmp...))
}

// mldReport says whether an Ethernet frame is an MLDv1 report sent from
// mac.
func mldReport(f []byte, mac net.HardwareAddr) bool {
	return len(f) > 14+48 && bytes.Equal(f[6:12], mac) && binary.BigEndian.Uint16(f[12:]) == 0x86dd &&
		f[14+6] == syscall.IPPROTO_HOPOPTS && f[14+40] == syscall.IPPROTO_ICMPV6 && f[14+48] == 131
}

// edgeRouter loads cfg in the test's namespace with eth1 a LAN on
// 192.168.1.1/24 and 2001:db8:1::1/64, fed from its veth peer, and eth0 a
// WAN dummy holding the default route in both families. It returns the
// LAN's two ends and a socket that sees what leaves by the WAN.
func edgeRouter(ctx context.Context, t *testing.T, cfg *model.Config) (x *Exec, lan, peer netlink.Link, out int) {
	t.Helper()
	quietIPv6(t)
	routeAll(t)
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "eth1" {
			cfg.Interfaces[i].IPv6 = model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:1::1/64"}
		}
	}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	lan, peer = vethPair(t, "eth1", "peer1", "192.168.1.1/24")
	netnstest.AddAddr(t, lan.Index, netip.MustParsePrefix("2001:db8:1::1/64"))
	wan := netnstest.Dummy(t, "eth0", "198.51.100.2/24", "2001:db8:ff::2/64")
	defaultVia(t, "198.51.100.1", "eth0", 0)
	defaultVia(t, "2001:db8:ff::1", "eth0", 0)
	x = &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	return x, lan, peer, capture(t, wan)
}

// Packets no host sends are dropped before conntrack sees them, whatever
// the protection settings say. A LAN host's TCP with SYN and FIN, with
// SYN and RST and with no flags at all, its datagram behind a segment
// routing header, and datagrams from and to IPv4-mapped addresses never
// leave by the WAN, and each is counted. Its plain SYN and datagram leave.
// A datagram from ::1 never leaves either, but the kernel drops it itself
// before the hook (ip6_rcv_core), so that rule never counts one off the
// wire.
func TestRawChainDropsWhatNoHostSendsInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x, lan, peer, out := edgeRouter(ctx, t, loadConfig(t, "testdata/minimal.json"))

	host, dst := net.ParseIP("192.168.1.10"), net.ParseIP("203.0.113.5")
	host6, far := net.ParseIP("2001:db8:1::10"), net.ParseIP("2001:db8:ff::99")
	tcp := func(sport uint16, flags byte) []byte {
		return tcpFrame(lan.HardwareAddr, peer.HardwareAddr, host, dst, sport, 443, flags, 1, 0)
	}
	udp := func(src, dst net.IP, rh []byte, sport uint16) []byte {
		return udp6Frame(lan.HardwareAddr, peer.HardwareAddr, src, dst, rh, sport)
	}
	// A segment routing header with no segment left to visit.
	srh := append([]byte{syscall.IPPROTO_UDP, 2, 4, 0, 0, 0, 0, 0}, far...)
	for _, frame := range [][]byte{
		tcp(40001, tcpSYN|tcpFIN),
		tcp(40002, tcpSYN|tcpRST),
		tcp(40003, 0),
		udp(host6, far, srh, 40011),
		udp(host6, net.ParseIP("::ffff:203.0.113.5"), nil, 40012),
		udp(net.IPv6loopback, far, nil, 40013),
		udp(net.ParseIP("::ffff:192.0.2.1"), far, nil, 40014),
		tcp(40004, tcpSYN),
		udp(host6, far, nil, 40015),
	} {
		sendFrame(t, peer, lan, frame)
	}

	var left []uint16
	for _, f := range departures(t, out, func(f []byte) bool {
		_, sport, ok := udp6Source(f)
		return ok && sport == 40015
	}) {
		if s, ok := parseTCP(f); ok && s.dport == 443 {
			left = append(left, s.sport)
		} else if _, sport, ok := udp6Source(f); ok {
			left = append(left, sport)
		}
	}
	if !slices.Equal(left, []uint16{40004, 40015}) {
		t.Errorf("left by the WAN from ports %v, want only the plain SYN's 40004 and the plain datagram's 40015", left)
	}
	for key, want := range map[string]uint64{kindTCPFlags: 3, kindRoutingHeader: 1, kindImpossible: 2} {
		if n := waitForCounter(ctx, t, x, RawChain+"/"+key, want); n != want {
			t.Errorf("%s counted %d packets, want %d", key, n, want)
		}
	}
}

// Forwarded IPv6 with hop-by-hop options is dropped: nothing a router
// passes on needs them, and every router on the way would take each one
// down its slow path. What comes to the router itself still arrives, so
// MLD, whose queries carry Router Alert in one, keeps working. A LAN
// host's datagram behind a PadN option never leaves by the WAN and its
// plain one does; a querier's MLDv1 general query gets the router's
// MLDv1 reports back.
func TestForwardDropsHopByHopOptionsAndMLDStillWorksInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x, lan, peer, out := edgeRouter(ctx, t, loadConfig(t, "testdata/minimal.json"))
	near := capture(t, lan)

	host, far := net.ParseIP("2001:db8:1::10"), net.ParseIP("2001:db8:ff::99")
	padN := []byte{syscall.IPPROTO_UDP, 0, 1, 4, 0, 0, 0, 0}
	for _, frame := range [][]byte{
		udp6Behind(lan.HardwareAddr, peer.HardwareAddr, host, far, syscall.IPPROTO_HOPOPTS, padN, 40001),
		udp6Frame(lan.HardwareAddr, peer.HardwareAddr, host, far, nil, 40002),
		mldQuery(peer.HardwareAddr, net.ParseIP("fe80::5")),
	} {
		sendFrame(t, peer, lan, frame)
	}

	var plain, options int
	for _, f := range departures(t, out, func(f []byte) bool {
		_, sport, ok := udp6Source(f)
		return ok && sport == 40002
	}) {
		switch {
		case len(f) < 14+40 || binary.BigEndian.Uint16(f[12:]) != 0x86dd || !net.IP(f[14+24:14+40]).Equal(far):
		case f[14+6] == syscall.IPPROTO_HOPOPTS:
			options++
		case f[14+6] == syscall.IPPROTO_UDP:
			plain++
		}
	}
	if plain != 1 || options != 0 {
		t.Errorf("left by the WAN: %d plain datagrams and %d with hop-by-hop options, want one and none", plain, options)
	}
	if n := waitForCounter(ctx, t, x, "forward/"+kindHopByHop, 1); n != 1 {
		t.Errorf("%s counted %d packets, want 1", kindHopByHop, n)
	}
	answered := func(f []byte) bool { return mldReport(f, lan.HardwareAddr) }
	if !slices.ContainsFunc(departures(t, near, answered), answered) {
		t.Error("the router sent no MLDv1 report back to the query")
	}
}

// departures reads every frame leaving by the link fd watches, until the
// one done accepts and a moment after, or three seconds.
func departures(t *testing.T, fd int, done func([]byte) bool) [][]byte {
	t.Helper()
	var out [][]byte
	buf := make([]byte, 2048)
	for end := time.Now().Add(3 * time.Second); ; {
		left := time.Until(end)
		if left <= 0 {
			return out
		}
		tv := syscall.NsecToTimeval(left.Nanoseconds())
		if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
			t.Fatal(err)
		}
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return out
		}
		f := append([]byte{}, buf[:n]...)
		out = append(out, f)
		if done != nil && done(f) {
			done, end = nil, time.Now().Add(200*time.Millisecond)
		}
	}
}

// udp6Source reads the source address and port of an Ethernet frame
// carrying IPv6 UDP with no extension headers.
func udp6Source(f []byte) (net.IP, uint16, bool) {
	if len(f) < 14+40+8 || binary.BigEndian.Uint16(f[12:]) != 0x86dd || f[14+6] != syscall.IPPROTO_UDP {
		return nil, 0, false
	}
	return net.IP(append([]byte{}, f[14+8:14+24]...)), binary.BigEndian.Uint16(f[14+40:]), true
}
