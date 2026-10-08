package nft

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
	"ostiole/internal/policy"
)

// A tunnel gateway holds what its rule sends it before the daemon has
// routed it, as at boot: with no ip rules the TV's SYN takes the main
// table towards eth0 and the kill switch drops it there. After one pass of
// the policy installer the next SYN goes into the tunnel, from the
// tunnel's own address and offering a segment size the tunnel takes.
func TestATunnelGatewayHoldsItsTrafficUntilItIsRoutedInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	cfg := loadConfig(t, "testdata/tunnel-gateway.json")
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// eth1 is the LAN, fed from its veth peer. eth0 holds the main table's
	// default route and wg1 stands in for the tunnel: dummies, which drop
	// what they are given once a packet socket has seen it leave.
	lan, peer := vethPair(t, "eth1", "peer1", "192.168.1.1/24")
	wan := netnstest.Dummy(t, "eth0", "203.0.113.2/24")
	tun := netnstest.Dummy(t, "wg1", "10.66.1.2/32")
	const tunnelMTU = 1420
	if err := netlink.SetLinkMTU(tun.Index, tunnelMTU); err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddRoute(netlink.Route{LinkIndex: wan.Index, Gw: net.ParseIP("203.0.113.1")}); err != nil {
		t.Fatal(err)
	}
	forwardIPv4(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	outWAN := capture(t, wan)
	outTun := capture(t, tun)
	toSite := func(s tcpSegment) bool { return s.dport == 443 }
	mss1460 := []byte{2, 4, 0x05, 0xb4}
	send := func(sport uint16) {
		sendFrame(t, peer, lan, synFrame(lan.HardwareAddr, peer.HardwareAddr,
			net.ParseIP("192.168.1.50"), net.ParseIP("198.18.0.7"), sport, 443, mss1460...))
	}

	send(40000)
	if n := waitForCounter(ctx, t, x, "forward/kill-switch:vpn", 1); n != 1 {
		t.Fatalf("the kill switch counted %d packets, want 1", n)
	}
	if s, ok := nextSegment(t, outWAN, toSite, 300*time.Millisecond); ok {
		t.Errorf("%+v left by the WAN", s)
	}

	hops := map[string]policy.Hop{}
	for _, g := range cfg.Gateways {
		address := ""
		if g.Name == "wan" {
			address = "203.0.113.1"
		}
		hops[g.Name] = policy.NewHop(cfg, g, address, true)
	}
	if err := policy.NewInstaller(slog.New(slog.DiscardHandler)).Sync(policy.Plan(cfg, hops)); err != nil {
		t.Fatal(err)
	}
	send(40001)
	tv := syscall.NsecToTimeval((3 * time.Second).Nanoseconds())
	if err := syscall.SetsockoptTimeval(outTun, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	for {
		n, _, err := syscall.Recvfrom(outTun, buf, 0)
		if err != nil {
			t.Fatalf("after the installer's pass, no SYN went into the tunnel: %v", err)
		}
		s, ok := parseTCP(buf[:n])
		if !ok || !toSite(s) {
			continue
		}
		if !s.src.Equal(net.ParseIP("10.66.1.2")) {
			t.Errorf("the SYN went into the tunnel from %s, want the tunnel's own address", s.src)
		}
		if mss, _ := synMSS(buf[:n], 443); mss != tunnelMTU-40 {
			t.Errorf("the SYN offered MSS %d, want %d", mss, tunnelMTU-40)
		}
		break
	}
	if s, ok := nextSegment(t, outWAN, toSite, 300*time.Millisecond); ok {
		t.Errorf("%+v left by the WAN", s)
	}
}

// udpTo reads the source and destination port of an Ethernet frame
// carrying an IPv4 UDP datagram to dst.
func udpTo(frame []byte, dst net.IP) (src net.IP, port uint16, ok bool) {
	if len(frame) < 14+20+8 || binary.BigEndian.Uint16(frame[12:]) != syscall.ETH_P_IP {
		return nil, 0, false
	}
	ip := frame[14:]
	ihl := int(ip[0]&0x0f) * 4
	if ip[9] != syscall.IPPROTO_UDP || len(ip) < ihl+8 || !net.IP(ip[16:20]).Equal(dst) {
		return nil, 0, false
	}
	return net.IP(append([]byte{}, ip[12:16]...)), binary.BigEndian.Uint16(ip[ihl+2:]), true
}

// nextUDP reads fd until a datagram to dst on port arrives, or the wait
// runs out, and returns where it came from.
func nextUDP(t *testing.T, fd int, dst net.IP, port uint16, wait time.Duration) (net.IP, bool) {
	t.Helper()
	tv := syscall.NsecToTimeval(wait.Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, false
		}
		if src, p, ok := udpTo(buf[:n], dst); ok && p == port {
			return src, true
		}
	}
	return nil, false
}

