package nft

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadConfig(t *testing.T, path string) *model.Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	// An ACME account needs a key to validate, and a private key has no
	// business in a repository. The fixtures leave it empty and get a
	// throwaway one here; nothing signs with it.
	for i := range cfg.ACME.Accounts {
		if cfg.ACME.Accounts[i].PrivateKey == "" {
			cfg.ACME.Accounts[i].PrivateKey = accountKey(t)
		}
	}
	return &cfg
}

func accountKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

func goldenCases(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("testdata/*.json")
	if err != nil || len(matches) == 0 {
		t.Fatalf("no golden inputs: %v", err)
	}
	return matches
}

func TestRenderGolden(t *testing.T) {
	t.Parallel()
	for _, in := range goldenCases(t) {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, in)
			got, err := Render(cfg)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			golden := strings.TrimSuffix(in, ".json") + ".nft"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden %s (run with -update): %v", golden, err)
			}
			if got != string(want) {
				t.Errorf("render mismatch for %s (run with -update to accept)\n--- got ---\n%s", name, got)
			}
		})
	}
}

func TestRenderRejectsInvalid(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{Version: model.SchemaVersion}
	if _, err := Render(cfg); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/full.json")
	a, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("render is not deterministic")
	}
}

func TestEmptyRuleset(t *testing.T) {
	t.Parallel()
	s := EmptyRuleset()
	if !strings.Contains(s, "delete table inet ostiole") || strings.Contains(s, "{") {
		t.Errorf("unexpected empty ruleset:\n%s", s)
	}
}

