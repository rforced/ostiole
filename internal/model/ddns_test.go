package model

import (
	"strings"
	"testing"
)

func ddnsConfig() *Config {
	cfg := certConfig()
	cfg.DNSProviders = []DNSProvider{
		{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"token": "t"}, Domains: []string{"example.com"}},
		{ID: "hetzner", Kind: "hetzner", Settings: map[string]string{"token": "t"}, Domains: []string{"example.org"}},
	}
	return cfg
}

func TestValidateAcceptsDDNSRecords(t *testing.T) {
	t.Parallel()
	cfg := ddnsConfig()
	cfg.Services.DDNS.Records = []DDNSRecord{
		{ID: "ddns-home", Enabled: true, Name: "home.example.com", Interface: "wan0", IPv4: true},
		// The same name's AAAA from the LAN, where the global address is.
		{ID: "ddns-home6", Enabled: true, Name: "Home.Example.com.", Interface: "lan0", IPv6: true},
		{ID: "ddns-apex", Name: "example.com", Interface: "wan0", IPv4: true, IPv6: true},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("records rejected: %v", err)
	}
}

func TestValidateCatchesDDNSMistakes(t *testing.T) {
	t.Parallel()
	cfg := ddnsConfig()
	cfg.Services.DDNS.Records = []DDNSRecord{
		{ID: "bad id", Name: "", Interface: "wan0", IPv4: true},
		{ID: "wild", Name: "*.example.com", Interface: "wan0", IPv4: true},
		{ID: "unicode", Name: "bücher.example.com", Interface: "wan0", IPv4: true},
		{ID: "junk", Name: "not a name", Interface: "wan0", IPv4: true},
		{ID: "elsewhere", Name: "home.example.net", Interface: "wan0", IPv4: true},
		{ID: "hetzner", Name: "home.example.org", Interface: "wan0", IPv4: true},
		{ID: "ghost", Name: "a.example.com", Interface: "eth9", IPv4: true},
		{ID: "neither", Name: "b.example.com", Interface: "wan0"},
		{ID: "first", Name: "c.example.com", Interface: "wan0", IPv4: true},
		{ID: "second", Name: "C.example.com", Interface: "lan0", IPv4: true},
	}
	got := issuesByPath(t, cfg)
	for path, want := range map[string]string{
		"services.ddns.records[0].id":        "must match",
		"services.ddns.records[0].name":      "a name is needed",
		"services.ddns.records[1].name":      "wildcard",
		"services.ddns.records[2].name":      "punycode",
		"services.ddns.records[3].name":      "not a domain name",
		"services.ddns.records[4].name":      "no DNS provider holds home.example.net",
		"services.ddns.records[5].name":      "Hetzner cannot keep a dynamic DNS record (Cloudflare can)",
		"services.ddns.records[6].interface": "unknown interface",
		"services.ddns.records[7].ipv4":      "A record, the AAAA record or both",
		"services.ddns.records[9].name":      "first already keeps the A record",
	} {
		if !strings.Contains(got[path], want) {
			t.Errorf("%s = %q, want it to say %q", path, got[path], want)
		}
	}
	if msg, bad := got["services.ddns.records[8].name"]; bad {
		t.Errorf("the first record of a name was refused: %s", msg)
	}
}
