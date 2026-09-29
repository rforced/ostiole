package nft

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/gateway"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
	"github.com/rforced/ostiole/internal/netnstest"
	"github.com/rforced/ostiole/internal/policy"
	"github.com/rforced/ostiole/internal/testenv"
)

// wgKernel skips a test that needs the wireguard module where it is not
// loaded, and enters a namespace where it is.
func wgKernel(t *testing.T) bool {
	t.Helper()
	if _, err := os.Stat("/sys/module/wireguard"); err != nil {
		testenv.Unavailable(t, "the wireguard module is not loaded")
	}
	return netnstest.Enter(t, "nft")
}

// wgKey makes a WireGuard key pair.
func wgKey(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// quietIPv6 lets IPv6 addresses work at once, without duplicate address
// detection holding them back for a second, in the namespace the call is
// made in. It has to come before the links it covers.
func quietIPv6(t *testing.T) {
	t.Helper()
	for _, key := range []string{"all", "default"} {
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+key+"/accept_dad", []byte("0"), 0o644); err != nil {
			t.Fatalf("accept_dad: %v", err)
		}
	}
}

// routeAll turns forwarding on in both families in the namespace the call
// is made in, with reverse path filtering off.
func routeAll(t *testing.T) {
	t.Helper()
	forwardIPv4(t)
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cable joins this namespace to ns with a veth: near stays here, far goes
// there. Both come up; the addresses are the caller's to give.
func cable(t *testing.T, near, far string, ns *os.File) {
	t.Helper()
	if err := netlink.AddVeth(near, far); err != nil {
		t.Fatal(err)
	}
	if err := netlink.SetLinkNamespace(netnstest.Link(t, far).Index, int(ns.Fd())); err != nil {
		t.Fatal(err)
	}
	if err := netlink.SetLinkUp(netnstest.Link(t, near).Index); err != nil {
		t.Fatal(err)
	}
	netnstest.Do(t, ns, func() {
		if err := netlink.SetLinkUp(netnstest.Link(t, far).Index); err != nil {
			t.Fatal(err)
		}
	})
}

// addrs puts addresses on a link of the namespace the call is made in.
func addrs(t *testing.T, name string, prefixes ...string) {
	t.Helper()
	link := netnstest.Link(t, name)
	for _, p := range prefixes {
		if err := netlink.AddAddr(link.Index, netip.MustParsePrefix(p)); err != nil {
			t.Fatalf("%s on %s: %v", p, name, err)
		}
	}
}

// route adds a route to the main table of the namespace the call is made
// in: through gw, or onto the link when gw is empty.
func route(t *testing.T, dst, gw, dev string) {
	t.Helper()
	r := netlink.Route{LinkIndex: netnstest.Link(t, dev).Index}
	switch dst {
	case "default":
		r.Dst = &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	case "default6":
		r.Dst = &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	default:
		_, n, err := net.ParseCIDR(dst)
		if err != nil {
			t.Fatal(err)
		}
		r.Dst = n
	}
	if gw != "" {
		r.Gw = net.ParseIP(gw)
	} else if r.Dst.IP.To4() != nil {
		// What ip(8) gives an IPv4 route with no next hop.
		r.Scope = unix.RT_SCOPE_LINK
	}
	if err := netlink.AddRoute(r); err != nil {
		t.Fatalf("route %s via %q dev %s: %v", dst, gw, dev, err)
	}
}

// wgDevice makes a WireGuard device in the namespace the call is made in.
func wgDevice(t *testing.T, name string, key *ecdh.PrivateKey, port uint16, peers []netlink.WireGuardPeerConfig, prefixes ...string) {
	t.Helper()
	if err := netlink.AddLink(name, "wireguard"); err != nil {
		t.Fatalf("add a WireGuard device: %v", err)
	}
	addrs(t, name, prefixes...)
	if err := netlink.SetWireGuard(name, key.Bytes(), port, peers); err != nil {
		t.Fatalf("set %s: %v", name, err)
	}
	if err := netlink.SetLinkUp(netnstest.Link(t, name).Index); err != nil {
		t.Fatal(err)
	}
}

func prefixes(ps ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ps))
	for i, p := range ps {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// server accepts TCP connections on addr in ns and records where each came
// from. It answers nothing: a connection accepted is the proof.
type server struct {
	mu    sync.Mutex
	froms []string
}

func listen(t *testing.T, ns *os.File, addr string) *server {
	t.Helper()
	s := &server{}
	var l net.Listener
	netnstest.Do(t, ns, func() {
		var err error
		if l, err = net.Listen("tcp", addr); err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			s.mu.Lock()
			s.froms = append(s.froms, host)
			s.mu.Unlock()
			_ = c.Close()
		}
	}()
	return s
}

// seen returns where the connections came from so far.
func (s *server) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.froms...)
}

