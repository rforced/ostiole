package nft

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/gateway"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
	"github.com/rforced/ostiole/internal/netnstest"
	"github.com/rforced/ostiole/internal/policy"
)

// twoLines builds the world of this file's tests around the router, the
// namespace the test runs in. eth0 and eth2 are two lines to one provider
// network, eth0's gateway ahead in the main table as networkd would have
// it, and eth1 is the LAN with one host. The provider network drops what
// arrives by a line its source is not routed back through, as providers
// do, so an answer that leaves by the wrong line never arrives. Past it
// sits a client.
func twoLines(t *testing.T) (lan, client *os.File) {
	t.Helper()
	quietIPv6(t)
	routeAll(t)
	// Loose, as the router's own sysctls have it.
	if err := os.WriteFile("/proc/sys/net/ipv4/conf/all/rp_filter", []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	lan, isp, client := netnstest.NewNS(t), netnstest.NewNS(t), netnstest.NewNS(t)
	for _, ns := range []*os.File{lan, isp, client} {
		netnstest.Do(t, ns, func() { quietIPv6(t) })
	}

	cable(t, "eth1", "lan0", lan)
	addrs(t, "eth1", "192.168.1.1/24", "2001:db8:10::1/64")
	netnstest.Do(t, lan, func() {
		addrs(t, "lan0", "192.168.1.10/24", "2001:db8:10::10/64")
		route(t, "default", "192.168.1.1", "lan0")
		route(t, "default6", "2001:db8:10::1", "lan0")
	})

	cable(t, "eth0", "isp1", isp)
	cable(t, "eth2", "isp2", isp)
	addrs(t, "eth0", "203.0.113.2/24", "2001:db8:1::2/64")
	addrs(t, "eth2", "198.51.100.2/24", "2001:db8:2::2/64")
	defaultVia(t, "203.0.113.1", "eth0", 10)
	defaultVia(t, "2001:db8:1::1", "eth0", 10)
	defaultVia(t, "198.51.100.1", "eth2", 20)
	defaultVia(t, "2001:db8:2::1", "eth2", 20)
	netnstest.Do(t, isp, func() {
		routeAll(t)
		addrs(t, "isp1", "203.0.113.1/24", "2001:db8:1::1/64")
		addrs(t, "isp2", "198.51.100.1/24", "2001:db8:2::1/64")
		// The LAN's IPv6 prefix is the second provider's.
		route(t, "2001:db8:10::/64", "2001:db8:2::2", "isp2")
		cable(t, "core0", "c0", client)
		addrs(t, "core0", "192.0.2.1/24", "2001:db8:ff::1/64")
		dropSpoofed(t)
	})
	netnstest.Do(t, client, func() {
		addrs(t, "c0", "192.0.2.9/24", "2001:db8:ff::9/64")
		route(t, "default", "192.0.2.1", "c0")
		route(t, "default6", "2001:db8:ff::1", "c0")
	})
	return lan, client
}

// dropSpoofed drops, in the namespace the call is made in, what arrives by
// a link its source is not routed back through, as a provider drops what
// its customer sends from an address that is not theirs.
func dropSpoofed(t *testing.T) {
	t.Helper()
	netnstest.Ruleset(t, `table inet isp {
	chain prerouting {
		type filter hook prerouting priority filter; policy accept;
		fib saddr . iif oif missing counter drop
	}
}`)
}

// defaultVia adds a default route through gw at a metric, in the namespace
// the call is made in.
func defaultVia(t *testing.T, gw, dev string, metric int) {
	t.Helper()
	ip := net.ParseIP(gw)
	dst := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	if ip.To4() == nil {
		dst = &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	}
	if err := netlink.AddRoute(netlink.Route{LinkIndex: netnstest.Link(t, dev).Index, Dst: dst, Gw: ip, Priority: metric}); err != nil {
		t.Fatalf("default via %s dev %s: %v", gw, dev, err)
	}
}

// twoLinesConfig is the router of twoLines: the LAN's host is forwarded
// on 8080 of both lines and takes IPv6 on 443 at its own address, and the
// router answers ping and 8443 on both. oneZone puts both lines in one
// zone, the other way a router with two WANs is set up.
func twoLinesConfig(oneZone bool) *model.Config {
	cfg := &model.Config{
		Version: model.SchemaVersion,
		System:  model.System{Hostname: "fw", Management: model.Management{WebPort: 443, SSHPort: 22}},
		Zones:   []model.Zone{{Name: "wan", External: true}, {Name: "lan", AntiLockout: true}},
		Interfaces: []model.Interface{
			{
				Name: "eth0", Zone: "wan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "203.0.113.2/24", Gateway: "203.0.113.1"},
				IPv6: model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:1::2/64", Gateway: "2001:db8:1::1"},
			},
			{
				Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.1.1/24"},
				IPv6: model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:10::1/64"},
			},
			{
				Name: "eth2", Zone: "wan2", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "198.51.100.2/24", Gateway: "198.51.100.1"},
				IPv6: model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:2::2/64", Gateway: "2001:db8:2::1"},
			},
		},
		Gateways: []model.Gateway{
			{Name: "first", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Monitor: "203.0.113.1"},
			{Name: "second", Enabled: true, Interface: "eth2", Address: "198.51.100.1", Monitor: "198.51.100.1", Priority: 1},
		},
		Rules: []model.Rule{{ID: "lan-out", Enabled: true, Zone: "lan", Action: model.ActionAccept, Protocol: model.ProtocolAny}},
		NAT:   model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
	}
	zones := []string{"wan", "wan2"}
	if oneZone {
		cfg.Interfaces[2].Zone, zones = "wan", zones[:1]
	} else {
		cfg.Zones = append(cfg.Zones, model.Zone{Name: "wan2", External: true})
	}
	for _, z := range zones {
		cfg.Rules = append(cfg.Rules,
			model.Rule{
				ID: z + "-ping", Enabled: true, Zone: z, Action: model.ActionAccept, Protocol: model.ProtocolICMP,
				Destination: model.Endpoint{Self: true},
			},
			model.Rule{
				ID: z + "-service", Enabled: true, Zone: z, Action: model.ActionAccept, Protocol: model.ProtocolTCP,
				Destination: model.Endpoint{Self: true, Ports: []string{"8443"}},
			},
			model.Rule{
				ID: z + "-web6", Enabled: true, Zone: z, Action: model.ActionAccept, Protocol: model.ProtocolTCP,
				Destination: model.Endpoint{Addresses: []string{"2001:db8:10::10"}, Ports: []string{"443"}},
			},
		)
		cfg.NAT.PortForwards = append(cfg.NAT.PortForwards, model.PortForward{
			ID: z + "-web", Enabled: true, Zone: z, Protocol: model.ProtocolTCP, Ports: []string{"8080"},
			Target: "192.168.1.10", TargetPort: "443",
		})
	}
	return cfg
}

