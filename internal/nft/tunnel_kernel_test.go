package nft

import (
	"context"
	"log/slog"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/netlink"
	"github.com/rforced/ostiole/internal/netnstest"
	"github.com/rforced/ostiole/internal/policy"
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
