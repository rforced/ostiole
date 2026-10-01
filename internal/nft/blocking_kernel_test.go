package nft

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/netlink"
	"github.com/rforced/ostiole/internal/netnstest"
)

// The kernel runs the ruleset of a router whose DNS server listens on the
// LAN alone. A query from the LAN to another resolver is redirected here. A
// query from the second internal zone leaves by the WAN for the resolver it
// was sent to: redirected, it would reach a port nothing answers on.
func TestPlainDNSIsRedirectedOnlyWhereTheServerListensInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	cfg := withVPNZone(loadConfig(t, "testdata/dns-blocking.json"))
	cfg.Services.DNS.Interfaces = []string{"eth1"}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}

	lan, lanPeer := vethPair(t, "eth1", "peer1", "192.168.1.1/24")
	vpn, vpnPeer := vethPair(t, "eth2", "peer2", "192.168.2.1/24")
	wan := netnstest.Dummy(t, "eth0", "203.0.113.2/24")
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
	out := capture(t, wan)
	resolver := net.ParseIP("198.51.100.53")
	toResolver := func(s tcpSegment) bool { return s.dport == 53 && s.dst.Equal(resolver) }

	sendFrame(t, vpnPeer, vpn, synFrame(vpn.HardwareAddr, vpnPeer.HardwareAddr,
		net.ParseIP("192.168.2.50"), resolver, 40000, 53))
	if _, ok := nextSegment(t, out, toResolver, 3*time.Second); !ok {
		t.Error("a query from the zone the DNS server does not listen on never left by the WAN")
	}

	sendFrame(t, lanPeer, lan, synFrame(lan.HardwareAddr, lanPeer.HardwareAddr,
		net.ParseIP("192.168.1.50"), resolver, 40001, 53))
	if n := waitForCounter(ctx, t, x, "nat_prerouting/block:dns-redirect", 1); n != 1 {
		t.Errorf("the redirect counted %d packets, want the LAN's 1", n)
	}
	if s, ok := nextSegment(t, out, toResolver, 300*time.Millisecond); ok {
		t.Errorf("%+v left by the WAN: the LAN's query was not redirected", s)
	}
}