func TestParseCounters(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"nftables":[
	  {"metainfo":{"version":"1.1.6"}},
	  {"rule":{"family":"inet","table":"ostiole","chain":"zone_lan","comment":"id:r2","expr":[{"match":{}},{"counter":{"packets":5,"bytes":500}},{"accept":null}]}},
	  {"rule":{"family":"inet","table":"ostiole","chain":"zone_lan","comment":"id:r2","expr":[{"counter":{"packets":1,"bytes":10}},{"accept":null}]}},
	  {"rule":{"family":"inet","table":"ostiole","chain":"input","comment":"default-drop","expr":[{"counter":{"packets":7,"bytes":70}}]}},
	  {"rule":{"family":"inet","table":"ostiole","chain":"input","expr":[{"counter":{"packets":9,"bytes":90}}]}}
	]}`)
	c, err := ParseCounters(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := c["r2"]; got != (Counter{Packets: 6, Bytes: 510}) {
		t.Errorf("r2 = %+v", got)
	}
	if got := c["input/default-drop"]; got != (Counter{Packets: 7, Bytes: 70}) {
		t.Errorf("input/default-drop = %+v", got)
	}
	if len(c) != 2 {
		t.Errorf("len = %d, want 2 (uncommented rules ignored)", len(c))
	}
}

// Per-interface drop logging: the common case stays one rule, and a router
// with an exception gets exactly one more.
func TestDefaultDropLogging(t *testing.T) {
	t.Parallel()
	base := func(global bool, overrides map[string]bool) *model.Config {
		cfg := loadConfig(t, "testdata/minimal.json")
		cfg.System.Management.LogDefaultDrops = global
		for i := range cfg.Interfaces {
			if v, ok := overrides[cfg.Interfaces[i].Name]; ok {
				cfg.Interfaces[i].LogDrops = &v
			}
		}
		return cfg
	}
	iface := loadConfig(t, "testdata/minimal.json").Interfaces[0].Name

	cases := map[string]struct {
		global    bool
		overrides map[string]bool
		want      []string
		notWant   []string
	}{
		"off everywhere": {false, nil,
			[]string{`counter comment "default-drop"`}, []string{"log prefix"}},
		"on everywhere": {true, nil,
			[]string{`counter log prefix "ostiole:c:input:drop: " group 1 comment "default-drop"`}, []string{"default-drop:"}},
		"one interface opts in": {false, map[string]bool{iface: true},
			[]string{`iifname "` + iface + `" counter log prefix "ostiole:c:input:drop: " group 1 drop comment "default-drop:logged"`,
				`counter comment "default-drop"`}, nil},
		"one interface opts out": {true, map[string]bool{iface: false},
			[]string{`iifname "` + iface + `" counter drop comment "default-drop:quiet"`,
				`counter log prefix "ostiole:c:input:drop: " group 1 comment "default-drop"`}, nil},
		"an override that agrees changes nothing": {true, map[string]bool{iface: true},
			[]string{`counter log prefix "ostiole:c:input:drop: " group 1 comment "default-drop"`}, []string{"default-drop:"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := Render(base(tc.global, tc.overrides))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
			for _, no := range tc.notWant {
				if strings.Contains(got, no) {
					t.Errorf("unexpected %q:\n%s", no, got)
				}
			}
		})
	}
}

// Blocking by source address must happen before anything accepts, after
// the state check so a flow already up is not cut, and after the
// link-local baseline: the fetched bogon list contains fe80::/10, and a
// WAN whose neighbour discovery is dropped never gets an IPv6 address.
func TestBlockedSourcesComeFirst(t *testing.T) {
	t.Parallel()
	got, err := Render(loadConfig(t, "testdata/blocked-sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	inputAt := strings.Index(got, "chain input")
	forwardAt := strings.Index(got, "chain forward")
	if inputAt < 0 || forwardAt < inputAt {
		t.Fatalf("the base chains are not where they should be:\n%s", got)
	}
	input := got[inputAt:forwardAt]
	state := strings.Index(input, "ct state invalid drop")
	icmp := strings.Index(input, "icmpv6 type")
	dhcp6 := strings.Index(input, "client:dhcpv6")
	block := strings.Index(input, "block-private")
	lockout := strings.Index(input, "anti-lockout")
	if state < 0 || icmp < state || dhcp6 < icmp || block < dhcp6 || lockout < block {
		t.Errorf("blocks are in the wrong place (state %d, icmp %d, dhcpv6 %d, block %d, anti-lockout %d):\n%s",
			state, icmp, dhcp6, block, lockout, input)
	}
	// Both chains: a spoofed source being routed through is the same
	// problem as one addressed to this router.
	if !strings.Contains(got[forwardAt:], "block-private") {
		t.Error("forwarded traffic is not checked")
	}
	// Link-local is never blocked: IPv6 needs it for neighbour discovery
	// and for the default route on a WAN.
	for line := range strings.SplitSeq(got, "\n") {
		if strings.Contains(line, "fe80::") && strings.Contains(line, "drop") {
			t.Errorf("link-local was blocked, which would take IPv6 down: %s", strings.TrimSpace(line))
		}
	}
	// The bogon sets exist before anything is fetched, so a refresh has
	// somewhere to put the list.
	for _, set := range []string{"set bogons_v4", "set bogons_v6"} {
		if !strings.Contains(got, set) {
			t.Errorf("missing %q", set)
		}
	}
}

// The answers to this router's own DHCPv6 requests come from the server's
// link-local address, which conntrack cannot pair with the multicast
// request, so an interface that may run the client needs a rule of its
// own. One that cannot run it gets none.
func TestDHCPv6ClientRule(t *testing.T) {
	t.Parallel()
	const rule = `iifname "eth0" ip6 saddr fe80::/10 ip6 daddr fe80::/10 udp sport 547 udp dport 546 counter accept comment "client:dhcpv6"`

	// blocked-sources: eth0 is a WAN in slaac mode, and the only such interface.
	got, err := Render(loadConfig(t, "testdata/blocked-sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, rule) {
		t.Errorf("slaac WAN has no DHCPv6 client rule:\n%s", got)
	}
	rows, err := SystemRules(loadConfig(t, "testdata/blocked-sources.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if len(r.Keys) == 1 && r.Keys[0] == "input/client:dhcpv6" {
			found = true
			if got, want := strings.Join(r.Zones, ","), "wan"; got != want {
				t.Errorf("DHCPv6 client row names zones %q, want %q", got, want)
			}
			if r.Setting != "interface" {
				t.Errorf("DHCPv6 client row is controlled by %q, want interface", r.Setting)
			}
		}
	}
	if !found {
		t.Error("no system rule row for the DHCPv6 client rule")
	}

	// hybrid-nat: no interface listens to router advertisements or asks
	// for an address.
	got, err = Render(loadConfig(t, "testdata/hybrid-nat.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "client:dhcpv6") {
		t.Errorf("DHCPv6 client rule rendered with no client to serve:\n%s", got)
	}

	// dhcp mode runs the client outright.
	cfg := loadConfig(t, "testdata/hybrid-nat.json")
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "eth0" {
			cfg.Interfaces[i].IPv6.Mode = model.AddrDHCP
		}
	}
	got, err = Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, rule) {
		t.Errorf("dhcp WAN has no DHCPv6 client rule:\n%s", got)
	}
}

// A node with no port opens nothing and still works, through a relay. The
// rest of what it needs comes from the zone its interface is in.
func TestTailscaleRuleFollowsThePort(t *testing.T) {
	t.Parallel()
	const rule = `iifname "eth0" udp dport 41641 counter accept comment "service:tailscale"`

	cfg := loadConfig(t, "testdata/tailscale.json")
	got, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, rule) {
		t.Errorf("no rule for the port peers dial:\n%s", got)
	}
	rows, err := SystemRules(loadConfig(t, "testdata/tailscale.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := findRow(rows, "Tailscale peers dialling in")
	if !ok {
		t.Fatal("no system rule row for the Tailscale rule")
	}
	if row.Setting != "tailscale" {
		t.Errorf("row is controlled by %q, want tailscale", row.Setting)
	}

	for _, tc := range []struct {
		name string
		edit func(*model.Interface)
	}{
		{"no port", func(in *model.Interface) { in.Tailscale.Port = 0 }},
		{"disabled", func(in *model.Interface) { in.Enabled = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, "testdata/tailscale.json")
			for i := range cfg.Interfaces {
				if cfg.Interfaces[i].Tailscale != nil {
					tc.edit(&cfg.Interfaces[i])
				}
			}
			got, err := Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, "service:tailscale") {
				t.Errorf("a port was opened anyway:\n%s", got)
			}
		})
	}
}

func TestACMERuleFollowsTheChallenge(t *testing.T) {
	t.Parallel()
	const rule = `iifname "eth0" tcp dport 80 counter accept comment "service:acme"`

	got, err := Render(loadConfig(t, "testdata/acme-http01.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, rule) {
		t.Errorf("no rule for the challenge:\n%s", got)
	}
	rows, err := SystemRules(loadConfig(t, "testdata/acme-http01.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := findRow(rows, "Certificate challenges")
	if !ok {
		t.Fatal("no system rule row for the challenge rule")
	}
	if row.Setting != "certificates" {
		t.Errorf("row is controlled by %q, want certificates", row.Setting)
	}

	for _, tc := range []struct {
		name string
		edit func(*model.Certificate)
	}{
		{"disabled", func(c *model.Certificate) { c.Enabled = false }},
		{"over dns", func(c *model.Certificate) {
			c.Challenge, c.Provider = model.ChallengeDNS, "dns"
			c.Names, c.InterfaceAddresses = []string{"router.example.test"}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, "testdata/acme-http01.json")
			cfg.ACME.Providers = []model.DNSProvider{
				{ID: "dns", Kind: "exec", Settings: map[string]string{"program": "/bin/true"}},
			}
			tc.edit(&cfg.Certificates[0])
			got, err := Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, "service:acme") {
				t.Errorf("port 80 was opened anyway:\n%s", got)
			}
		})
	}
}

func TestProxyRulesSitAtTheZoneTail(t *testing.T) {
	t.Parallel()
	const (
		tcp = `fib daddr type local tcp dport { 80, 443 } counter accept comment "service:proxy"`
		udp = `fib daddr type local udp dport { 443, 5353 } counter accept comment "service:proxy-udp"`
	)

	got, err := Render(loadConfig(t, "testdata/proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{tcp, udp} {
		if !strings.Contains(got, want) {
			t.Errorf("no proxy rule %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `iifname "eth0" tcp dport { 80, 443 }`) {
		t.Errorf("the rule is in the zone chain, so it needs no interface match:\n%s", got)
	}
	rows, err := SystemRules(loadConfig(t, "testdata/proxy.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var proxy []SystemRule
	for _, r := range rows {
		if r.Setting == "proxy" {
			proxy = append(proxy, r)
		}
	}
	if len(proxy) != 2 {
		t.Fatalf("%d rows for the proxy, want 2", len(proxy))
	}
	for _, r := range proxy {
		if !r.After {
			t.Errorf("%s row is not marked as evaluated after the zone's rules", r.Protocol)
		}
		if got, want := strings.Join(r.Zones, ","), "wan"; got != want {
			t.Errorf("%s row zones = %q, want %q", r.Protocol, got, want)
		}
	}

	for _, tc := range []struct {
		name string
		edit func(*model.Proxy)
	}{
		{"disabled", func(p *model.Proxy) { p.Enabled = false }},
		{"nothing to serve", func(p *model.Proxy) { p.Sites, p.Routes = nil, nil }},
		{"another zone", func(p *model.Proxy) { p.Zones = []string{"lan"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, "testdata/proxy.json")
			tc.edit(&cfg.Services.Proxy)
			got, err := Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(sectionOf(got, "chain zone_wan"), "service:proxy") {
				t.Errorf("wan was opened anyway:\n%s", got)
			}
		})
	}
}

// With the proxy up, port 80 is its own: the challenge reaches the solver
// through the proxy's own route, so the input rule would open nothing.
func TestProxyTakesOverTheChallengePort(t *testing.T) {
	t.Parallel()
	got, err := Render(loadConfig(t, "testdata/acme-http01-proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "service:acme") {
		t.Errorf("port 80 is opened twice:\n%s", got)
	}
	if !strings.Contains(got, `fib daddr type local tcp dport { 80, 443 } counter accept comment "service:proxy"`) {
		t.Errorf("the proxy does not carry port 80:\n%s", got)
	}
}

// sectionOf returns the lines of one chain block.
func sectionOf(ruleset, header string) string {
	_, rest, ok := strings.Cut(ruleset, header+" {")
	if !ok {
		return ""
	}
	body, _, _ := strings.Cut(rest, "\n\t}")
	return body
}

// The baseline accepts a family only where it is turned on: an interface
// whose IPv6 mode is none is left out of the neighbour discovery rule and
// the DHCPv6 client rule, and a router with no IPv6 at all carries neither.
func TestBaselineFollowsAddressModes(t *testing.T) {
	t.Parallel()
	// blocked-sources: eth0 slaac, eth1 static, eth2 none.
	got, err := Render(loadConfig(t, "testdata/blocked-sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `iifname { "eth0", "eth1" } icmpv6 type {`) {
		t.Errorf("neighbour discovery is not scoped to the interfaces with IPv6:\n%s", got)
	}
	rows, err := SystemRules(loadConfig(t, "testdata/blocked-sources.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	zones := func(description string) string {
		for _, r := range rows {
			if r.Description == description {
				return strings.Join(r.Zones, ",")
			}
		}
		return "(no row)"
	}
	if got, want := zones("IPv6 neighbour discovery and errors"), "wan,lan"; got != want {
		t.Errorf("IPv6 row names zones %q, want %q", got, want)
	}
	if got, want := zones("ICMP errors"), "wan,lan,iot"; got != want {
		t.Errorf("IPv4 row names zones %q, want %q", got, want)
	}

	cfg := loadConfig(t, "testdata/blocked-sources.json")
	for i := range cfg.Interfaces {
		cfg.Interfaces[i].IPv6 = model.IPv6{Mode: model.AddrNone}
	}
	cfg.Services.DHCP.V6 = nil
	got, err = Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"icmpv6 type", "client:dhcpv6"} {
		if strings.Contains(got, s) {
			t.Errorf("no interface has IPv6, yet the ruleset carries %q:\n%s", s, got)
		}
	}
	if !strings.Contains(got, "icmp type {") {
		t.Errorf("IPv4 errors went missing with IPv6:\n%s", got)
	}
	rows, err = SystemRules(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Description == "IPv6 neighbour discovery and errors" {
			t.Error("IPv6 row shown for a router with no IPv6")
		}
	}
}

// A fetched bogon list lands in the sets the rules already match on.
func TestBogonListFillsTheSets(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/blocked-sources.json")
	got, err := RenderWithFeeds(cfg, map[string][]string{
		BogonFeed: {"192.0.2.0/24", "2001:db8::/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "elements = { 192.0.2.0/24 }") {
		t.Errorf("the IPv4 bogons are not in the set:\n%s", got)
	}
	if !strings.Contains(got, "elements = { 2001:db8::/32 }") {
		t.Errorf("the IPv6 bogons are not in the set:\n%s", got)
	}
	// And the same sets are what a refresh replaces.
	sets := BlockSets(cfg, []string{"192.0.2.0/24"})
	if len(sets) != 2 || sets[0].Name != "bogons_v4" {
		t.Errorf("BlockSets = %+v", sets)
	}
	if got := BlockSets(loadConfig(t, "testdata/minimal.json"), nil); got != nil {
		t.Errorf("a router that blocks no bogons has no sets to fill: %+v", got)
	}
}

// Enforcement does not wait for the lists, and the drops do not wait for the
// DNS server either: only the redirect needs a resolver here to send
// clients to, and validation refuses it without one.
func TestDNSEnforcementWithoutListsOrServer(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/dns-blocking.json")
	cfg.Blocking.Enabled = false
	got, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"chain block_dns", `comment "block:dot"`, `comment "block:doh"`, `comment "block:dns-redirect"`} {
		if !strings.Contains(got, want) {
			t.Errorf("lists off: no %s in:\n%s", want, got)
		}
	}

	cfg.Services.DNS.Enabled = false
	if _, err := Render(cfg); err == nil || !strings.Contains(err.Error(), "every client would lose DNS") {
		t.Errorf("the redirect with the DNS server off was rendered rather than refused: %v", err)
	}
	cfg.Blocking.Enforce.RedirectDNS = false
	got, err = Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"chain block_dns", `comment "block:dot"`, `comment "block:doh"`} {
		if !strings.Contains(got, want) {
			t.Errorf("DNS server off: no %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "block:dns-redirect") {
		t.Errorf("DNS server off but plain DNS is redirected to it:\n%s", got)
	}
}

// A NAT statement ends the chain, so a mapped host has to meet its 1:1
// rule before the masquerade or an outbound rule can claim it. That holds
// in every mode, as binat comes first in pf.
func TestOneToOneComesBeforeOutboundNAT(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode  model.OutboundMode
		after []string
	}{
		{model.OutboundAutomatic, []string{"auto-nat:wan"}},
		{model.OutboundHybrid, []string{"id:nat-all", "auto-nat:wan"}},
		{model.OutboundManual, []string{"id:nat-all"}},
		{model.OutboundDisabled, nil},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, "testdata/minimal.json")
			cfg.NAT.Outbound.Mode = tc.mode
			if tc.mode == model.OutboundHybrid || tc.mode == model.OutboundManual {
				cfg.NAT.Outbound.Rules = []model.OutboundRule{{ID: "nat-all", Enabled: true, Zone: "wan"}}
			}
			cfg.NAT.OneToOne = []model.OneToOneNAT{
				{ID: "one-mail", Enabled: true, Zone: "wan", External: "203.0.113.10", Internal: "192.168.1.25"},
			}
			got, err := Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, post, ok := strings.Cut(got, "chain nat_postrouting")
			if !ok {
				t.Fatalf("no nat_postrouting chain:\n%s", got)
			}
			one := strings.Index(post, `comment "id:one-mail"`)
			if one < 0 {
				t.Fatalf("no 1:1 rule:\n%s", post)
			}
			for _, c := range tc.after {
				at := strings.Index(post, fmt.Sprintf("comment %q", c))
				if at < 0 {
					t.Errorf("no %s:\n%s", c, post)
				} else if at < one {
					t.Errorf("%s comes before the 1:1 rule, so the mapping never applies:\n%s", c, post)
				}
			}
		})
	}
}
