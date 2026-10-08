package model

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ostiole/internal/wg"
)

// newKey is a fresh WireGuard key. A peer's public key only has to have the
// shape of one.
func newKey(t *testing.T) string {
	t.Helper()
	k, err := wg.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// tunnelConfig is a starter router with wg0 in a zone of its own and one
// device on it.
func tunnelConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Starter(StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Zones = append(cfg.Zones, Zone{Name: "wg0"})
	cfg.Interfaces = append(cfg.Interfaces, Interface{
		Name:    "wg0",
		Zone:    "wg0",
		Enabled: true,
		IPv4:    IPv4{Mode: AddrStatic, Address: "10.66.0.1/24"},
		IPv6:    IPv6{Mode: AddrNone},
		WireGuard: &WireGuard{
			PrivateKey: newKey(t),
			ListenPort: 51820,
			Peers: []WireGuardPeer{
				{Name: "laptop", Enabled: true, PublicKey: newKey(t), AllowedIPs: []string{"10.66.0.2/32"}},
			},
		},
	})
	return cfg
}

// tunnel is wg0 in a config from tunnelConfig.
func tunnel(cfg *Config) *Interface {
	return &cfg.Interfaces[len(cfg.Interfaces)-1]
}

// tunnelIssues maps each path with a problem to its message.
func tunnelIssues(t *testing.T, cfg *Config) map[string]string {
	t.Helper()
	var got map[string]string
	for _, issue := range issues(t, cfg) {
		path, msg, _ := strings.Cut(issue, ": ")
		if got == nil {
			got = map[string]string{}
		}
		got[path] = msg
	}
	return got
}

func TestTunnelConfigValidates(t *testing.T) {
	t.Parallel()
	if err := tunnelConfig(t).Validate(); err != nil {
		t.Fatal(err)
	}
}

// An IPv6 endpoint is written in brackets, and cutting it at the first
// colon refused every one of them.
func TestValidatePeerEndpoint(t *testing.T) {
	t.Parallel()
	const path = "interfaces[2].wireguard.peers[0].endpoint"
	for _, tc := range []struct {
		endpoint string
		ok       bool
	}{
		{"vpn.example.net:51820", true},
		{"203.0.113.7:51820", true},
		{"[2001:db8::1]:51820", true},
		{"2001:db8::1:51820", false},
		{"[2001:db8::1]", false},
		{"vpn.example.net", false},
		{":51820", false},
		{"vpn.example.net:0", false},
		{"vpn.example.net:65536", false},
		{"vpn.example.net:wg", false},
	} {
		cfg := tunnelConfig(t)
		tunnel(cfg).WireGuard.Peers[0].Endpoint = tc.endpoint
		msg, refused := tunnelIssues(t, cfg)[path]
		if refused == tc.ok {
			t.Errorf("%q: refused = %v (%s), want %v", tc.endpoint, refused, msg, !tc.ok)
		}
	}
}

// WireGuard keeps an address on one peer only, so the second peer to list
// it would lose it without a word.
func TestValidateRefusesAnAddressTwoPeersList(t *testing.T) {
	t.Parallel()
	cfg := tunnelConfig(t)
	w := tunnel(cfg).WireGuard
	w.Peers = append(w.Peers, WireGuardPeer{Name: "phone", Enabled: true, PublicKey: newKey(t), AllowedIPs: []string{"10.66.0.3/32", "10.66.0.2"}})
	msg := tunnelIssues(t, cfg)["interfaces[2].wireguard.peers[1].allowedIps[1]"]
	if !strings.Contains(msg, `also listed by peer "laptop"`) {
		t.Errorf("message = %q", msg)
	}

	// A disabled peer holds nothing, so a new device can take the address
	// of the one it replaces.
	w.Peers[0].Enabled = false
	if got := tunnelIssues(t, cfg); got != nil {
		t.Errorf("a disabled peer still counted: %v", got)
	}
}

// A peer reached at an address the tunnel routes would have the tunnel's
// own packets sent into the tunnel.
func TestValidateRefusesAnEndpointInsideTheTunnel(t *testing.T) {
	t.Parallel()
	const path = "interfaces[2].wireguard.peers[1].endpoint"
	for _, tc := range []struct {
		endpoint string
		ok       bool
	}{
		{"192.168.99.1:51820", false}, // the peer's own network
		{"10.66.0.9:51820", false},    // the tunnel's network
		{"10.66.0.2:51820", false},    // another peer's address
		{"203.0.113.9:51820", true},
		{"branch.example.net:51820", true},
	} {
		cfg := tunnelConfig(t)
		w := tunnel(cfg).WireGuard
		w.Peers = append(w.Peers, WireGuardPeer{
			Name: "branch", Enabled: true, PublicKey: newKey(t),
			AllowedIPs: []string{"10.66.0.3/32", "192.168.99.0/24"}, Endpoint: tc.endpoint,
		})
		msg, refused := tunnelIssues(t, cfg)[path]
		if refused == tc.ok {
			t.Errorf("%q: refused = %v (%s), want %v", tc.endpoint, refused, msg, !tc.ok)
		}
	}

	// Sending everything to a provider adds no route, so its endpoint is
	// reached the usual way.
	cfg := tunnelConfig(t)
	w := tunnel(cfg).WireGuard
	w.Peers = append(w.Peers, WireGuardPeer{
		Name: "provider", Enabled: true, PublicKey: newKey(t),
		AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Endpoint: "198.51.100.4:51820",
	})
	if got := tunnelIssues(t, cfg); got != nil {
		t.Errorf("a default route counted as routed: %v", got)
	}
}

// Two homes on 192.168.1.0/24 pass every other check and cannot reach
// each other.
func TestValidateRefusesOverlappingPeerNetworks(t *testing.T) {
	t.Parallel()
	t.Run("an interface", func(t *testing.T) {
		t.Parallel()
		cfg := tunnelConfig(t)
		w := tunnel(cfg).WireGuard
		w.Peers = append(w.Peers, WireGuardPeer{Name: "home", Enabled: true, PublicKey: newKey(t), AllowedIPs: []string{"192.168.1.0/24"}})
		msg := tunnelIssues(t, cfg)["interfaces[2].wireguard.peers[1].allowedIps[0]"]
		if msg != "192.168.1.0/24 is on eth1; list only the peer's tunnel address and the networks behind it, "+
			"or renumber one side if the far end uses these numbers too" {
			t.Errorf("message = %q", msg)
		}
	})
	t.Run("another tunnel's peer", func(t *testing.T) {
		t.Parallel()
		cfg := tunnelConfig(t)
		w := tunnel(cfg).WireGuard
		w.Peers = append(w.Peers, WireGuardPeer{Name: "branch", Enabled: true, PublicKey: newKey(t), AllowedIPs: []string{"192.168.99.0/24"}})
		cfg.Interfaces = append(cfg.Interfaces, Interface{
			Name: "wg1", Zone: "wg0", Enabled: true,
			IPv4: IPv4{Mode: AddrStatic, Address: "10.67.0.1/24"},
			IPv6: IPv6{Mode: AddrNone},
			WireGuard: &WireGuard{PrivateKey: newKey(t), Peers: []WireGuardPeer{
				{Name: "friend", Enabled: true, PublicKey: newKey(t), AllowedIPs: []string{"10.67.0.2/32", "192.168.99.128/25"}},
			}},
		})
		msg := tunnelIssues(t, cfg)["interfaces[3].wireguard.peers[0].allowedIps[1]"]
		if msg != "192.168.99.128/25 is also behind wg0/branch; renumber one side" {
			t.Errorf("message = %q", msg)
		}
		// Switched off, the other tunnel routes nothing.
		cfg.Interfaces[3].Enabled = false
		if got := tunnelIssues(t, cfg); got != nil {
			t.Errorf("a disabled tunnel still counted: %v", got)
		}
	})
	t.Run("nested on one tunnel", func(t *testing.T) {
		t.Parallel()
		// WireGuard picks the most specific peer, and a device's address
		// inside the tunnel's own network is what the network is for.
		cfg := tunnelConfig(t)
		w := tunnel(cfg).WireGuard
		w.Peers = append(w.Peers,
			WireGuardPeer{Name: "site", Enabled: true, PublicKey: newKey(t), AllowedIPs: []string{"10.0.0.0/8"}},
			WireGuardPeer{Name: "server", Enabled: true, PublicKey: newKey(t), AllowedIPs: []string{"10.70.0.5/32", "::/0"}},
		)
		if got := tunnelIssues(t, cfg); got != nil {
			t.Errorf("nested peers refused: %v", got)
		}
	})
}

func TestTunnelRoutes(t *testing.T) {
	t.Parallel()
	in := Interface{
		Name: "wg0",
		IPv4: IPv4{Mode: AddrStatic, Address: "10.66.0.1/24"},
		IPv6: IPv6{Mode: AddrStatic, Address: "fd66::1/64"},
		WireGuard: &WireGuard{Peers: []WireGuardPeer{
			{Name: "laptop", Enabled: true, AllowedIPs: []string{"10.66.0.2/32", "fd66::2/128"}},
			{Name: "branch", Enabled: true, AllowedIPs: []string{"192.168.99.0/24", "10.66.0.0/16", "0.0.0.0/0"}},
			{Name: "office", Enabled: true, AllowedIPs: []string{"192.168.99.7", "192.168.99.0/24"}},
			{Name: "retired", AllowedIPs: []string{"172.16.0.0/12"}},
		}},
	}
	var got []string
	for _, r := range in.TunnelRoutes() {
		got = append(got, r.Prefix.String()+" "+r.Peer)
	}
	want := []string{"10.66.0.0/16 branch", "192.168.99.0/24 branch", "192.168.99.7/32 office"}
	if !slices.Equal(got, want) {
		t.Errorf("routes = %q, want %q", got, want)
	}
	if routes := (Interface{Name: "eth0"}).TunnelRoutes(); routes != nil {
		t.Errorf("an interface that is no tunnel routes %v", routes)
	}
}

func TestKeepaliveWithoutEndpoint(t *testing.T) {
	t.Parallel()
	cfg := tunnelConfig(t)
	wg0 := tunnel(cfg)
	wg0.WireGuard.Peers = []WireGuardPeer{
		{Name: "phone", Enabled: true, Keepalive: 25},
		{Name: "laptop", Enabled: true},
		{Name: "branch", Enabled: true, Endpoint: "198.51.100.7:51820", Keepalive: 25},
		{Name: "retired", Keepalive: 25},
	}
	off := *wg0
	off.Name, off.Enabled = "wg1", false
	off.WireGuard = &WireGuard{Peers: []WireGuardPeer{{Name: "tablet", Enabled: true, Keepalive: 25}}}
	cfg.Interfaces = append(cfg.Interfaces, off)
	if got := cfg.KeepaliveWithoutEndpoint(); !slices.Equal(got, []string{"wg0/phone"}) {
		t.Errorf("peers = %q, want only wg0/phone", got)
	}
}

// A rule names a peer as "tunnel/peer" and means its addresses. It cannot
// name one that is not there, one that stands for the whole internet, or
// one that arrives in another zone than the rule looks at.
func TestARuleNamesAPeer(t *testing.T) {
	t.Parallel()
	rule := func(zone string, src, dst Endpoint) Rule {
		return Rule{ID: "r1", Enabled: true, Zone: zone, Action: ActionAccept, Protocol: ProtocolAny,
			Source: src, Destination: dst}
	}
	for _, tc := range []struct {
		name  string
		rule  Rule
		edit  func(*Config)
		issue string
	}{
		{"as a source", rule("wg0", Endpoint{Peer: "wg0/laptop"}, Endpoint{}), nil, ""},
		{"inverted", rule("wg0", Endpoint{Peer: "wg0/laptop", NotAddresses: true}, Endpoint{}), nil, ""},
		{"as a destination", rule("lan", Endpoint{}, Endpoint{Peer: "wg0/laptop"}), nil, ""},
		{"unknown", rule("wg0", Endpoint{Peer: "wg0/tablet"}, Endpoint{}), nil, "rules[1].source.peer"},
		{"no tunnel", rule("wg0", Endpoint{Peer: "laptop"}, Endpoint{}), nil, "rules[1].source.peer"},
		{"with addresses", rule("wg0", Endpoint{Peer: "wg0/laptop", Addresses: []string{"10.66.0.9"}}, Endpoint{}), nil, "rules[1].source"},
		{"with self", rule("lan", Endpoint{}, Endpoint{Peer: "wg0/laptop", Self: true}), nil, "rules[1].destination.self"},
		{"another zone", rule("lan", Endpoint{Peer: "wg0/laptop"}, Endpoint{}), nil, "rules[1].source.peer"},
		{"the whole internet", rule("lan", Endpoint{}, Endpoint{Peer: "wg0/laptop"}), func(c *Config) {
			tunnel(c).WireGuard.Peers[0].AllowedIPs = []string{"0.0.0.0/0"}
		}, "rules[1].destination.peer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tunnelConfig(t)
			cfg.Rules = append(cfg.Rules, tc.rule)
			if tc.edit != nil {
				tc.edit(cfg)
			}
			got := tunnelIssues(t, cfg)
			switch {
			case tc.issue == "" && len(got) > 0:
				t.Errorf("issues %v, want none", got)
			case tc.issue != "" && got[tc.issue] == "":
				t.Errorf("no issue at %s: %v", tc.issue, got)
			}
		})
	}
}