// dial connects from ns to addr, reporting whether it got through. A nil
// ns is the test's own.
func dial(t *testing.T, ns *os.File, addr string, wait time.Duration) error {
	t.Helper()
	var err error
	connect := func() {
		var c net.Conn
		if c, err = net.DialTimeout("tcp", addr, wait); err == nil {
			_ = c.Close()
		}
	}
	if ns == nil {
		connect()
	} else {
		netnstest.Do(t, ns, connect)
	}
	return err
}

// waitSeen waits for the server to have recorded a connection.
func waitSeen(t *testing.T, s *server) []string {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if got := s.seen(); len(got) > 0 {
			return got
		}
	}
	return s.seen()
}

// wayOutConfig is the router of TestAWayOutCarriesBothFamiliesAndStopsWhenItsFarEndDiesInKernel:
// the LAN's traffic goes out a provider's tunnel or nowhere.
func wayOutConfig(router, provider *ecdh.PrivateKey) *model.Config {
	return &model.Config{
		Version: model.SchemaVersion,
		System:  model.System{Hostname: "fw", Management: model.Management{WebPort: 443, SSHPort: 22}},
		Zones: []model.Zone{
			{Name: "wan", External: true},
			{Name: "lan", AntiLockout: true},
			{Name: "vpn", External: true},
		},
		Interfaces: []model.Interface{
			{
				Name: "eth0", Zone: "wan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "203.0.113.2/24"},
				IPv6: model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:1::2/64"},
			},
			{
				Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.1.1/24"},
				IPv6: model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:10::1/64"},
			},
			{
				Name: "wg1", Zone: "vpn", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "10.66.1.2/32"},
				IPv6: model.IPv6{Mode: model.AddrStatic, Address: "fc00:bbbb::2/128"},
				WireGuard: &model.WireGuard{
					PrivateKey: b64(router.Bytes()),
					Peers: []model.WireGuardPeer{{
						Name: "provider", Enabled: true, PublicKey: b64(provider.PublicKey().Bytes()),
						AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Endpoint: "203.0.113.1:51820",
					}},
				},
			},
		},
		Gateways: []model.Gateway{
			{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Monitor: "203.0.113.1"},
			{Name: "vpn", Enabled: true, Interface: "wg1", Monitor: "10.64.0.1"},
		},
		Rules: []model.Rule{{
			ID: "lan-out-vpn", Enabled: true, Zone: "lan", Action: model.ActionAccept,
			Protocol: model.ProtocolAny, Gateway: "vpn",
		}},
		NAT: model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
	}
}