// runRouter loads cfg's ruleset in the test's namespace and gives the
// gateway monitor one pass, which installs the routing it owns, as the
// daemon would.
func runRouter(ctx context.Context, t *testing.T, cfg *model.Config) *gateway.Monitor {
	t.Helper()
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Exec{}).Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	mon := gateway.New(gateway.NewICMPProber(), gateway.NewNetlinkRouter(), log)
	mon.Timeout = 300 * time.Millisecond
	mon.Policy = policy.NewInstaller(log)
	mon.Source = func() *model.Config { return cfg }
	mon.Tick(ctx)
	return mon
}

// A connection that comes in on a line is answered out of that line, not
// out of whichever holds the default route: a forwarded port, the router's
// own service and its ping, in both families, and a LAN host's IPv6 from
// the second line's prefix. The first line's answers are the control:
// they took the default route before and still do.
func TestRepliesLeaveByTheLineTheirConnectionCameInOnInKernel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		oneZone bool
	}{
		{"a zone each", false},
		{"one zone", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !netnstest.Enter(t, "nft") {
				return
			}
			lan, client := twoLines(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			runRouter(ctx, t, twoLinesConfig(tc.oneZone))
			web := listen(t, lan, "192.168.1.10:443")
			listen(t, lan, "[2001:db8:10::10]:443")
			for _, addr := range []string{"203.0.113.2:8443", "198.51.100.2:8443", "[2001:db8:1::2]:8443", "[2001:db8:2::2]:8443"} {
				listenHere(t, addr)
			}

			for _, c := range []struct{ what, addr string }{
				{"a port forwarded on the first line", "203.0.113.2:8080"},
				{"a port forwarded on the second line", "198.51.100.2:8080"},
				{"the router on the first line", "203.0.113.2:8443"},
				{"the router on the second line", "198.51.100.2:8443"},
				{"the router's IPv6 on the first line", "[2001:db8:1::2]:8443"},
				{"the router's IPv6 on the second line", "[2001:db8:2::2]:8443"},
				{"a LAN host's IPv6 from the second line's prefix", "[2001:db8:10::10]:443"},
			} {
				if err := dial(t, client, c.addr, 2*time.Second); err != nil {
					t.Errorf("%s: %v", c.what, err)
				}
			}
			for _, addr := range []string{"203.0.113.2", "198.51.100.2", "2001:db8:1::2", "2001:db8:2::2"} {
				var err error
				netnstest.Do(t, client, func() {
					_, err = gateway.NewICMPProber().Probe(ctx, addr, "", time.Second)
				})
				if err != nil {
					t.Errorf("ping %s: %v", addr, err)
				}
			}
			if got := web.seen(); slices.ContainsFunc(got, func(from string) bool { return from != "192.0.2.9" }) {
				t.Errorf("the LAN's server saw %v, want the client's own address", got)
			}
		})
	}
}