// Translate needs something to translate, and an address of the tunnel's
// own in each family it would translate, or the masquerade takes another
// interface's.
func TestTranslateNeedsTheTunnelsAddress(t *testing.T) {
	t.Parallel()
	const path = "interfaces[2].wireguard.peers[0].masquerade"
	for _, tc := range []struct {
		name    string
		allowed []string
		ipv6    string
		ok      bool
	}{
		{"a site", []string{"10.66.0.2/32", "192.168.50.0/24"}, "", true},
		{"a site with IPv6", []string{"192.168.50.0/24", "fd50::/64"}, "fd66::1/64", true},
		{"no IPv6 address", []string{"192.168.50.0/24", "fd50::/64"}, "", false},
		{"only a default route", []string{"0.0.0.0/0"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tunnelConfig(t)
			wg0 := tunnel(cfg)
			if tc.ipv6 != "" {
				wg0.IPv6 = IPv6{Mode: AddrStatic, Address: tc.ipv6}
			}
			p := &wg0.WireGuard.Peers[0]
			p.AllowedIPs, p.Masquerade = tc.allowed, true
			got := tunnelIssues(t, cfg)[path]
			if tc.ok != (got == "") {
				t.Errorf("issue %q at %s, want ok %v", got, path, tc.ok)
			}
		})
	}
}