// A way out, end to end: this namespace is the router, with the ruleset,
// the policy tables and the gateway monitor the daemon would run. A
// client sits on its LAN and a provider across its WAN, whose end of the
// tunnel also stands in for the internet. Both families reach the far
// host from the tunnel's own addresses, never from the WAN's. With the
// provider's end dead, the monitor takes the gateway down after its three
// failed probes and the client's traffic stops, none of it by the WAN.
func TestAWayOutCarriesBothFamiliesAndStopsWhenItsFarEndDiesInKernel(t *testing.T) {
	if !wgKernel(t) {
		return
	}
	quietIPv6(t)
	routeAll(t)
	routerKey, providerKey := wgKey(t), wgKey(t)
	client, far := netnstest.NewNS(t), netnstest.NewNS(t)
	for _, ns := range []*os.File{client, far} {
		netnstest.Do(t, ns, func() { quietIPv6(t) })
	}

	cable(t, "eth1", "lan0", client)
	addrs(t, "eth1", "192.168.1.1/24", "2001:db8:10::1/64")
	netnstest.Do(t, client, func() {
		addrs(t, "lan0", "192.168.1.10/24", "2001:db8:10::10/64")
		route(t, "default", "192.168.1.1", "lan0")
		route(t, "default6", "2001:db8:10::1", "lan0")
	})

	cable(t, "eth0", "wan0", far)
	addrs(t, "eth0", "203.0.113.2/24", "2001:db8:1::2/64")
	route(t, "default", "203.0.113.1", "eth0")
	route(t, "default6", "2001:db8:1::1", "eth0")
	// The provider's side: its WAN, its end of the tunnel, and the far
	// host on a dummy, reachable either way. It routes the router's tunnel
	// addresses back into the tunnel, and the LAN's IPv6 prefix to the
	// router, as the internet would.
	netnstest.Do(t, far, func() {
		addrs(t, "wan0", "203.0.113.1/24", "2001:db8:1::1/64")
		route(t, "2001:db8:10::/64", "2001:db8:1::2", "wan0")
		netnstest.Dummy(t, "net0", "198.18.0.7/32", "2001:db8:ff::7/128")
		wgDevice(t, "wg0", providerKey, 51820, []netlink.WireGuardPeerConfig{{
			PublicKey:  routerKey.PublicKey().Bytes(),
			AllowedIPs: prefixes("10.66.1.2/32", "fc00:bbbb::2/128"),
		}}, "10.64.0.1/24", "fc00:bbbb::1/64")
		route(t, "10.66.1.2/32", "", "wg0")
	})
	wgDevice(t, "wg1", routerKey, 0, []netlink.WireGuardPeerConfig{{
		PublicKey:  providerKey.PublicKey().Bytes(),
		Endpoint:   netip.MustParseAddrPort("203.0.113.1:51820"),
		AllowedIPs: prefixes("0.0.0.0/0", "::/0"),
	}}, "10.66.1.2/32", "fc00:bbbb::2/128")

	cfg := wayOutConfig(routerKey, providerKey)
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := (&Exec{}).Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	mon := gateway.New(gateway.NewICMPProber(), gateway.NewNetlinkRouter(), log)
	mon.Timeout = 300 * time.Millisecond
	mon.Policy = policy.NewInstaller(log)
	mon.Source = func() *model.Config { return cfg }
	mon.Tick(ctx)

	v4 := listen(t, far, "198.18.0.7:443")
	v6 := listen(t, far, "[2001:db8:ff::7]:443")
	if err := dial(t, client, "198.18.0.7:443", 3*time.Second); err != nil {
		t.Fatalf("IPv4 through the tunnel: %v", err)
	}
	if err := dial(t, client, "[2001:db8:ff::7]:443", 3*time.Second); err != nil {
		t.Fatalf("IPv6 through the tunnel: %v", err)
	}
	if got := waitSeen(t, v4); len(got) != 1 || got[0] != "10.66.1.2" {
		t.Errorf("IPv4 arrived from %v, want the tunnel's address alone", got)
	}
	if got := waitSeen(t, v6); len(got) != 1 || got[0] != "fc00:bbbb::2" {
		t.Errorf("IPv6 arrived from %v, want the tunnel's address alone", got)
	}
	for mon.Tick(ctx); ; {
		if s := statusOf(mon, "vpn"); s.Online && !s.Unknown {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("the tunnel gateway never came up")
		}
		mon.Tick(ctx)
	}

	// The provider's end dies. Three failed probes take the gateway down,
	// and its table then blocks rather than let the client out the WAN.
	netnstest.Do(t, far, func() {
		if err := netlink.SetLinkDown(netnstest.Link(t, "wg0").Index); err != nil {
			t.Fatal(err)
		}
	})
	for range gateway.FailAfter {
		mon.Tick(ctx)
	}
	if s := statusOf(mon, "vpn"); s.Online {
		t.Fatalf("the tunnel gateway is still up: %+v", s)
	}
	for _, addr := range []string{"198.18.0.7:443", "[2001:db8:ff::7]:443"} {
		if err := dial(t, client, addr, time.Second); err == nil {
			t.Errorf("%s got through with the tunnel down", addr)
		} else if errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if got := append(v4.seen(), v6.seen()...); len(got) != 2 {
		t.Errorf("the far host saw %v, want only the two connections through the tunnel", got)
	}
}

// statusOf finds one gateway among the monitor's statuses.
func statusOf(m *gateway.Monitor, name string) gateway.Status {
	for _, s := range m.Statuses() {
		if s.Name == name {
			return s
		}
	}
	return gateway.Status{}
}

// siteConfig is a router with a site tunnel to a cabin: its LAN reaches
// the cabin's, and the cabin reaches one host on the LAN, the NAS, on 443.
// dial says whether this router calls the cabin or the cabin calls in.
func siteConfig(router, cabin *ecdh.PrivateKey, dial, translate bool) *model.Config {
	peer := model.WireGuardPeer{
		Name: "cabin", Enabled: true, PublicKey: b64(cabin.PublicKey().Bytes()),
		AllowedIPs: []string{"10.77.0.2/32", "192.168.60.0/24"}, Masquerade: translate,
	}
	wg := &model.WireGuard{PrivateKey: b64(router.Bytes())}
	if dial {
		peer.Endpoint = "203.0.113.1:51820"
	} else {
		wg.ListenPort = 51820
	}
	wg.Peers = []model.WireGuardPeer{peer}
	return &model.Config{
		Version: model.SchemaVersion,
		System:  model.System{Hostname: "fw", Management: model.Management{WebPort: 443, SSHPort: 22}},
		Zones:   []model.Zone{{Name: "wan", External: true}, {Name: "lan", AntiLockout: true}, {Name: "sites"}},
		Interfaces: []model.Interface{
			{
				Name: "eth0", Zone: "wan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "203.0.113.2/24"}, IPv6: model.IPv6{Mode: model.AddrNone},
			},
			{
				Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.1.1/24"}, IPv6: model.IPv6{Mode: model.AddrNone},
			},
			{
				Name: "wg2", Zone: "sites", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "10.77.0.1/30"}, IPv6: model.IPv6{Mode: model.AddrNone},
				WireGuard: wg,
			},
		},
		Rules: []model.Rule{
			{ID: "allow-lan", Enabled: true, Zone: "lan", Action: model.ActionAccept, Protocol: model.ProtocolAny},
			{
				ID: "cabin-to-nas", Enabled: true, Zone: "sites", Action: model.ActionAccept, Protocol: model.ProtocolTCP,
				Source:      model.Endpoint{Peer: "wg2/cabin"},
				Destination: model.Endpoint{Addresses: []string{"192.168.1.10"}, Ports: []string{"443"}},
			},
		},
		NAT: model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
	}
}

