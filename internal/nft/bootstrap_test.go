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
