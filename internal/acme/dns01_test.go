package acme

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/dnsclient"
	"github.com/rforced/ostiole/internal/dnsclient/dnstest"
	"github.com/rforced/ostiole/internal/dnsprovider"
	"github.com/rforced/ostiole/internal/model"
)

// RFC 8555 §8.4: the base64url SHA-256 of the key authorization, without
// padding.
func TestChallengeValue(t *testing.T) {
	t.Parallel()
	// Worked out with openssl, from RFC 8555 §8.1's token and a
	// thumbprint: printf %s "$keyAuth" | openssl dgst -sha256 -binary |
	// base64 | tr +/ -_ | tr -d =
	got := challengeValue("evaGxfADs6pSRb2LAv9IZf17Dt3juxGJ-PCt92wr-oA.9jg46WB3rR_AHD-EBXdN7cBkH1WOu0tA3M9fm21mqTI")
	if want := "lCM7cZyQXcVHK2nnW3jjAhNT3Fvm18UN-kWZZknKoYM"; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
}

// The record goes where the CNAMEs from _acme-challenge end, in the zone
// the provider holds for that name, or else the zone the resolvers find.
func TestChallengeRecordFollowsTheCNAME(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(
		dnstest.CNAME("_acme-challenge.www.example.test", "www.acme.delegated.test"),
		dnstest.SOA("delegated.test"),
		dnstest.SOA("example.test"),
	)
	r := dnsclient.Resolver{Servers: []netip.AddrPort{srv.Addr}}
	p := model.DNSProvider{ID: "p", Domains: []string{"example.test"}}

	got, err := challengeRecord(context.Background(), r, p, "www.example.test", "tok", "tok.thumb")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "www.acme.delegated.test" || got.Zone != "delegated.test" {
		t.Errorf("record at %s in %s, want www.acme.delegated.test in delegated.test", got.Name, got.Zone)
	}
	if got.Value != challengeValue("tok.thumb") || got.Domain != "www.example.test" || got.Token != "tok" || got.KeyAuth != "tok.thumb" {
		t.Errorf("record = %+v", got)
	}

	// Without a CNAME the provider's own domain is the zone, asked of
	// nobody.
	before := len(srv.Asked())
	got, err = challengeRecord(context.Background(), r, p, "plain.example.test", "tok", "tok.thumb")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "_acme-challenge.plain.example.test" || got.Zone != "example.test" {
		t.Errorf("record at %s in %s", got.Name, got.Zone)
	}
	if asked := srv.Asked()[before:]; !slices.Equal(asked, []string{"udp _acme-challenge.plain.example.test. CNAME"}) {
		t.Errorf("asked %q, want the CNAME question alone", asked)
	}
}

// The longest of the provider's domains holding the name is its zone.
func TestZoneOfPrefersTheLongestDomain(t *testing.T) {
	t.Parallel()
	p := model.DNSProvider{Domains: []string{"example.test", "Sub.Example.test.", "ample.test"}}
	zone, err := zoneOf(context.Background(), dnsclient.Resolver{}, p, "_acme-challenge.a.sub.example.test")
	if err != nil || zone != "sub.example.test" {
		t.Errorf("zone = %q, %v", zone, err)
	}
	// A name no domain holds is looked up, and with no resolver that
	// fails rather than asking anyone else.
	if _, err := zoneOf(context.Background(), dnsclient.Resolver{}, p, "_acme-challenge.other.test"); err == nil {
		t.Error("a zone was found with nothing to ask")
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A resolver may carry a port; resolv.conf's never does.
func TestResolversCarryTheirPorts(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{System: model.System{DNSServers: []string{"192.0.2.1", "192.0.2.2:5353", "2001:db8::1", "[2001:db8::2]:5353", "junk"}}}
	c := &Client{Config: func() *model.Config { return cfg }}
	var got []string
	for _, a := range c.resolvers() {
		got = append(got, a.String())
	}
	want := []string{"192.0.2.1:53", "192.0.2.2:5353", "[2001:db8::1]:53", "[2001:db8::2]:5353"}
	if !slices.Equal(got, want) {
		t.Errorf("resolvers = %q, want %q", got, want)
	}

	c = &Client{Config: func() *model.Config { return &model.Config{} }, resolvConfPath: writeFile(t, "search lan\nnameserver 127.0.0.53\nnameserver fe80::1%eth0\n")}
	got = nil
	for _, a := range c.resolvers() {
		got = append(got, a.String())
	}
	if want := []string{"127.0.0.53:53", "[fe80::1%eth0]:53"}; !slices.Equal(got, want) {
		t.Errorf("resolv.conf = %q, want %q", got, want)
	}
}

// zone is example.test served by two nameservers at one port, on
// 127.0.0.2 and 127.0.0.3, and a resolver that knows where they are.
func zone(t *testing.T) (ns1, ns2, resolver *dnstest.Server, r dnsclient.Resolver) {
	t.Helper()
	ns1 = dnstest.NewAt(t, netip.MustParseAddrPort("127.0.0.2:0"))
	ns2 = dnstest.NewAt(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.3"), ns1.Addr.Port()))
	resolver = dnstest.New(t)
	resolver.Add(
		dnstest.SOA("example.test"),
		dnstest.NS("example.test", "ns1.example.test"),
		dnstest.NS("example.test", "ns2.example.test"),
		dnstest.Addr("ns1.example.test", ns1.Addr.Addr()),
		dnstest.Addr("ns2.example.test", ns2.Addr.Addr()),
	)
	return ns1, ns2, resolver, dnsclient.Resolver{Servers: []netip.AddrPort{resolver.Addr}, Port: ns1.Addr.Port()}
}

func precheck(r dnsclient.Resolver) (*dns01, *pending) {
	d := &dns01{spec: dnsprovider.Kind{Poll: 10 * time.Millisecond}, resolver: r, wait: 300 * time.Millisecond}
	p := &pending{state: dnsprovider.Record{Zone: "example.test", Name: "_acme-challenge.example.test", Value: "v"}}
	return d, p
}

// The record has to be at every one of the zone's nameservers; one that
// lags is waited for.
func TestPrecheckWaitsForEveryNameserver(t *testing.T) {
	t.Parallel()
	ns1, ns2, _, r := zone(t)
	ns1.Add(dnstest.TXT("_acme-challenge.example.test", "v"))
	time.AfterFunc(50*time.Millisecond, func() { ns2.Add(dnstest.TXT("_acme-challenge.example.test", "v")) })
	d, p := precheck(r)
	if err := d.ready(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []*dnstest.Server{ns1, ns2} {
		if asked := ns.Asked(); len(asked) == 0 {
			t.Errorf("%s was not asked", ns.Addr)
		}
	}
}

// A nameserver that never has the record ends the wait with its name.
func TestPrecheckGivesUp(t *testing.T) {
	t.Parallel()
	ns1, _, _, r := zone(t)
	ns1.Add(dnstest.TXT("_acme-challenge.example.test", "v"))
	d, p := precheck(r)
	err := d.ready(context.Background(), p)
	want := "the record at _acme-challenge.example.test had not reached every nameserver after 300ms: ns2.example.test"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("err = %v, want %q…", err, want)
	}
}

// With the check relaxed, as for pebble, nobody is asked.
func TestPrecheckRelaxedAsksNobody(t *testing.T) {
	t.Parallel()
	_, _, resolver, r := zone(t)
	d, p := precheck(r)
	d.relax = true
	if err := d.ready(context.Background(), p); err != nil || len(resolver.Asked()) != 0 {
		t.Errorf("ready = %v after %v", err, resolver.Asked())
	}
}