// Two sites over a tunnel, dialled from each end in turn. The router
// under test is this namespace, with its LAN's NAS and printer in one
// namespace; the cabin's router is another, across the WAN, with its LAN
// host behind it in a third. Both ends list the other's LAN, so hosts
// reach each other both ways, and the rule naming the cabin's peer lets it
// reach the NAS and not the printer. With Translate on, and the cabin
// listing only this router's tunnel address, the LAN still reaches the
// cabin, from that address, and the cabin cannot open a connection back.
func TestTwoSitesReachEachOtherAsTheirRulesAllowInKernel(t *testing.T) {
	for _, tc := range []struct {
		name            string
		dial, translate bool
	}{
		{"this router dials", true, false},
		{"the cabin dials", false, false},
		{"translated", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !wgKernel(t) {
				return
			}
			quietIPv6(t)
			routeAll(t)
			routerKey, cabinKey := wgKey(t), wgKey(t)
			lan, cabin, cabinLAN := netnstest.NewNS(t), netnstest.NewNS(t), netnstest.NewNS(t)

			cable(t, "eth1", "lan0", lan)
			addrs(t, "eth1", "192.168.1.1/24")
			netnstest.Do(t, lan, func() {
				addrs(t, "lan0", "192.168.1.10/24", "192.168.1.11/24")
				route(t, "default", "192.168.1.1", "lan0")
			})
			cable(t, "eth0", "wan0", cabin)
			addrs(t, "eth0", "203.0.113.2/24")
			netnstest.Do(t, cabin, func() {
				routeAll(t)
				addrs(t, "wan0", "203.0.113.1/24")
			})
			netnstest.Do(t, cabin, func() {
				if err := netlink.AddVeth("lan1", "host0"); err != nil {
					t.Fatal(err)
				}
				if err := netlink.SetLinkNamespace(netnstest.Link(t, "host0").Index, int(cabinLAN.Fd())); err != nil {
					t.Fatal(err)
				}
				if err := netlink.SetLinkUp(netnstest.Link(t, "lan1").Index); err != nil {
					t.Fatal(err)
				}
				addrs(t, "lan1", "192.168.60.1/24")
			})
			netnstest.Do(t, cabinLAN, func() {
				if err := netlink.SetLinkUp(netnstest.Link(t, "host0").Index); err != nil {
					t.Fatal(err)
				}
				addrs(t, "host0", "192.168.60.10/24")
				route(t, "default", "192.168.60.1", "host0")
			})

			// The cabin lists this router's LAN unless it is translated, in
			// which case it knows only this router's tunnel address.
			cabinAllowed := prefixes("10.77.0.1/32", "192.168.1.0/24")
			if tc.translate {
				cabinAllowed = prefixes("10.77.0.1/32")
			}
			cabinPeer := netlink.WireGuardPeerConfig{PublicKey: routerKey.PublicKey().Bytes(), AllowedIPs: cabinAllowed}
			routerPeer := netlink.WireGuardPeerConfig{
				PublicKey: cabinKey.PublicKey().Bytes(), AllowedIPs: prefixes("10.77.0.2/32", "192.168.60.0/24"),
			}
			var routerPort, cabinPort uint16
			if tc.dial {
				routerPeer.Endpoint = netip.MustParseAddrPort("203.0.113.1:51820")
				cabinPort = 51820
			} else {
				cabinPeer.Endpoint = netip.MustParseAddrPort("203.0.113.2:51820")
				routerPort = 51820
			}
			netnstest.Do(t, cabin, func() {
				wgDevice(t, "wg0", cabinKey, cabinPort, []netlink.WireGuardPeerConfig{cabinPeer}, "10.77.0.2/30")
				if !tc.translate {
					route(t, "192.168.1.0/24", "", "wg0")
				}
			})
			wgDevice(t, "wg2", routerKey, routerPort, []netlink.WireGuardPeerConfig{routerPeer}, "10.77.0.1/30")
			// What networkd would add for the peer's networks.
			route(t, "192.168.60.0/24", "", "wg2")

			ruleset, err := Render(siteConfig(routerKey, cabinKey, tc.dial, tc.translate))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := (&Exec{}).Apply(ctx, ruleset); err != nil {
				t.Fatal(err)
			}
			nas := listen(t, lan, "192.168.1.10:443")
			printer := listen(t, lan, "192.168.1.11:443")
			host := listen(t, cabinLAN, "192.168.60.10:443")

			// When the cabin calls in, it has to first: this router has no
			// endpoint for it until then.
			if !tc.dial {
				if err := dial(t, cabinLAN, "192.168.1.10:443", 3*time.Second); err != nil {
					t.Fatalf("the cabin to the NAS: %v", err)
				}
			}
			if err := dial(t, lan, "192.168.60.10:443", 3*time.Second); err != nil {
				t.Fatalf("the LAN to the cabin: %v", err)
			}
			want := "192.168.1.10"
			if tc.translate {
				want = "10.77.0.1"
			}
			if got := waitSeen(t, host); len(got) != 1 || got[0] != want {
				t.Errorf("the cabin saw %v, want %s", got, want)
			}
			if tc.translate {
				if err := dial(t, cabinLAN, "192.168.1.10:443", time.Second); err == nil {
					t.Error("the cabin opened a connection to the NAS through a translated peer")
				}
				return
			}
			if tc.dial {
				if err := dial(t, cabinLAN, "192.168.1.10:443", 3*time.Second); err != nil {
					t.Fatalf("the cabin to the NAS: %v", err)
				}
			}
			if got := waitSeen(t, nas); len(got) != 1 || got[0] != "192.168.60.10" {
				t.Errorf("the NAS saw %v, want the cabin's host", got)
			}
			if err := dial(t, cabinLAN, "192.168.1.11:443", time.Second); err == nil {
				t.Error("the cabin reached the printer, which no rule lets it")
			}
			if got := printer.seen(); len(got) != 0 {
				t.Errorf("the printer saw %v", got)
			}
		})
	}
}

