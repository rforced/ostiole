package dnsblock

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

// testCache writes two overlapping lists, the way two real subscriptions
// overlap.
func testCache(t *testing.T) *Cache {
	t.Helper()
	c := NewCache(t.TempDir())
	if err := c.Save(Meta{Name: "ads"}, []string{
		"ads.example.com",
		"deep.ads.example.com", // a parent already covers this
		"tracker.example.net",
		"counter.example.net",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(Meta{Name: "malware"}, []string{
		"bad.example.org",
		"ads.example.com", // both lists have it
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func testConfig() *model.Config {
	cfg := &model.Config{}
	cfg.System.Hostname = "gateway"
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Domain = "lan"
	cfg.Blocking = model.Blocking{
		Enabled: true,
		Lists: []model.BlockList{
			{Name: "ads", Enabled: true},
			{Name: "malware", Enabled: true},
		},
	}
	return cfg
}

func render(t *testing.T, cfg *model.Config, c *Cache) (string, Result) {
	t.Helper()
	o := OptionsFor(cfg)
	var buf bytes.Buffer
	res, err := Render(&buf, o, c)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String(), res
}

func TestRenderMergesAndDeduplicates(t *testing.T) {
	out, res := render(t, testConfig(), testCache(t))
	want := []string{
		"local=/ads.example.com/",
		"local=/counter.example.net/",
		"local=/tracker.example.net/",
		"local=/bad.example.org/",
	}
	for _, w := range want {
		if !strings.Contains(out, w+"\n") {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, "deep.ads.example.com") {
		t.Errorf("a name its parent already blocks was written out:\n%s", out)
	}
	if n := strings.Count(out, "local=/ads.example.com/"); n != 1 {
		t.Errorf("ads.example.com written %d times, want once", n)
	}
	if res.Domains != 4 {
		t.Errorf("Domains = %d; want 4", res.Domains)
	}
	if !strings.HasPrefix(out, Header) {
		t.Errorf("no header:\n%s", out)
	}
}

func TestRenderOrderIsStable(t *testing.T) {
	cfg, c := testConfig(), testCache(t)
	first, _ := render(t, cfg, c)
	second, _ := render(t, cfg, c)
	if first != second {
		t.Error("two renders of the same configuration differ")
	}
}

func TestRenderAllowCarvesOutAndFilters(t *testing.T) {
	cfg := testConfig()
	cfg.Blocking.Allow = []string{"tracker.example.net", "ads.example.com"}
	out, res := render(t, cfg, testCache(t))
	if !strings.Contains(out, "server=/tracker.example.net/#\n") {
		t.Errorf("no carve-out for an allowed name:\n%s", out)
	}
	if strings.Contains(out, "local=/tracker.example.net/") {
		t.Errorf("an allowed name was blocked anyway:\n%s", out)
	}
	if strings.Contains(out, "local=/ads.example.com/") {
		t.Errorf("an allowed name was blocked anyway:\n%s", out)
	}
	if res.Allowed != 2 {
		t.Errorf("Allowed = %d; want 2", res.Allowed)
	}
}

// A name under an allowed parent must not be written out: dnsmasq prefers
// the most specific match, so blocking the child would beat allowing the
// parent and the allowlist would quietly not work.
func TestRenderDropsChildrenOfAllowedNames(t *testing.T) {
	cfg := testConfig()
	cfg.Blocking.Allow = []string{"example.com"}
	out, _ := render(t, cfg, testCache(t))
	if strings.Contains(out, "local=/ads.example.com/") {
		t.Errorf("a child of an allowed name was blocked:\n%s", out)
	}
	if !strings.Contains(out, "server=/example.com/#") {
		t.Errorf("no carve-out for the allowed parent:\n%s", out)
	}
}

func TestRenderNeverBlocksThisBoxsOwnNames(t *testing.T) {
	cfg := testConfig()
	cfg.Services.DNS.HostOverrides = []model.HostOverride{{Hostname: "printer.lan", IP: "192.168.1.5"}}
	c := NewCache(t.TempDir())
	if err := c.Save(Meta{Name: "ads"}, []string{"lan", "printer.lan", "gateway", "ads.example.com"}); err != nil {
		t.Fatal(err)
	}
	cfg.Blocking.Lists = []model.BlockList{{Name: "ads", Enabled: true}}
	out, _ := render(t, cfg, c)
	for _, name := range []string{"local=/lan/", "local=/printer.lan/", "local=/gateway/"} {
		if strings.Contains(out, name) {
			t.Errorf("a name this router answers for was blocked (%s):\n%s", name, out)
		}
	}
	// ...and no carve-out either: the local domain must stay local.
	if strings.Contains(out, "server=/lan/#") {
		t.Errorf("the local domain was carved out to upstream:\n%s", out)
	}
	if !strings.Contains(out, "local=/ads.example.com/") {
		t.Errorf("an ordinary name stopped being blocked:\n%s", out)
	}
}

// A domain handed to its own resolver is left alone: blocking a name inside
// it would half-break a delegation the operator asked for, and a carve-out
// would fight the server= line dnsmasq gets for the same domain.
func TestRenderNeverBlocksADelegatedDomain(t *testing.T) {
	cfg := testConfig()
	cfg.Services.DNS.DomainOverrides = []model.DomainOverride{{Domain: "ts.net", Servers: []string{"100.100.100.100"}}}
	c := NewCache(t.TempDir())
	if err := c.Save(Meta{Name: "ads"}, []string{"ts.net", "log.ts.net", "ads.example.com"}); err != nil {
		t.Fatal(err)
	}
	cfg.Blocking.Lists = []model.BlockList{{Name: "ads", Enabled: true}}
	out, _ := render(t, cfg, c)
	for _, name := range []string{"local=/ts.net/", "local=/log.ts.net/"} {
		if strings.Contains(out, name) {
			t.Errorf("a delegated name was blocked (%s):\n%s", name, out)
		}
	}
	if strings.Contains(out, "server=/ts.net/#") {
		t.Errorf("a delegated domain was carved out to upstream:\n%s", out)
	}
	if !strings.Contains(out, "local=/ads.example.com/") {
		t.Errorf("an ordinary name stopped being blocked:\n%s", out)
	}
}

func TestRenderDenyAndCanary(t *testing.T) {
	cfg := testConfig()
	cfg.Blocking.Deny = []string{"typed.example.com"}
	cfg.Blocking.Enforce.FirefoxCanary = true
	out, _ := render(t, cfg, testCache(t))
	if !strings.Contains(out, "local=/typed.example.com/") {
		t.Errorf("a name from the deny list was not blocked:\n%s", out)
	}
	if !strings.Contains(out, "local=/"+FirefoxCanary+"/") {
		t.Errorf("the Firefox canary was not answered:\n%s", out)
	}
}

func TestRenderNullMode(t *testing.T) {
	cfg := testConfig()
	cfg.Blocking.Mode = model.BlockNull
	out, _ := render(t, cfg, testCache(t))
	if !strings.Contains(out, "address=/ads.example.com/0.0.0.0\n") {
		t.Errorf("no IPv4 null answer:\n%s", out)
	}
	if !strings.Contains(out, "address=/ads.example.com/::\n") {
		t.Errorf("no IPv6 null answer, so AAAA would be forwarded and the block leak:\n%s", out)
	}
}

func TestRenderRefusesMoreThanTheCeiling(t *testing.T) {
	var buf bytes.Buffer
	o := OptionsFor(testConfig())
	o.Max = 2
	if _, err := Render(&buf, o, testCache(t)); err == nil {
		t.Fatal("a list over the ceiling was accepted")
	}
}

// The deny list and the canary are the operator's own words, so they are
// written whether or not the subscribed lists are on.
func TestRenderListsOffKeepsDenyAndCanary(t *testing.T) {
	cfg := testConfig()
	cfg.Blocking.Enabled = false
	cfg.Blocking.Deny = []string{"typed.example.com"}
	cfg.Blocking.Enforce.FirefoxCanary = true
	out, res := render(t, cfg, testCache(t))
	if strings.Contains(out, "ads.example.com") {
		t.Errorf("the lists are off but a list name was written:\n%s", out)
	}
	if !strings.Contains(out, "local=/typed.example.com/") {
		t.Errorf("the lists are off and the deny list went with them:\n%s", out)
	}
	if !strings.Contains(out, "local=/"+FirefoxCanary+"/") {
		t.Errorf("the lists are off and the canary went with them:\n%s", out)
	}
	if res.Domains != 2 {
		t.Errorf("Domains = %d; want 2", res.Domains)
	}
}

// With the DNS server off nothing reads the file, so it says nothing.
func TestRenderDNSOffWritesNothing(t *testing.T) {
	cfg := testConfig()
	cfg.Services.DNS.Enabled = false
	cfg.Blocking.Deny = []string{"typed.example.com"}
	cfg.Blocking.Enforce.FirefoxCanary = true
	out, res := render(t, cfg, testCache(t))
	if res.Domains != 0 {
		t.Errorf("Domains = %d; want 0", res.Domains)
	}
	if strings.TrimSpace(strings.TrimPrefix(out, Header)) != "" {
		t.Errorf("wrote entries with the DNS server off:\n%s", out)
	}
}

func TestRenderWithNothingEnabled(t *testing.T) {
	cfg := testConfig()
	for i := range cfg.Blocking.Lists {
		cfg.Blocking.Lists[i].Enabled = false
	}
	out, res := render(t, cfg, testCache(t))
	if res.Domains != 0 {
		t.Errorf("Domains = %d; want 0", res.Domains)
	}
	if strings.TrimSpace(strings.TrimPrefix(out, Header)) != "" {
		t.Errorf("wrote entries for lists that are off:\n%s", out)
	}
}