// A line of the proxy's access list that names a peer comes from somewhere:
// it is not anywhere, and it has to look where the peer arrives.
func TestAnAccessLineNamesAPeer(t *testing.T) {
	t.Parallel()
	a := ProxyAccess{Source: Endpoint{Peer: "wg0/laptop"}}
	if a.Anywhere() {
		t.Error("a line from one peer reads as anywhere")
	}
}

// A line of the proxy's access list that names a peer has to look where
// the peer arrives, as a rule does.
func TestAnAccessLineLooksWhereItsPeerArrives(t *testing.T) {
	t.Parallel()
	for zone, ok := range map[string]bool{"wg0": true, "lan": false} {
		cfg := tunnelConfig(t)
		cfg.Services.Proxy = workingProxy()
		cfg.Services.Proxy.Access = append(cfg.Services.Proxy.Access, ProxyAccess{
			ID: "laptop-https", Enabled: true, Zone: zone, Action: ActionAccept,
			Ports: []string{ProxyPortHTTPS}, Source: Endpoint{Peer: "wg0/laptop"},
		})
		path := fmt.Sprintf("services.proxy.access[%d].source.peer", len(cfg.Services.Proxy.Access)-1)
		if got := tunnelIssues(t, cfg)[path]; (got == "") != ok {
			t.Errorf("zone %s: issue %q, want ok %v", zone, got, ok)
		}
	}
}