// deviceConfig is a router devices dial into on wg0, whose devices may
// reach the LAN and the internet.
func deviceConfig(router, device *ecdh.PrivateKey) *model.Config {
	return &model.Config{
		Version: model.SchemaVersion,
		System:  model.System{Hostname: "fw", Management: model.Management{WebPort: 443, SSHPort: 22}},
		Zones:   []model.Zone{{Name: "wan", External: true}, {Name: "lan", AntiLockout: true}, {Name: "wg0"}},
		Interfaces: []model.Interface{
			{
				Name: "eth0", Zone: "wan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "203.0.113.2/24"},
				IPv6: model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:1::2/64"},
			},
			{
				Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.1.1/24"}, IPv6: model.IPv6{Mode: model.AddrNone},
			},
			{
				Name: "wg0", Zone: "wg0", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "10.66.0.1/24"},
				IPv6: model.IPv6{Mode: model.AddrStatic, Address: "fd66::1/64"},
				WireGuard: &model.WireGuard{
					PrivateKey: b64(router.Bytes()), ListenPort: 51820,
					Peers: []model.WireGuardPeer{{
						Name: "phone", Enabled: true, PublicKey: b64(device.PublicKey().Bytes()),
						AllowedIPs: []string{"10.66.0.2/32", "fd66::2/128"},
					}},
				},
			},
		},
		Rules: []model.Rule{
			{ID: "allow-lan", Enabled: true, Zone: "lan", Action: model.ActionAccept, Protocol: model.ProtocolAny},
			{ID: "devices-out", Enabled: true, Zone: "wg0", Action: model.ActionAccept, Protocol: model.ProtocolAny},
		},
		NAT: model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
	}
}

