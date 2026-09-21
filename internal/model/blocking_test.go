package model

import (
	"strings"
	"testing"
)

func TestNeverBlockedCoversTheExpandedNames(t *testing.T) {
	c := &Config{}
	c.System.Hostname = "gateway"
	c.Services.DNS.Domain = "lan"
	c.Services.DNS.HostOverrides = []HostOverride{
		{Hostname: "printer", IP: "192.168.1.5", Aliases: []string{"print"}},
		{Hostname: "potato", Domain: "test", IP: "192.168.1.6"},
	}
	c.Services.DHCP.StaticLeases = []StaticLease{{MAC: "aa:bb:cc:dd:ee:01", Hostname: "nas"}}

	got := map[string]bool{}
	for _, n := range c.NeverBlocked() {
		got[n] = true
	}
	// dnsmasq is told expand-hosts, so both forms answer and both matter.
	for _, want := range []string{"lan", "gateway", "gateway.lan", "printer", "printer.lan", "print", "print.lan", "nas", "nas.lan", "potato.test"} {
		if !got[want] {
			t.Errorf("%q is not protected; have %v", want, c.NeverBlocked())
		}
	}
	// A name in another domain answers in full only; neither the bare
	// label nor the local domain on the end is ever asked for.
	for _, unwanted := range []string{"potato", "potato.test.lan"} {
		if got[unwanted] {
			t.Errorf("%q is protected but never answered; have %v", unwanted, c.NeverBlocked())
		}
	}
}

func TestDenyingAParentOfOurOwnNameIsRefused(t *testing.T) {
	c := starterForBlocking()
	c.Blocking.Deny = []string{"lan"}
	if err := c.Validate(); err == nil {
		t.Fatal("denying the local domain was accepted")
	}
	// A name that merely looks similar is fine.
	c.Blocking.Deny = []string{"notlan"}
	if err := c.Validate(); err != nil {
		t.Fatalf("an unrelated name was refused: %v", err)
	}
}

func TestDenyingADelegatedDomainIsRefused(t *testing.T) {
	c := starterForBlocking()
	c.Services.DNS.DomainOverrides = []DomainOverride{{Domain: "ts.net", Servers: []string{"100.100.100.100"}}}
	c.Blocking.Deny = []string{"net"}
	err := c.Validate()
	if err == nil {
		t.Fatal("denying a parent of a delegated domain was accepted")
	}
	if !strings.Contains(err.Error(), "undo the delegation") {
		t.Errorf("unhelpful error: %v", err)
	}
	// A name inside the delegation is not a contradiction in itself; the
	// render drops it because the whole subtree belongs to another resolver.
	c.Blocking.Deny = []string{"ads.example.com"}
	if err := c.Validate(); err != nil {
		t.Fatalf("an unrelated name was refused: %v", err)
	}
}

func TestBlockingNeedsTheDNSServer(t *testing.T) {
	c := starterForBlocking()
	c.Services.DNS.Enabled = false
	err := c.Validate()
	if err == nil {
		t.Fatal("blocking with the DNS server off was accepted")
	}
	if !strings.Contains(err.Error(), "dnsmasq is what refuses the names") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// The redirect sends every client's DNS to this router, so this router had
// better be answering.
func TestRedirectNeedsTheDNSServer(t *testing.T) {
	c := starterForBlocking()
	c.Blocking.Enabled = false
	c.Blocking.Enforce.RedirectDNS = true
	if err := c.Validate(); err != nil {
		t.Fatalf("the redirect was refused with the DNS server on: %v", err)
	}
	c.Services.DNS.Enabled = false
	err := c.Validate()
	if err == nil {
		t.Fatal("the redirect with the DNS server off was accepted")
	}
	if !strings.Contains(err.Error(), "every client would lose DNS") {
		t.Errorf("unhelpful error: %v", err)
	}
	// The drops need no resolver here, so they are fine on their own.
	c.Blocking.Enforce.RedirectDNS = false
	c.Blocking.Enforce.BlockDoT = true
	if err := c.Validate(); err != nil {
		t.Fatalf("dropping DoT with the DNS server off was refused: %v", err)
	}
}

// starterForBlocking is the first configuration `ostiole init` writes, with
// the DNS server on and blocking turned on over it.
func starterForBlocking() *Config {
	c := Starter(StarterOptions{Hostname: "gateway"})
	c.Services.DNS.Enabled = true
	c.Services.DNS.Domain = "lan"
	c.Services.DNS.Upstreams = []string{"1.1.1.1"}
	c.Blocking = Blocking{
		Enabled: true,
		Lists:   []BlockList{{Name: "ads", Enabled: true, URL: "https://example.test/hosts"}},
	}
	return c
}

// The UI picks countries from a list now, so this is the only thing standing
// between a hand-written configuration and a GeoIP alias that fetches 404s.
func TestCountryAliasRefusesWhatIsNotACode(t *testing.T) {
	c := starterForBlocking()
	c.Aliases = []Alias{{Name: "countries", Type: AliasGeoIP, Entries: []string{"de", "not-a-country"}}}
	err := c.Validate()
	if err == nil {
		t.Fatal("a GeoIP alias with junk in it was accepted")
	}
	if !strings.Contains(err.Error(), "two-letter country code") {
		t.Errorf("unhelpful error: %v", err)
	}

	c.Aliases[0].Entries = []string{"de", "FR"}
	if err := c.Validate(); err != nil {
		t.Errorf("real country codes were refused: %v", err)
	}

	c.Aliases[0].Entries = nil
	if err := c.Validate(); err == nil {
		t.Error("a country alias with no countries was accepted")
	}
}