// The router's own lookups, sent through a tunnel gateway: a resolver's
// query is marked as it leaves, held by the kill switch while no table
// routes the mark, as at boot, and once the installer has run it goes
// into the tunnel from the tunnel's own address. The test stands in for
// the resolver, its uid the resolver's. Anything else it sends keeps the
// main table.
func TestTheRoutersLookupsGoThroughTheirGatewayInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	cfg := loadConfig(t, "testdata/dns-via.json")
	built, err := BuildEnv(cfg, Env{ResolverUIDs: []uint32{uint32(os.Getuid())}})
	if err != nil {
		t.Fatal(err)
	}
	wan := netnstest.Dummy(t, "eth0", "203.0.113.2/24")
	tun := netnstest.Dummy(t, "wg1", "10.66.1.2/32")
	if err := netlink.AddRoute(netlink.Route{LinkIndex: wan.Index, Gw: net.ParseIP("203.0.113.1")}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, built.Ruleset); err != nil {
		t.Fatal(err)
	}
	outWAN := capture(t, wan)
	outTun := capture(t, tun)
	upstream := net.ParseIP("9.9.9.9")
	send := func(port int) {
		c, err := net.Dial("udp4", net.JoinHostPort(upstream.String(), strconv.Itoa(port)))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		// The kill switch's drop comes back as an error; it is the point.
		_, _ = c.Write([]byte("query"))
	}

	send(53)
	if n := waitForCounter(ctx, t, x, "postrouting/kill-switch:vpn", 1); n != 1 {
		t.Fatalf("the kill switch counted %d lookups, want 1", n)
	}
	if src, ok := nextUDP(t, outWAN, upstream, 53, 300*time.Millisecond); ok {
		t.Errorf("a lookup left by the WAN from %s", src)
	}
	send(123)
	if _, ok := nextUDP(t, outWAN, upstream, 123, 3*time.Second); !ok {
		t.Error("what is not a lookup did not keep the main table")
	}

	hops := map[string]policy.Hop{}
	for _, g := range cfg.Gateways {
		address := ""
		if g.Name == "wan" {
			address = "203.0.113.1"
		}
		hops[g.Name] = policy.NewHop(cfg, g, address, true)
	}
	if err := policy.NewInstaller(slog.New(slog.DiscardHandler)).Sync(policy.Plan(cfg, hops)); err != nil {
		t.Fatal(err)
	}
	send(53)
	if src, ok := nextUDP(t, outTun, upstream, 53, 3*time.Second); !ok {
		t.Error("after the installer's pass, no lookup went into the tunnel")
	} else if !src.Equal(net.ParseIP("10.66.1.2")) {
		t.Errorf("the lookup went into the tunnel from %s, want the tunnel's own address", src)
	}
	if src, ok := nextUDP(t, outWAN, upstream, 53, 300*time.Millisecond); ok {
		t.Errorf("a lookup left by the WAN from %s", src)
	}
}