// A device dials in from the internet, set up as the file the page makes
// would set it up. Given this router's networks it reaches the LAN; given
// everything it reaches the internet through the router in both families,
// its unique local IPv6 address translated to the router's own on the way
// out, since no router would forward it.
func TestADeviceReachesWhatItsFileSendsThroughInKernel(t *testing.T) {
	if !wgKernel(t) {
		return
	}
	quietIPv6(t)
	routeAll(t)
	routerKey, deviceKey := wgKey(t), wgKey(t)
	lan, inet, device := netnstest.NewNS(t), netnstest.NewNS(t), netnstest.NewNS(t)

	cable(t, "eth1", "lan0", lan)
	addrs(t, "eth1", "192.168.1.1/24")
	netnstest.Do(t, lan, func() {
		addrs(t, "lan0", "192.168.1.10/24")
		route(t, "default", "192.168.1.1", "lan0")
	})
	// The internet: a router between this one's WAN and the device's
	// line, holding the far host on a dummy.
	cable(t, "eth0", "up0", inet)
	addrs(t, "eth0", "203.0.113.2/24", "2001:db8:1::2/64")
	route(t, "default", "203.0.113.1", "eth0")
	route(t, "default6", "2001:db8:1::1", "eth0")
	netnstest.Do(t, inet, func() {
		quietIPv6(t)
		routeAll(t)
		addrs(t, "up0", "203.0.113.1/24", "2001:db8:1::1/64")
		netnstest.Dummy(t, "net0", "198.18.0.7/32", "2001:db8:ff::7/128")
		if err := netlink.AddVeth("line0", "dev0"); err != nil {
			t.Fatal(err)
		}
		if err := netlink.SetLinkNamespace(netnstest.Link(t, "dev0").Index, int(device.Fd())); err != nil {
			t.Fatal(err)
		}
		if err := netlink.SetLinkUp(netnstest.Link(t, "line0").Index); err != nil {
			t.Fatal(err)
		}
		addrs(t, "line0", "198.51.100.1/24")
	})
	far := listen(t, inet, "198.18.0.7:443")
	far6 := listen(t, inet, "[2001:db8:ff::7]:443")
	nas := listen(t, lan, "192.168.1.10:443")

	wgDevice(t, "wg0", routerKey, 51820, []netlink.WireGuardPeerConfig{{
		PublicKey: deviceKey.PublicKey().Bytes(), AllowedIPs: prefixes("10.66.0.2/32", "fd66::2/128"),
	}}, "10.66.0.1/24", "fd66::1/64")
	ruleset, err := Render(deviceConfig(routerKey, deviceKey))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := (&Exec{}).Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}

	// The device, as its file would have it: the router is its peer, at
	// the router's public address; what goes through the tunnel is the
	// file's choice. The line to the router stays outside the tunnel.
	setDevice := func(allowed ...string) {
		netnstest.Do(t, device, func() {
			if l, err := netlink.LinkByName("wg0"); err == nil {
				if err := netlink.DeleteLink(l.Index); err != nil {
					t.Fatal(err)
				}
			}
			wgDevice(t, "wg0", deviceKey, 0, []netlink.WireGuardPeerConfig{{
				PublicKey:  routerKey.PublicKey().Bytes(),
				Endpoint:   netip.MustParseAddrPort("203.0.113.2:51820"),
				AllowedIPs: prefixes(allowed...),
			}}, "10.66.0.2/32", "fd66::2/128")
			for _, a := range allowed {
				p := netip.MustParsePrefix(a)
				switch {
				case p.Bits() > 0:
					route(t, a, "", "wg0")
				case p.Addr().Is4():
					route(t, "default", "", "wg0")
				default:
					route(t, "default6", "", "wg0")
				}
			}
		})
	}
	netnstest.Do(t, device, func() {
		quietIPv6(t)
		if err := netlink.SetLinkUp(netnstest.Link(t, "dev0").Index); err != nil {
			t.Fatal(err)
		}
		addrs(t, "dev0", "198.51.100.50/24")
		route(t, "203.0.113.2/32", "198.51.100.1", "dev0")
	})

	// This router's networks: the LAN, not the internet.
	setDevice("192.168.1.0/24", "10.66.0.0/24", "fd66::/64")
	if err := dial(t, device, "192.168.1.10:443", 3*time.Second); err != nil {
		t.Fatalf("the device to the NAS: %v", err)
	}
	if got := waitSeen(t, nas); len(got) != 1 || got[0] != "10.66.0.2" {
		t.Errorf("the NAS saw %v, want the device's tunnel address", got)
	}
	if err := dial(t, device, "198.18.0.7:443", time.Second); err == nil {
		t.Error("the device reached the internet with only this router's networks sent through")
	}

	// Everything: the internet through the router, IPv6 included.
	setDevice("0.0.0.0/0", "::/0")
	if err := dial(t, device, "198.18.0.7:443", 3*time.Second); err != nil {
		t.Fatalf("the device to the internet: %v", err)
	}
	if err := dial(t, device, "[2001:db8:ff::7]:443", 3*time.Second); err != nil {
		t.Fatalf("the device to the internet over IPv6: %v", err)
	}
	if got := waitSeen(t, far); len(got) != 1 || got[0] != "203.0.113.2" {
		t.Errorf("the far host saw %v, want the router's WAN address", got)
	}
	if got := waitSeen(t, far6); len(got) != 1 || got[0] != "2001:db8:1::2" {
		t.Errorf("the far host saw %v over IPv6, want the router's WAN address", got)
	}
}