// A device that calls the router at its second line has the handshake
// answered from there, and its tunnel works: WireGuard's answers are the
// router's own, sent by the kernel.
func TestADeviceCallingTheSecondLineIsAnsweredFromItInKernel(t *testing.T) {
	if !wgKernel(t) {
		return
	}
	lan, client := twoLines(t)
	routerKey, deviceKey := wgKey(t), wgKey(t)
	cfg := twoLinesConfig(false)
	cfg.Zones = append(cfg.Zones, model.Zone{Name: "wg0"})
	cfg.Interfaces = append(cfg.Interfaces, model.Interface{
		Name: "wg0", Zone: "wg0", Enabled: true,
		IPv4: model.IPv4{Mode: model.AddrStatic, Address: "10.66.0.1/24"},
		IPv6: model.IPv6{Mode: model.AddrNone},
		WireGuard: &model.WireGuard{
			PrivateKey: b64(routerKey.Bytes()), ListenPort: 51820,
			Peers: []model.WireGuardPeer{{
				Name: "phone", Enabled: true, PublicKey: b64(deviceKey.PublicKey().Bytes()),
				AllowedIPs: []string{"10.66.0.2/32"},
			}},
		},
	})
	cfg.Rules = append(cfg.Rules, model.Rule{ID: "devices-out", Enabled: true, Zone: "wg0", Action: model.ActionAccept, Protocol: model.ProtocolAny})
	wgDevice(t, "wg0", routerKey, 51820, []netlink.WireGuardPeerConfig{{
		PublicKey: deviceKey.PublicKey().Bytes(), AllowedIPs: prefixes("10.66.0.2/32"),
	}}, "10.66.0.1/24")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runRouter(ctx, t, cfg)
	nas := listen(t, lan, "192.168.1.10:443")

	netnstest.Do(t, client, func() {
		wgDevice(t, "wg0", deviceKey, 0, []netlink.WireGuardPeerConfig{{
			PublicKey:  routerKey.PublicKey().Bytes(),
			Endpoint:   netip.MustParseAddrPort("198.51.100.2:51820"),
			AllowedIPs: prefixes("192.168.1.0/24"),
		}}, "10.66.0.2/32")
		route(t, "192.168.1.0/24", "", "wg0")
	})
	if err := dial(t, client, "192.168.1.10:443", 5*time.Second); err != nil {
		t.Fatalf("the LAN through the tunnel: %v", err)
	}
	if got := waitSeen(t, nas); len(got) != 1 || got[0] != "10.66.0.2" {
		t.Errorf("the LAN's server saw %v, want the device's tunnel address", got)
	}
	// A device follows its peer to wherever the peer's answers come from,
	// so an answer from the first line's address would move it there.
	netnstest.Do(t, client, func() {
		d, err := netlink.WireGuard("wg0")
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Peers) != 1 || d.Peers[0].Endpoint != netip.MustParseAddrPort("198.51.100.2:51820") {
			t.Errorf("the device's peer is %+v, want the router at 198.51.100.2:51820", d.Peers)
		}
	})
}

// A line whose gateway the monitor calls down still answers what comes in
// on it: the monitor, or the address it probes, may be what failed, and
// pf's reply-to takes no notice of it either. The demoted default route
// is what the answers take.
func TestALineTheMonitorCallsDownStillAnswersInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	_, client := twoLines(t)
	cfg := twoLinesConfig(false)
	cfg.Gateways[1].Monitor = "198.51.100.99"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mon := runRouter(ctx, t, cfg)
	for range gateway.FailAfter {
		mon.Tick(ctx)
	}
	if s := statusOf(mon, "second"); s.Online {
		t.Fatalf("the second line's gateway is up: %+v", s)
	}
	listenHere(t, "198.51.100.2:8443")
	if err := dial(t, client, "198.51.100.2:8443", 2*time.Second); err != nil {
		t.Errorf("the router on the second line, called down: %v", err)
	}
}

// A port a provider forwards to the router's end of a way-out tunnel is
// answered back into the tunnel. Out of the WAN, the answer would carry
// the tunnel's address to a provider that expects it inside.
func TestAPortForwardedThroughATunnelIsAnsweredIntoItInKernel(t *testing.T) {
	if !wgKernel(t) {
		return
	}
	client, far, routerKey, providerKey := wayOut(t)
	netnstest.Do(t, far, func() { dropSpoofed(t) })
	cfg := wayOutConfig(routerKey, providerKey)
	cfg.NAT.PortForwards = []model.PortForward{{
		ID: "forwarded", Enabled: true, Zone: "vpn", Protocol: model.ProtocolTCP, Ports: []string{"8080"},
		Target: "192.168.1.10", TargetPort: "443",
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mon := runRouter(ctx, t, cfg)
	for mon.Tick(ctx); !statusOf(mon, "vpn").Online || statusOf(mon, "vpn").Unknown; mon.Tick(ctx) {
		if ctx.Err() != nil {
			t.Fatal("the tunnel gateway never came up")
		}
	}
	server := listen(t, client, "192.168.1.10:443")
	if err := dial(t, far, "10.66.1.2:8080", 3*time.Second); err != nil {
		t.Fatalf("the forwarded port through the tunnel: %v", err)
	}
	if got := waitSeen(t, server); len(got) != 1 || got[0] != "10.64.0.1" {
		t.Errorf("the LAN's server saw %v, want the provider's end of the tunnel", got)
	}
}
