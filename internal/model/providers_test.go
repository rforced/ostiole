package model

import (
	"encoding/json"
	"net/netip"
	"os"
	"strings"
	"testing"
)

// The cases are in testdata so the web UI's copy of ProviderFor is held
// to the same answers.
func TestProviderForTakesTheLongestDomain(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/provider-for.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Providers []DNSProvider `json:"providers"`
		Cases     []struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
			Zone     string `json:"zone"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{DNSProviders: table.Providers}
	for _, tc := range table.Cases {
		p, zone, ok := cfg.ProviderFor(tc.Name)
		id := ""
		if ok {
			id = p.ID
		}
		if id != tc.Provider || zone != tc.Zone {
			t.Errorf("ProviderFor(%q) = %q in %q, want %q in %q", tc.Name, id, zone, tc.Provider, tc.Zone)
		}
	}
}

func TestValidateChecksProviderDomains(t *testing.T) {
	t.Parallel()
	cfg := certConfig()
	cfg.DNSProviders = []DNSProvider{
		{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"token": "t"},
			Domains: []string{"example.com", "*.example.net", "com", "exa mple.org", "example.com."}},
		{ID: "home", Kind: "cloudflare", Settings: map[string]string{"token": "t"},
			Domains: []string{"home.example.com", "Example.COM"}},
	}
	got := issuesByPath(t, cfg)
	for path, want := range map[string]string{
		"dnsProviders[0].domains[1]": "wildcard",
		"dnsProviders[0].domains[2]": "two labels",
		"dnsProviders[0].domains[3]": "not a domain name",
		"dnsProviders[0].domains[4]": "listed twice",
		"dnsProviders[1].domains[1]": "held by cf",
	} {
		if !strings.Contains(got[path], want) {
			t.Errorf("%s = %q, want it to say %q", path, got[path], want)
		}
	}
	for _, path := range []string{"dnsProviders[0].domains[0]", "dnsProviders[1].domains[0]"} {
		if msg, bad := got[path]; bad {
			t.Errorf("%s refused: %s", path, msg)
		}
	}
}

func TestPublicAddress(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"198.51.100.4":        true,
		"2001:db8::1":         true,
		"::ffff:198.51.100.4": true,
		"192.168.1.1":         false,
		"10.0.0.1":            false,
		"100.64.0.1":          false,
		"127.0.0.1":           false,
		"169.254.1.1":         false,
		"fe80::1":             false,
		"fd00::1":             false,
		"::1":                 false,
		"224.0.0.1":           false,
	} {
		if got := PublicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("PublicAddress(%s) = %v, want %v", addr, got, want)
		}
	}
}
