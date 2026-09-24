package nft

import (
	"strings"
	"testing"
)

func TestBootstrap(t *testing.T) {
	t.Parallel()
	r := Bootstrap([]uint16{443, 22, 22, 0})
	for _, want := range []string{
		"delete table inet ostiole",
		"policy drop;",
		`tcp dport { 443, 22 } accept comment "bootstrap:management"`,
		"ct state established,related accept",
		"udp dport 68 accept",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("bootstrap lacks %q:\n%s", want, r)
		}
	}
	// One port is written bare, and no port means no management rule at
	// all rather than a broken one.
	if !strings.Contains(Bootstrap([]uint16{8443}), "tcp dport 8443 accept") {
		t.Error("a single port is not written bare")
	}
	if strings.Contains(Bootstrap(nil), "tcp dport") {
		t.Error("no ports still wrote a management rule")
	}
	// Forwarding is off until the first apply says what the zones are.
	if !strings.Contains(r, "chain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n\t}") {
		t.Errorf("forward chain is not a plain drop:\n%s", r)
	}
}

// A router whose saved ruleset will not load keeps the management ports its
// configuration names, on its anti-lockout interfaces only, and forwards
// nothing.
func TestFallbackKeepsManagementOnTheAntiLockoutInterfaces(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/minimal.json")
	r := Fallback(cfg, []uint16{9443, 22})
	for _, want := range []string{
		"# Fallback ruleset",
		"policy drop;",
		`iifname "eth1" tcp dport { 443, 22 } accept comment "fallback:management"`,
		`iifname "eth1" udp dport { 67, 547 } accept comment "fallback:dhcp"`,
		"chain forward {\n\t\ttype filter hook forward priority filter; policy drop;\n\t}",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("fallback lacks %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "9443") {
		t.Error("the default ports were used over the configuration's")
	}

	// No zone keeps anybody in: the ports open from anywhere rather than
	// from nowhere, as on a router that was just installed.
	for i := range cfg.Zones {
		cfg.Zones[i].AntiLockout = false
	}
	if r := Fallback(cfg, nil); !strings.Contains(r, "\t\ttcp dport { 443, 22 } accept comment \"fallback:management\"") ||
		strings.Contains(r, `"fallback:dhcp"`) {
		t.Errorf("fallback without an anti-lockout zone:\n%s", r)
	}
	// No configuration at all: the ports it was given, from anywhere.
	if r := Fallback(nil, []uint16{9443, 22}); !strings.Contains(r, "\t\ttcp dport { 9443, 22 } accept") {
		t.Errorf("fallback without a configuration:\n%s", r)
	}
}