// wayOutConfig is a starter router with a tunnel to a provider, wg1 in an
// external zone of its own, and a gateway through it.
func wayOutConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Starter(StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Zones = append(cfg.Zones, Zone{Name: "vpn", External: true})
	cfg.Interfaces = append(cfg.Interfaces, Interface{
		Name:    "wg1",
		Zone:    "vpn",
		Enabled: true,
		IPv4:    IPv4{Mode: AddrStatic, Address: "10.66.1.2/32"},
		IPv6:    IPv6{Mode: AddrStatic, Address: "fc00:bbbb::2/128"},
		WireGuard: &WireGuard{
			PrivateKey: newKey(t),
			Peers: []WireGuardPeer{{
				Name: "provider", Enabled: true, PublicKey: newKey(t),
				AllowedIPs: []string{"0.0.0.0/0", "::/0"}, Endpoint: "198.51.100.7:51820",
			}},
		},
	})
	cfg.Gateways = []Gateway{
		{Name: "wan", Enabled: true, Interface: "eth0", Monitor: "9.9.9.9"},
		{Name: "vpn", Enabled: true, Interface: "wg1", Monitor: "10.64.0.1"},
	}
	return cfg
}

func TestATunnelGatewayValidates(t *testing.T) {
	t.Parallel()
	cfg := wayOutConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	vpn, _ := cfg.Gateway("vpn")
	if !cfg.TunnelGateway(*vpn) {
		t.Error("a gateway on a tunnel with no address is not a tunnel gateway")
	}
	if v4, v6 := cfg.TunnelFamilies(*vpn); !v4 || !v6 {
		t.Errorf("families = %v, %v; want both", v4, v6)
	}
	wan, _ := cfg.Gateway("wan")
	if cfg.TunnelGateway(*wan) {
		t.Error("the WAN's gateway is taken for a tunnel gateway")
	}
	// The tunnel never carries the default route, so one WAN is still
	// nothing to fail over between.
	if cfg.CanFailover() {
		t.Error("a WAN and a tunnel gateway count as two lines to fail over between")
	}
}