// overlapConfig is siteConfig with the cabin on this side's own network,
// 192.168.1.0/24: the cabin's shows here as 10.201.1.0/24 and this side's
// there as 10.200.1.0/24. The cabin may reach the NAS on 443 and this
// router on 8443.
func overlapConfig(router, cabin *ecdh.PrivateKey) *model.Config {
	cfg := siteConfig(router, cabin, true, false)
	peer := &cfg.Interfaces[2].WireGuard.Peers[0]
	peer.AllowedIPs = []string{"10.77.0.2/32", "192.168.1.0/24"}
	peer.Theirs = []model.NetMap{{Network: "192.168.1.0/24", As: "10.201.1.0/24"}}
	peer.Ours = []model.NetMap{{Network: "192.168.1.0/24", As: "10.200.1.0/24"}}
	cfg.Rules = append(cfg.Rules, model.Rule{
		ID: "cabin-to-router", Enabled: true, Zone: "sites", Action: model.ActionAccept, Protocol: model.ProtocolTCP,
		Source:      model.Endpoint{Peer: "wg2/cabin"},
		Destination: model.Endpoint{Self: true, Ports: []string{"8443"}},
	})
	return cfg
}

// Two sites on one network, 192.168.1.0/24, joined by a tunnel with this
// side doing all the translating. Each side reaches the other at the
// prefix it is shown, both ways, from the prefix it is shown; the rule
// naming the cabin's peer still tells the NAS from the printer; the cabin
// reaches this router's own services and gets its answers back through
// the tunnel. The cabin router's .1, which is this router's own address
// here, is not answered by this router, and a host here that sends to
// this side's shown prefix is stopped before conntrack sees it.
func TestTwoSitesOnOneNetworkReachEachOtherTranslatedInKernel(t *testing.T) {
	if !wgKernel(t) {
		return
	}
	quietIPv6(t)
	routeAll(t)
	routerKey, cabinKey := wgKey(t), wgKey(t)
	lan, cabin, cabinLAN := netnstest.NewNS(t), netnstest.NewNS(t), netnstest.NewNS(t)

	cable(t, "eth1", "lan0", lan)
	addrs(t, "eth1", "192.168.1.1/24")
	netnstest.Do(t, lan, func() {
		addrs(t, "lan0", "192.168.1.10/24", "192.168.1.11/24")
		route(t, "default", "192.168.1.1", "lan0")
	})
	cable(t, "eth0", "wan0", cabin)
	addrs(t, "eth0", "203.0.113.2/24")
	netnstest.Do(t, cabin, func() {
		routeAll(t)
		addrs(t, "wan0", "203.0.113.1/24")
		if err := netlink.AddVeth("lan1", "host0"); err != nil {
			t.Fatal(err)
		}
		if err := netlink.SetLinkNamespace(netnstest.Link(t, "host0").Index, int(cabinLAN.Fd())); err != nil {
			t.Fatal(err)
		}
		if err := netlink.SetLinkUp(netnstest.Link(t, "lan1").Index); err != nil {
			t.Fatal(err)
		}
		addrs(t, "lan1", "192.168.1.1/24")
		wgDevice(t, "wg0", cabinKey, 51820, []netlink.WireGuardPeerConfig{{
			PublicKey: routerKey.PublicKey().Bytes(), AllowedIPs: prefixes("10.77.0.1/32", "10.200.1.0/24"),
		}}, "10.77.0.2/30")
		route(t, "10.200.1.0/24", "", "wg0")
	})
	netnstest.Do(t, cabinLAN, func() {
		if err := netlink.SetLinkUp(netnstest.Link(t, "host0").Index); err != nil {
			t.Fatal(err)
		}
		addrs(t, "host0", "192.168.1.20/24")
		route(t, "default", "192.168.1.1", "host0")
	})
	wgDevice(t, "wg2", routerKey, 0, []netlink.WireGuardPeerConfig{{
		PublicKey: cabinKey.PublicKey().Bytes(), Endpoint: netip.MustParseAddrPort("203.0.113.1:51820"),
		AllowedIPs: prefixes("10.77.0.2/32", "192.168.1.0/24"),
	}}, "10.77.0.1/30")
	// What networkd adds for the shown prefix; the real network has only
	// the translation's own table.
	route(t, "10.201.1.0/24", "", "wg2")

	cfg := overlapConfig(routerKey, cabinKey)
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	if err := policy.NewInstaller(slog.New(slog.DiscardHandler)).Sync(policy.Translations(cfg)); err != nil {
		t.Fatal(err)
	}
	nas := listen(t, lan, "192.168.1.10:443")
	printer := listen(t, lan, "192.168.1.11:443")
	host := listen(t, cabinLAN, "192.168.1.20:443")
	self := listenHere(t, "0.0.0.0:8443")

	if err := dial(t, lan, "10.201.1.20:443", 3*time.Second); err != nil {
		t.Fatalf("the NAS to the cabin's host: %v", err)
	}
	if got := waitSeen(t, host); len(got) != 1 || got[0] != "10.200.1.10" {
		t.Errorf("the cabin's host saw %v, want the NAS as it is shown there", got)
	}
	if err := dial(t, cabinLAN, "10.200.1.10:443", 3*time.Second); err != nil {
		t.Fatalf("the cabin's host to the NAS: %v", err)
	}
	if got := waitSeen(t, nas); len(got) != 1 || got[0] != "10.201.1.20" {
		t.Errorf("the NAS saw %v, want the cabin's host as it is shown here", got)
	}
	if err := dial(t, cabinLAN, "10.200.1.11:443", time.Second); err == nil {
		t.Error("the cabin reached the printer, which no rule lets it")
	}
	if got := printer.seen(); len(got) != 0 {
		t.Errorf("the printer saw %v", got)
	}
	// This router's own services, answered back through the tunnel.
	if err := dial(t, cabinLAN, "10.200.1.1:8443", 3*time.Second); err != nil {
		t.Fatalf("the cabin's host to this router: %v", err)
	}
	if got := waitSeen(t, self); len(got) != 1 || got[0] != "10.201.1.20" {
		t.Errorf("this router saw %v, want the cabin's host as it is shown here", got)
	}
	// The cabin router's .1 is this router's own address here.
	if err := dial(t, lan, "10.201.1.1:8443", time.Second); err == nil {
		t.Error("10.201.1.1 was answered, by this router rather than the cabin's")
	}
	if got := self.seen(); len(got) != 1 {
		t.Errorf("this router answered %v, want only the cabin's host", got)
	}
	// This router's own connection to the cabin's host, from its tunnel
	// address.
	if err := dial(t, nil, "10.201.1.20:443", 3*time.Second); err != nil {
		t.Fatalf("this router to the cabin's host: %v", err)
	}
	got := host.seen()
	for deadline := time.Now().Add(3 * time.Second); len(got) < 2 && time.Now().Before(deadline); got = host.seen() {
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 2 || got[1] != "10.77.0.1" {
		t.Errorf("the cabin's host saw %v, want this router at its tunnel address last", got)
	}
	if err := dial(t, lan, "10.200.1.11:443", time.Second); err == nil {
		t.Error("a host here reached this side's shown prefix")
	}
	if n := waitForCounter(ctx, t, x, "translate_guard/translate-guard:wg2", 1); n == 0 {
		t.Error("the guard counted nothing")
	}
}

// listenHere is listen in the test's own namespace.
func listenHere(t *testing.T, addr string) *server {
	t.Helper()
	s := &server{}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			s.mu.Lock()
			s.froms = append(s.froms, host)
			s.mu.Unlock()
			_ = c.Close()
		}
	}()
	return s
}
