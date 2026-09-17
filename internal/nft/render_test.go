package nft

import (
	"encoding/json"
	"flag"
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
	return &cfg
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
	a, _ := Render(cfg)
	b, _ := Render(cfg)
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
			[]string{`counter log prefix "ostiole:input:drop: " group 1 comment "default-drop"`}, []string{"default-drop:"}},
		"one interface opts in": {false, map[string]bool{iface: true},
			[]string{`iifname "` + iface + `" counter log prefix "ostiole:input:drop: " group 1 drop comment "default-drop:logged"`,
				`counter comment "default-drop"`}, nil},
		"one interface opts out": {true, map[string]bool{iface: false},
			[]string{`iifname "` + iface + `" counter drop comment "default-drop:quiet"`,
				`counter log prefix "ostiole:input:drop: " group 1 comment "default-drop"`}, nil},
		"an override that agrees changes nothing": {true, map[string]bool{iface: true},
			[]string{`counter log prefix "ostiole:input:drop: " group 1 comment "default-drop"`}, []string{"default-drop:"}},
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
	for _, line := range strings.Split(got, "\n") {
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