// A gateway with an address on a tunnel is an ordinary one: a far end
// routed by its tunnel address, as before tunnel gateways.
func TestAGatewayWithAnAddressOnATunnelIsOrdinary(t *testing.T) {
	t.Parallel()
	cfg := wayOutConfig(t)
	cfg.Gateways[1].Address = "10.66.1.1"
	if cfg.TunnelGateway(cfg.Gateways[1]) {
		t.Error("a gateway with an address counts as a tunnel gateway")
	}
}

// A family the tunnel has no address in, or no peer takes the default
// route of, is not carried; the policy table refuses it instead.
func TestTunnelFamiliesNeedAnAddressAndADefaultRoute(t *testing.T) {
	t.Parallel()
	cfg := wayOutConfig(t)
	tunnel(cfg).IPv6 = IPv6{Mode: AddrNone}
	vpn, _ := cfg.Gateway("vpn")
	if v4, v6 := cfg.TunnelFamilies(*vpn); !v4 || v6 {
		t.Errorf("without an IPv6 address: families = %v, %v; want IPv4 alone", v4, v6)
	}
	cfg = wayOutConfig(t)
	tunnel(cfg).WireGuard.Peers[0].AllowedIPs = []string{"::/0"}
	vpn, _ = cfg.Gateway("vpn")
	if v4, v6 := cfg.TunnelFamilies(*vpn); v4 || !v6 {
		t.Errorf("a peer taking ::/0 alone: families = %v, %v; want IPv6 alone", v4, v6)
	}
	tunnel(cfg).WireGuard.Peers[0].Enabled = false
	if v4, v6 := cfg.TunnelFamilies(*vpn); v4 || v6 {
		t.Errorf("a disabled peer: families = %v, %v; want none", v4, v6)
	}
}

