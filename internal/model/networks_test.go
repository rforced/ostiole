package model

import (
	"net/netip"
	"slices"
	"testing"
)

func TestInternalNetworks(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Zones: []Zone{{Name: "wan", External: true}, {Name: "lan"}, {Name: "guest"}, {Name: "vpn"}, {Name: "tailnet"}},
		Interfaces: []Interface{
			{Name: "eth0", Zone: "wan", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "203.0.113.2/24"}},
			{Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"},
				IPv6: IPv6{Mode: AddrStatic, Address: "fd00:1::1/64"}},
			{Name: "eth2", Zone: "guest", Enabled: false, IPv4: IPv4{Mode: AddrStatic, Address: "192.168.2.1/24"}},
			{Name: "eth3", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "192.168.3.1/24"}},
			{Name: "eth4", Zone: "guest", Enabled: true, IPv4: IPv4{Mode: AddrDHCP}},
			{Name: "wg0", Zone: "vpn", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "10.66.0.1/24"},
				WireGuard: &WireGuard{Peers: []WireGuardPeer{
					{Name: "laptop", Enabled: true, AllowedIPs: []string{"10.66.0.2/32", "192.168.50.0/24"}},
					{Name: "tablet", Enabled: false, AllowedIPs: []string{"192.168.60.0/24"}},
					{Name: "exit", Enabled: true, AllowedIPs: []string{"0.0.0.0/0", "::/0"}},
				}}},
			{Name: "wg1", Zone: "wan", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "10.77.0.1/24"},
				WireGuard: &WireGuard{Peers: []WireGuardPeer{
					{Name: "far-site", Enabled: true, AllowedIPs: []string{"192.168.70.0/24"}},
				}}},
			{Name: TailscaleDevice, Zone: "tailnet", Enabled: true, Tailscale: &Tailscale{}},
		},
	}
	want := []string{
		"192.168.1.0/24", "fd00:1::/64", "10.66.0.0/24", "10.66.0.2/32", "192.168.50.0/24",
		"100.64.0.0/10", "fd7a:115c:a1e0::/48",
	}
	if got := prefixStrings(cfg.InternalNetworks()); !slices.Equal(got, want) {
		t.Errorf("internal networks = %v, want %v", got, want)
	}

	cfg.Interfaces[len(cfg.Interfaces)-1].Enabled = false
	if got := prefixStrings(cfg.InternalNetworks()); slices.Contains(got, "100.64.0.0/10") {
		t.Errorf("a disabled tailnet counted: %v", got)
	}
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}
