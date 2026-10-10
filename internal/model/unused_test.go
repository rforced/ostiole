package model

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// unusedConfig is a starter router with one item of each kind nothing
// uses, one of each kind switched off, and used ones beside them.
func unusedConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Starter(StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "192.0.2.1/24", WAN: "eth0",
		Services: true, DNSUpstreams: []string{"198.51.100.53"}})
	cfg.Zones = append(cfg.Zones, Zone{Name: "vpn", External: true}, Zone{Name: "spare_zone", External: true})
	cfg.Interfaces = append(cfg.Interfaces,
		Interface{Name: "eth2", Zone: "lan", IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone}},
		Interface{
			Name: "wg1", Zone: "vpn", Enabled: true,
			IPv4: IPv4{Mode: AddrStatic, Address: "198.51.100.2/32"},
			IPv6: IPv6{Mode: AddrNone},
			WireGuard: &WireGuard{PrivateKey: newKey(t), Peers: []WireGuardPeer{
				{Name: "provider", Enabled: true, PublicKey: newKey(t), AllowedIPs: []string{"0.0.0.0/0"},
					Endpoint: "203.0.113.7:51820"},
				{Name: "old_peer", PublicKey: newKey(t), AllowedIPs: []string{"198.51.100.128/25"}},
			}},
		})
	cfg.Aliases = []Alias{
		{Name: "spare_hosts", Type: AliasHosts, Entries: []string{"203.0.113.0/24"}},
		{Name: "trusted", Type: AliasHosts, Entries: []string{"192.0.2.10"}},
	}
	cfg.Schedules = []Schedule{{Name: "evenings", Start: "18:00", End: "23:00"}}
	cfg.Rules = append(cfg.Rules,
		Rule{ID: "from-trusted", Enabled: true, Zone: "lan", Action: ActionAccept, Protocol: ProtocolAny,
			Source: Endpoint{Alias: "trusted"}},
		Rule{ID: "old-rule", Zone: "lan", Action: ActionDrop, Protocol: ProtocolAny},
		Rule{ID: "spare-in", Enabled: true, Zone: "spare_zone", Action: ActionDrop, Protocol: ProtocolAny},
		Rule{ID: "to-spare", Enabled: true, Zone: "lan", DestZone: "spare_zone", Action: ActionAccept, Protocol: ProtocolAny},
		Rule{ID: "old-spare-in", Zone: "spare_zone", Action: ActionAccept, Protocol: ProtocolAny},
	)
	cfg.NAT = NAT{
		Outbound: OutboundNAT{Mode: OutboundHybrid, Rules: []OutboundRule{
			{ID: "spare-snat", Enabled: true, Zone: "spare_zone"},
			{ID: "old-snat", Zone: "wan", NoNAT: true},
			{ID: "old-spare-snat", Zone: "spare_zone", NoNAT: true},
		}},
		PortForwards: []PortForward{
			{ID: "spare-fwd", Enabled: true, Zone: "spare_zone", Protocol: ProtocolTCP, Ports: []string{"8080"}, Target: "192.0.2.20"},
			{ID: "old-fwd", Zone: "wan", Protocol: ProtocolTCP, Ports: []string{"2222"}, Target: "192.0.2.40"},
		},
		OneToOne: []OneToOneNAT{
			{ID: "spare-1to1", Enabled: true, Zone: "spare_zone", External: "203.0.113.10", Internal: "192.0.2.30"},
			{ID: "old-1to1", Zone: "wan", External: "203.0.113.11", Internal: "192.0.2.31"},
		},
	}
	syn := DefaultSynFlood
	cfg.Protection = Protection{Zones: []string{"wan", "spare_zone"}, SynFlood: &syn}
	cfg.Gateways = append(cfg.Gateways,
		Gateway{Name: "spare_vpn", Enabled: true, Interface: "wg1", Monitor: "198.51.100.1"},
		Gateway{Name: "old_gw", Interface: "eth0", Address: "203.0.113.1"},
	)
	cfg.GatewayGroups = []GatewayGroup{
		{Name: "spare_group", Enabled: true, Members: []GatewayMember{{Gateway: "gw_eth0"}}},
		{Name: "old_group", Members: []GatewayMember{{Gateway: "gw_eth0"}}},
	}
	cfg.Routes = []StaticRoute{{ID: "old-route", Destination: "198.51.100.64/26", Gateway: "192.0.2.254"}}
	cfg.Crons = []Cron{{ID: "old-cron", Schedule: "@daily", Kind: CronRefreshAliases}}
	cfg.Wireless = Wireless{Country: "DE", Radios: []Radio{
		{Name: "wlan0", Enabled: true, Band: Band5G, Channel: 36, Width: 80, Standard: StandardAX},
		{Name: "wlan1", Band: Band5G, Channel: 44, Width: 80, Standard: StandardAX},
	}}
	cfg.Services.DHCP.StaticLeases = []StaticLease{
		{MAC: "00:00:5e:00:53:01", IP: "192.0.2.50", Hostname: "printer"},
		{MAC: "00:00:5e:00:53:02", IP: "203.0.113.50", Hostname: "spare-box"},
	}
	cfg.Services.Proxy = Proxy{
		Enabled: true,
		Access: []ProxyAccess{
			{ID: "wan-https", Enabled: true, Zone: "wan", Action: ActionAccept, Ports: []string{ProxyPortHTTPS}},
			{ID: "spare-access", Enabled: true, Zone: "spare_zone", Action: ActionAccept, Ports: []string{ProxyPortHTTPS}},
			{ID: "old-access", Zone: "wan", Action: ActionAccept, Ports: []string{ProxyPortHTTP}},
		},
		Pools: []ProxyPool{
			{ID: "web", Upstreams: []ProxyUpstream{{Address: "192.0.2.20:8080"}}},
			{ID: "spare-pool", Upstreams: []ProxyUpstream{{Address: "192.0.2.21:8080"}}},
		},
		Profiles: []WAFProfile{{ID: "spare-waf"}},
		Sites: []ProxySite{
			{ID: "shop", Enabled: true, Hosts: []string{"shop.example.test"}, Pool: "web", Certificate: "shop-cert",
				AllowFrom: []string{"trusted"}},
			{ID: "old-site", Hosts: []string{"old.example.test"}, Pool: "web"},
		},
		Routes: []L4Route{{ID: "old-l4", Protocol: "tcp", Port: 2223,
			Upstreams: []ProxyUpstream{{Address: "192.0.2.40:22"}}}},
	}
	cfg.ACME = ACME{Accounts: []ACMEAccount{
		{ID: "le", Directory: ACMEDirectoryLetsEncryptStaging, PrivateKey: accountKey()},
		{ID: "spare-account", Directory: ACMEDirectoryLetsEncryptStaging, PrivateKey: accountKey()},
	}}
	acme := func(id string, enabled bool, name string) Certificate {
		return Certificate{ID: id, Enabled: enabled, Source: SourceACME, Names: []string{name},
			Account: "le", Challenge: ChallengeHTTP}
	}
	cfg.Certificates = []Certificate{
		acme("shop-cert", true, "shop.example.test"),
		acme("spare-cert", true, "spare.example.test"),
		acme("old-cert", false, "old.example.test"),
	}
	cfg.DNSProviders = []DNSProvider{
		{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"token": "made-up"}, Domains: []string{"example.test"}},
		{ID: "spare-dns", Kind: ProviderExec, Settings: map[string]string{"program": "/bin/true"},
			Domains: []string{"example.invalid"}},
	}
	cfg.Services.DDNS = DDNS{Records: []DDNSRecord{{ID: "home", Name: "home.example.test", Interface: "eth0", IPv4: true}}}
	cfg.Blocking.Lists = []BlockList{{Name: "old_list", URL: "https://lists.example.test/hosts.txt"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	return cfg
}

func TestUnusedFindsOneOfEachKind(t *testing.T) {
	t.Parallel()
	unused, _ := unusedConfig(t).Unused()
	want := []Unused{
		{Kind: "acme-account", ID: "spare-account", Name: "spare-account", Path: "acme.accounts[spare-account]",
			Why: "No certificate uses it"},
		{Kind: "alias", ID: "spare_hosts", Name: "spare_hosts", Path: "aliases[spare_hosts]", Why: "Nothing names it"},
		{Kind: "certificate", ID: "spare-cert", Name: "spare-cert", Path: "certificates[spare-cert]",
			Why: "No site or the web UI uses it"},
		{Kind: "dns-provider", ID: "spare-dns", Name: "spare-dns", Path: "dnsProviders[spare-dns]",
			Why: "No certificate or dynamic DNS record uses it"},
		{Kind: "gateway", ID: "spare_vpn", Name: "spare_vpn", Path: "gateways[spare_vpn]", Why: "No rule routes through it"},
		{Kind: "gateway-group", ID: "spare_group", Name: "spare_group", Path: "gatewayGroups[spare_group]",
			Why: "No rule routes through it"},
		{Kind: "pool", ID: "spare-pool", Name: "spare-pool", Path: "services.proxy.pools[spare-pool]", Why: "No site uses it"},
		{Kind: "radio", ID: "wlan0", Name: "wlan0", Path: "wireless.radios[wlan0]", Why: "No network is on it"},
		{Kind: "schedule", ID: "evenings", Name: "evenings", Path: "schedules[evenings]", Why: "No rule names it"},
		{Kind: "static-lease", ID: "00:00:5e:00:53:02", Name: "spare-box",
			Path: "services.dhcp.staticLeases[00:00:5e:00:53:02]", Why: "Outside every DHCP network"},
		{Kind: "waf-profile", ID: "spare-waf", Name: "spare-waf", Path: "services.proxy.wafProfiles[spare-waf]",
			Why: "No site uses it"},
		{Kind: "zone", ID: "spare_zone", Name: "spare_zone", Path: "zones[spare_zone]", Why: "No interface is in it",
			Takes: []string{"rule spare-in", "rule to-spare", "rule old-spare-in", "outbound NAT spare-snat",
				"outbound NAT old-spare-snat", "port forward spare-fwd", "1:1 NAT spare-1to1", "protection",
				"proxy access rule spare-access"}},
	}
	got, _ := json.MarshalIndent(unused, "", "  ")
	exp, _ := json.MarshalIndent(want, "", "  ")
	if string(got) != string(exp) {
		t.Errorf("unused =\n%s\nwant\n%s", got, exp)
	}
}

func TestUnusedListsWhatIsSwitchedOff(t *testing.T) {
	t.Parallel()
	_, disabled := unusedConfig(t).Unused()
	want := []Disabled{
		{Kind: "block-list", ID: "old_list", Name: "old_list", Path: "blocking.lists[old_list]"},
		{Kind: "certificate", ID: "old-cert", Name: "old-cert", Path: "certificates[old-cert]"},
		{Kind: "cron", ID: "old-cron", Name: "old-cron", Path: "crons[old-cron]"},
		{Kind: "ddns-record", ID: "home", Name: "home.example.test", Path: "services.ddns.records[home]"},
		{Kind: "gateway", ID: "old_gw", Name: "old_gw", Path: "gateways[old_gw]"},
		{Kind: "gateway-group", ID: "old_group", Name: "old_group", Path: "gatewayGroups[old_group]"},
		{Kind: "interface", ID: "eth2", Name: "eth2", Path: "interfaces[eth2]"},
		{Kind: "one-to-one-nat", ID: "old-1to1", Name: "old-1to1", Path: "nat.oneToOne[old-1to1]"},
		{Kind: "outbound-nat", ID: "old-snat", Name: "old-snat", Path: "nat.outbound.rules[old-snat]"},
		{Kind: "outbound-nat", ID: "old-spare-snat", Name: "old-spare-snat", Path: "nat.outbound.rules[old-spare-snat]"},
		{Kind: "port-forward", ID: "old-fwd", Name: "old-fwd", Path: "nat.portForwards[old-fwd]"},
		{Kind: "proxy-access", ID: "old-access", Name: "old-access", Path: "services.proxy.access[old-access]"},
		{Kind: "proxy-route", ID: "old-l4", Name: "old-l4", Path: "services.proxy.routes[old-l4]"},
		{Kind: "proxy-site", ID: "old-site", Name: "old-site", Path: "services.proxy.sites[old-site]"},
		{Kind: "radio", ID: "wlan1", Name: "wlan1", Path: "wireless.radios[wlan1]"},
		{Kind: "rule", ID: "old-rule", Name: "old-rule", Path: "rules[old-rule]"},
		{Kind: "rule", ID: "old-spare-in", Name: "old-spare-in", Path: "rules[old-spare-in]"},
		{Kind: "static-route", ID: "old-route", Name: "old-route", Path: "routes[old-route]"},
		{Kind: "wireguard-peer", ID: "wg1/old_peer", Name: "wg1/old_peer", Path: "interfaces[wg1].wireguard.peers[old_peer]"},
	}
	if !slices.Equal(disabled, want) {
		t.Errorf("disabled =\n%v\nwant\n%v", disabled, want)
	}
}

func unusedKeys(unused []Unused) []UnusedKey {
	var out []UnusedKey
	for _, u := range unused {
		out = append(out, UnusedKey{Kind: u.Kind, ID: u.ID})
	}
	return out
}

func TestRemoveUnusedLeavesAConfigurationThatValidates(t *testing.T) {
	t.Parallel()
	cfg := unusedConfig(t)
	unused, disabled := cfg.Unused()
	if err := cfg.RemoveUnused(unusedKeys(unused)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("after removal: %v", err)
	}
	left, stillOff := cfg.Unused()
	if len(left) != 0 {
		t.Errorf("unused after removal: %v", left)
	}
	// The zone took its switched-off rule and NAT entry with it; nothing
	// else switched off went.
	want := slices.DeleteFunc(slices.Clone(disabled), func(d Disabled) bool {
		return d.ID == "old-spare-in" || d.ID == "old-spare-snat"
	})
	if len(want) != len(disabled)-2 || !slices.Equal(stillOff, want) {
		t.Errorf("disabled after removal: %v, want %v", stillOff, want)
	}
}

func TestRemovingAZoneTakesWhatIsWrittenAgainstIt(t *testing.T) {
	t.Parallel()
	cfg := unusedConfig(t)
	if err := cfg.RemoveUnused([]UnusedKey{{Kind: "zone", ID: "spare_zone"}}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("after removal: %v", err)
	}
	for _, r := range cfg.Rules {
		if r.Zone == "spare_zone" || r.DestZone == "spare_zone" {
			t.Errorf("rule %s is left", r.ID)
		}
	}
	n := cfg.NAT
	if len(n.Outbound.Rules) != 1 || len(n.PortForwards) != 1 || len(n.OneToOne) != 1 {
		t.Errorf("NAT left = %+v", n)
	}
	if !slices.Equal(cfg.Protection.Zones, []string{"wan"}) {
		t.Errorf("protection zones = %v", cfg.Protection.Zones)
	}
	if slices.ContainsFunc(cfg.Services.Proxy.Access, func(a ProxyAccess) bool { return a.Zone == "spare_zone" }) {
		t.Error("the zone's access line is left")
	}
}

func TestRemoveUnusedRefusesWhatIsNotUnused(t *testing.T) {
	t.Parallel()
	for _, k := range []UnusedKey{
		{Kind: "alias", ID: "nothing-by-this-name"},
		{Kind: "made-up-kind", ID: "spare_hosts"},
		{Kind: "alias", ID: "trusted"},
		{Kind: "gateway", ID: "gw_eth0"},
		{Kind: "certificate", ID: "old-cert"},
		{Kind: "certificate", ID: "shop-cert"},
	} {
		cfg := unusedConfig(t)
		before, _ := json.Marshal(cfg)
		err := cfg.RemoveUnused([]UnusedKey{{Kind: "alias", ID: "spare_hosts"}, k})
		if err == nil || !strings.Contains(err.Error(), k.ID) {
			t.Errorf("%v: error %v, want one naming it", k, err)
		}
		if after, _ := json.Marshal(cfg); string(after) != string(before) {
			t.Errorf("%v: the configuration changed on a refusal", k)
		}
	}
}

func TestOnlyTunnelGatewaysCanBeUnused(t *testing.T) {
	t.Parallel()
	cfg := unusedConfig(t)
	unused, _ := cfg.Unused()
	for _, u := range unused {
		if u.Kind == "gateway" && u.ID != "spare_vpn" {
			t.Errorf("gateway %s is listed unused", u.ID)
		}
	}
	cfg.Rules = append(cfg.Rules, Rule{ID: "out-vpn", Enabled: true, Zone: "lan", Action: ActionAccept,
		Protocol: ProtocolAny, Gateway: "spare_vpn"})
	unused, _ = cfg.Unused()
	if slices.ContainsFunc(unused, func(u Unused) bool { return u.Kind == "gateway" }) {
		t.Errorf("a gateway a rule routes through is listed unused: %v", unused)
	}
}

// A lease written as an IPv6 host part takes its prefix from the
// interface, so a server for IPv6 anywhere covers it.
func TestALeaseOnlyIPv6ServesIsUsed(t *testing.T) {
	t.Parallel()
	cfg := unusedConfig(t)
	cfg.Services.DHCP.StaticLeases[1].IP = ""
	cfg.Services.DHCP.StaticLeases[1].IPv6 = "::20"
	if unused, _ := cfg.Unused(); !slices.ContainsFunc(unused, func(u Unused) bool { return u.Kind == "static-lease" }) {
		t.Fatal("a lease with no server for IPv6 is not listed")
	}
	cfg.Interfaces[0].IPv6 = IPv6{Mode: AddrStatic, Address: "2001:db8:1::1/64"}
	cfg.Services.DHCP.V6 = []DHCPv6Server{{Interface: "eth1", Enabled: true, Mode: RAStateless}}
	if unused, _ := cfg.Unused(); slices.ContainsFunc(unused, func(u Unused) bool { return u.Kind == "static-lease" }) {
		t.Error("a host part is listed outside the IPv6 server's network")
	}
	cfg.Services.DHCP.StaticLeases[1].IPv6 = "2001:db8:2::20"
	if unused, _ := cfg.Unused(); !slices.ContainsFunc(unused, func(u Unused) bool { return u.Kind == "static-lease" }) {
		t.Error("an address outside the IPv6 server's network is not listed")
	}
}

// The only lines that let anything reach the proxy keep their zone: the
// proxy would be left with none.
func TestAZoneHoldingTheProxysOnlyOpenLineIsKept(t *testing.T) {
	t.Parallel()
	cfg := unusedConfig(t)
	cfg.Services.Proxy.Access = slices.DeleteFunc(cfg.Services.Proxy.Access, func(a ProxyAccess) bool { return a.ID == "wan-https" })
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	unused, _ := cfg.Unused()
	if slices.ContainsFunc(unused, func(u Unused) bool { return u.Kind == "zone" }) {
		t.Fatalf("the zone holding the proxy's only open line is listed: %v", unused)
	}
	if err := cfg.RemoveUnused(unusedKeys(unused)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("after removal: %v", err)
	}
}

// An empty protection list means every external zone, so the zones it
// names are kept while none of them has an interface. Beside one that
// has, an empty zone goes and takes its entry with it. Only with every
// defence off can the list name nothing but empty zones.
func TestTheLastZoneProtectionNamesIsKept(t *testing.T) {
	t.Parallel()
	zones := func(cfg *Config) []Unused {
		unused, _ := cfg.Unused()
		return slices.DeleteFunc(unused, func(u Unused) bool { return u.Kind != "zone" })
	}
	cfg := unusedConfig(t)
	if got := zones(cfg); len(got) != 1 || !slices.Contains(got[0].Takes, "protection") {
		t.Errorf("beside wan, the empty zone is listed as %v, want it taking protection", got)
	}

	cfg.Protection = Protection{Zones: []string{"spare_zone"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := zones(cfg); len(got) != 0 {
		t.Errorf("the only zone protection names is listed: %v", got)
	}
	err := cfg.RemoveUnused([]UnusedKey{{Kind: "zone", ID: "spare_zone"}})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("removing the only zone protection names: %v, want it in use", err)
	}

	cfg.Zones = append(cfg.Zones, Zone{Name: "quiet_zone", External: true})
	cfg.Protection.Zones = []string{"spare_zone", "quiet_zone"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := zones(cfg); len(got) != 0 {
		t.Errorf("removing these would empty protection's zones: %v", got)
	}
}

// unusedZone is the zone listed unused by that name, if it is.
func unusedZone(cfg *Config, name string) (Unused, bool) {
	unused, _ := cfg.Unused()
	i := slices.IndexFunc(unused, func(u Unused) bool { return u.Kind == "zone" && u.ID == name })
	if i < 0 {
		return Unused{}, false
	}
	return unused[i], true
}

func TestRemovingAZoneTakesItOffDNSEnforcement(t *testing.T) {
	t.Parallel()
	cfg := unusedConfig(t)
	cfg.Zones = append(cfg.Zones, Zone{Name: "spare_lan"})
	cfg.Blocking.Enforce.Zones = []string{"lan", "spare_lan"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if u, ok := unusedZone(cfg, "spare_lan"); !ok || !slices.Contains(u.Takes, "DNS enforcement") {
		t.Errorf("beside lan, the empty zone is listed as %+v (%t), want it taking DNS enforcement", u, ok)
	}
	if err := cfg.RemoveUnused([]UnusedKey{{Kind: "zone", ID: "spare_lan"}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Blocking.Enforce.Zones, []string{"lan"}) {
		t.Errorf("enforced zones = %v, want [lan]", cfg.Blocking.Enforce.Zones)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("after removal: %v", err)
	}
}

// An empty enforcement list means every zone that is not external, so the
// zones it names are kept while none of them has an interface.
func TestTheLastZoneDNSEnforcementNamesIsKept(t *testing.T) {
	t.Parallel()
	cfg := unusedConfig(t)
	cfg.Zones = append(cfg.Zones, Zone{Name: "spare_lan"})
	cfg.Blocking.Enforce.Zones = []string{"spare_lan"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if u, ok := unusedZone(cfg, "spare_lan"); ok {
		t.Errorf("the only zone DNS enforcement names is listed: %+v", u)
	}
	err := cfg.RemoveUnused([]UnusedKey{{Kind: "zone", ID: "spare_lan"}})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("removing the only zone DNS enforcement names: %v, want it in use", err)
	}

	cfg.Zones = append(cfg.Zones, Zone{Name: "quiet_lan"})
	cfg.Blocking.Enforce.Zones = []string{"spare_lan", "quiet_lan"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range cfg.Blocking.Enforce.Zones {
		if u, ok := unusedZone(cfg, name); ok {
			t.Errorf("removing it would empty DNS enforcement's zones: %+v", u)
		}
	}
}

func TestAnAliasDNSEnforcementExceptsIsUsed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field string
		set   func(*DNSEnforce)
	}{
		{"exemptClients", func(e *DNSEnforce) { e.ExemptClients = "spare_hosts" }},
		{"exemptDestinations", func(e *DNSEnforce) { e.ExemptDestinations = "spare_hosts" }},
	} {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			cfg := unusedConfig(t)
			tc.set(&cfg.Blocking.Enforce)
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			if unused, _ := cfg.Unused(); slices.ContainsFunc(unused, func(u Unused) bool { return u.Kind == "alias" }) {
				t.Errorf("an alias %s names is listed: %v", tc.field, unused)
			}
			if err := cfg.RemoveUnused([]UnusedKey{{Kind: "alias", ID: "spare_hosts"}}); err == nil {
				t.Errorf("an alias %s names was removed", tc.field)
			}
		})
	}
}
