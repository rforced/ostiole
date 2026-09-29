package model

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/wg"
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