func TestValidateTunnelGateway(t *testing.T) {
	t.Parallel()
	const gw = "gateways[1]"
	for _, tc := range []struct {
		name, path, want string
		change           func(*Config)
	}{
		{"no monitor", gw + ".monitor", "a tunnel gateway needs an IPv4 address beyond the tunnel to probe",
			func(c *Config) { c.Gateways[1].Monitor = "" }},
		{"an IPv6 monitor", gw + ".monitor", "a tunnel gateway is probed over IPv4",
			func(c *Config) { c.Gateways[1].Monitor = "2001:db8::53" }},
		{"a priority", gw + ".priority", "a tunnel gateway never carries the default route; leave it at 0",
			func(c *Config) { c.Gateways[1].Priority = 2 }},
		{"no peer takes the default route", gw + ".interface",
			"no peer on wg1 takes a default route, so nothing would go through it",
			func(c *Config) { tunnel(c).WireGuard.Peers[0].AllowedIPs = []string{"10.64.0.0/24"} }},
		{"no address in the family carried", gw + ".interface",
			"wg1 has no address in the family its peer's default route is in",
			func(c *Config) {
				tunnel(c).WireGuard.Peers[0].AllowedIPs = []string{"::/0"}
				tunnel(c).IPv6 = IPv6{Mode: AddrNone}
			}},
		{"an internal zone", gw + ".interface",
			"wg1 has to be in an external zone, or its far end can use this router's DNS",
			func(c *Config) { c.Zones[len(c.Zones)-1].External = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := wayOutConfig(t)
			tc.change(cfg)
			if got := tunnelIssues(t, cfg)[tc.path]; got != tc.want {
				t.Errorf("%s = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestValidateLookupsThroughAGateway(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		change     func(*Config)
	}{
		{"a tunnel gateway", "", func(c *Config) { c.Services.DNS.Via = "vpn" }},
		{"a line", "", func(c *Config) { c.Services.DNS.Via = "wan" }},
		{"nothing called that", `no enabled gateway or group is called "vps"`,
			func(c *Config) { c.Services.DNS.Via = "vps" }},
		{"a disabled gateway", `no enabled gateway or group is called "vpn"`, func(c *Config) {
			c.Services.DNS.Via = "vpn"
			c.Gateways[1].Enabled = false
		}},
		{"a tunnel dialled by name",
			"wg1/provider is dialled by name, which could not be looked up once lookups need wg1; give its address",
			func(c *Config) {
				c.Services.DNS.Via = "vpn"
				tunnel(c).WireGuard.Peers[0].Endpoint = "vpn.example.net:51820"
			}},
		{"a group holding a tunnel dialled by name",
			"wg1/provider is dialled by name, which could not be looked up once lookups need wg1; give its address",
			func(c *Config) {
				c.GatewayGroups = []GatewayGroup{{Name: "private", Enabled: true, Members: []GatewayMember{
					{Gateway: "vpn", Tier: 1}, {Gateway: "wan", Tier: 2},
				}}}
				c.Services.DNS.Via = "private"
				tunnel(c).WireGuard.Peers[0].Endpoint = "vpn.example.net:51820"
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := wayOutConfig(t)
			tc.change(cfg)
			if got := tunnelIssues(t, cfg)["services.dns.via"]; got != tc.want {
				t.Errorf("services.dns.via = %q, want %q", got, tc.want)
			}
		})
	}
}

// overlapConfig is a starter router whose LAN, 192.168.1.0/24, is also the
// cabin's: the cabin's shows here as 10.201.1.0/24 and this side's there
// as 10.200.1.0/24.
func overlapConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Starter(StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Gateways = nil
	cfg.Zones = append(cfg.Zones, Zone{Name: "sites"})
	cfg.Interfaces = append(cfg.Interfaces, Interface{
		Name: "wg2", Zone: "sites", Enabled: true,
		IPv4: IPv4{Mode: AddrStatic, Address: "10.77.0.1/30"},
		IPv6: IPv6{Mode: AddrNone},
		WireGuard: &WireGuard{
			PrivateKey: newKey(t), ListenPort: 51821,
			Peers: []WireGuardPeer{{
				Name: "cabin", Enabled: true, PublicKey: newKey(t),
				AllowedIPs: []string{"10.77.0.2/32", "192.168.1.0/24"},
				Theirs:     []NetMap{{Network: "192.168.1.0/24", As: "10.201.1.0/24"}},
				Ours:       []NetMap{{Network: "192.168.1.0/24", As: "10.200.1.0/24"}},
			}},
		},
	})
	return cfg
}

func TestTwoSitesOnOneNetworkValidateWhenBothSidesAreShown(t *testing.T) {
	t.Parallel()
	cfg := overlapConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	routes := tunnel(cfg).TunnelRoutes()
	if len(routes) != 1 || routes[0].Prefix.String() != "192.168.1.0/24" || !routes[0].Mapped {
		t.Errorf("routes = %+v, want the cabin's network, mapped", routes)
	}
	targets := cfg.TranslateTargets()
	if len(targets) != 1 || targets[0].Tunnel != "wg2" || len(targets[0].Networks) != 1 {
		t.Fatalf("translate targets = %+v, want wg2's", targets)
	}
	// Numbered after the gateways and groups, whose numbers stay as they were.
	cfg.Gateways = append(cfg.Gateways, Gateway{Name: "wan", Enabled: true, Interface: "eth0"})
	if got := cfg.TranslateTargets()[0]; got.Mark != 2<<PolicyMarkShift || got.Table != PolicyTableBase+2 {
		t.Errorf("after one gateway: mark %#x, table %d; want the second number", got.Mark, got.Table)
	}
}

func TestValidateNetworkMaps(t *testing.T) {
	t.Parallel()
	const peer = "interfaces[2].wireguard.peers[0]"
	for _, tc := range []struct {
		name, path, want string
		change           func(*WireGuardPeer)
	}{
		{"this side not shown", peer + ".allowedIps[1]",
			"192.168.1.0/24 is also on eth1; show that network to cabin under another prefix too, or translate to the tunnel address",
			func(p *WireGuardPeer) { p.Ours = nil }},
		{"translated to the tunnel address instead", "", "",
			func(p *WireGuardPeer) { p.Ours, p.Masquerade = nil, true }},
		{"both at once", peer + ".ours", "Translate to the tunnel address already hides this side; leave these out",
			func(p *WireGuardPeer) { p.Masquerade = true }},
		{"not the peer's network", peer + ".theirs[0].network", "192.168.2.0/24 is not one of cabin's allowed addresses",
			func(p *WireGuardPeer) { p.Theirs[0].Network = "192.168.2.0/24" }},
		{"a shorter prefix", peer + ".theirs[0].as", "10.201.0.0/16 has to be a /24 like 192.168.1.0/24, so each host keeps its number",
			func(p *WireGuardPeer) { p.Theirs[0].As = "10.201.0.0/16" }},
		{"IPv6", peer + ".theirs[0].as", `"fd01::/64" is not an IPv4 network`,
			func(p *WireGuardPeer) { p.Theirs[0].As = "fd01::/64" }},
		{"a shown prefix this side uses", peer + ".theirs[0].as", "192.168.1.0/24 overlaps the network it stands for",
			func(p *WireGuardPeer) { p.Theirs[0].As = "192.168.1.0/24" }},
		{"a shown prefix on an interface", peer + ".theirs[0].as", "10.77.0.0/24 is also on wg2; a shown prefix has to be free",
			func(p *WireGuardPeer) { p.Theirs[0].As = "10.77.0.0/24" }},
		{"one shown prefix twice", peer + ".ours[0].as", "10.201.1.0/24 is also behind wg2/cabin; a shown prefix has to be free",
			func(p *WireGuardPeer) { p.Ours[0].As = "10.201.1.0/24" }},
		{"this side's network where the far end has one", peer + ".ours[0].as",
			"192.168.70.0/24 is also behind wg2/cabin; a shown prefix has to be free",
			func(p *WireGuardPeer) {
				p.AllowedIPs = append(p.AllowedIPs, "192.168.70.0/24")
				p.Ours[0].As = "192.168.70.0/24"
			}},
		{"not this side's network", peer + ".ours[0].network", "172.20.0.0/24 is none of this router's networks",
			func(p *WireGuardPeer) { p.Ours[0].Network = "172.20.0.0/24" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := overlapConfig(t)
			tc.change(&tunnel(cfg).WireGuard.Peers[0])
			got := tunnelIssues(t, cfg)
			if tc.path == "" {
				if len(got) != 0 {
					t.Errorf("issues = %v, want none", got)
				}
				return
			}
			if got[tc.path] != tc.want {
				t.Errorf("%s = %q, want %q (all: %v)", tc.path, got[tc.path], tc.want, got)
			}
		})
	}
}

// A rule naming a peer whose network is shown elsewhere matches that
// peer's real addresses pinned to its tunnel, which cannot be inverted.
func TestAShowingPeerCannotBeInverted(t *testing.T) {
	t.Parallel()
	cfg := overlapConfig(t)
	cfg.Rules = append(cfg.Rules, Rule{
		ID: "not-cabin", Enabled: true, Zone: "sites", Action: ActionDrop, Protocol: ProtocolAny,
		Source: Endpoint{Peer: "wg2/cabin", NotAddresses: true},
	})
	var msg string
	for path, m := range tunnelIssues(t, cfg) {
		if strings.HasSuffix(path, ".source.peer") {
			msg = m
		}
	}
	if msg != `peer "wg2/cabin" shows networks under other prefixes, so it cannot be inverted` {
		t.Errorf("message = %q", msg)
	}
}
