package dnsclient

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rforced/ostiole/internal/dnsclient/dnstest"
)

func resolver(servers ...*dnstest.Server) Resolver {
	var r Resolver
	for _, s := range servers {
		r.Servers = append(r.Servers, s.Addr)
	}
	return r
}

// The zone is the first name walking up with a SOA of its own: past the
// record's own name, which exists, and past an empty name between.
func TestZoneWalksUpToTheSOA(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(dnstest.SOA("example.test"), dnstest.TXT("_acme-challenge.www.example.test", "v"))

	zone, err := resolver(srv).Zone(context.Background(), "_acme-challenge.www.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if zone != "example.test" {
		t.Errorf("zone = %q", zone)
	}
	want := []string{
		"udp _acme-challenge.www.example.test. SOA",
		"udp www.example.test. SOA",
		"udp example.test. SOA",
	}
	if got := srv.Asked(); !slices.Equal(got, want) {
		t.Errorf("asked %q, want %q", got, want)
	}
}

// A CNAME's answer carries its target's SOA, which is not the zone of the
// name asked about.
func TestZoneLooksPastACNAME(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(
		dnstest.SOA("example.test"),
		dnstest.CNAME("www.example.test", "other.test"),
		dnstest.SOA("other.test"),
	)
	zone, err := resolver(srv).Zone(context.Background(), "www.example.test.")
	if err != nil {
		t.Fatal(err)
	}
	if zone != "example.test" {
		t.Errorf("zone = %q, want the CNAME's own zone", zone)
	}
}

// A resolver that refuses passes the question to the next; when every
// one fails, the last failure says why.
func TestResolversAreAskedInTurn(t *testing.T) {
	t.Parallel()
	refusing, answering := dnstest.New(t), dnstest.New(t)
	refusing.SetRCode("", dnsmessage.RCodeRefused)
	answering.Add(dnstest.SOA("example.test"))

	zone, err := resolver(refusing, answering).Zone(context.Background(), "a.example.test")
	if err != nil || zone != "example.test" {
		t.Errorf("Zone = %q, %v", zone, err)
	}

	answering.SetRCode("", dnsmessage.RCodeServerFailure)
	_, err = resolver(refusing, answering).Zone(context.Background(), "a.example.test")
	var rc *RCodeError
	if !errors.As(err, &rc) || rc.RCode != dnsmessage.RCodeServerFailure {
		t.Fatalf("err = %v, want the second resolver's SERVFAIL", err)
	}
	if want := answering.Addr.String() + " answered SERVFAIL for a.example.test SOA"; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
}

// Without a resolver nothing is asked, and nobody else is asked instead.
func TestNoResolverAsksNobody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := (Resolver{}).Zone(ctx, "example.test"); !errors.Is(err, ErrNoResolver) {
		t.Errorf("Zone = %v", err)
	}
	if _, err := (Resolver{}).Follow(ctx, "example.test"); !errors.Is(err, ErrNoResolver) {
		t.Errorf("Follow = %v", err)
	}
	if _, err := (Resolver{}).Nameservers(ctx, "example.test"); !errors.Is(err, ErrNoResolver) {
		t.Errorf("Nameservers = %v", err)
	}
}

// A name with no SOA anywhere above it has no zone.
func TestZoneOfANameNobodyHolds(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	_, err := resolver(srv).Zone(context.Background(), "a.example.test")
	if err == nil || err.Error() != "no zone holds a.example.test" {
		t.Errorf("err = %v", err)
	}
}

// The challenge record is written where its CNAMEs end.
func TestFollowWalksTheChain(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(
		dnstest.CNAME("_acme-challenge.example.test", "a.acme.other.test"),
		dnstest.CNAME("a.acme.other.test", "b.acme.third.test"),
	)
	r := resolver(srv)
	got, err := r.Follow(context.Background(), "_acme-challenge.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got != "b.acme.third.test" {
		t.Errorf("Follow = %q", got)
	}
	// A name with no CNAME is its own end, whether or not it exists.
	if got, err := r.Follow(context.Background(), "_acme-challenge.plain.test."); err != nil || got != "_acme-challenge.plain.test" {
		t.Errorf("Follow = %q, %v", got, err)
	}
}

func TestFollowStopsAtALoop(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(dnstest.CNAME("a.example.test", "b.example.test"), dnstest.CNAME("b.example.test", "A.example.test"))
	_, err := resolver(srv).Follow(context.Background(), "a.example.test")
	if err == nil || !strings.Contains(err.Error(), "loop") {
		t.Errorf("err = %v, want a loop", err)
	}
}

func TestFollowStopsAtALongChain(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	for i := range maxChain + 1 {
		srv.Add(dnstest.CNAME(string(rune('a'+i))+".example.test", string(rune('a'+i+1))+".example.test"))
	}
	_, err := resolver(srv).Follow(context.Background(), "a.example.test")
	if err == nil || !strings.Contains(err.Error(), "run past 10") {
		t.Errorf("err = %v, want the chain refused", err)
	}
	// Ten steps are allowed.
	if got, err := resolver(srv).Follow(context.Background(), "b.example.test"); err != nil || got != "l.example.test" {
		t.Errorf("Follow = %q, %v", got, err)
	}
}

// Each nameserver comes with its addresses, IPv4 first, at the port the
// resolver says. One with no address is left out.
func TestNameserversComeWithTheirAddresses(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(
		dnstest.NS("example.test", "NS2.example.test"),
		dnstest.NS("example.test", "ns1.example.test"),
		dnstest.NS("example.test", "lame.example.test"),
		dnstest.Addr("ns1.example.test", netip.MustParseAddr("2001:db8::1")),
		dnstest.Addr("ns1.example.test", netip.MustParseAddr("192.0.2.1")),
		dnstest.Addr("ns2.example.test", netip.MustParseAddr("192.0.2.2")),
	)
	r := resolver(srv)
	r.Port = 5353
	got, err := r.Nameservers(context.Background(), "example.test")
	if err != nil {
		t.Fatal(err)
	}
	want := []Nameserver{
		{Name: "ns1.example.test", Addrs: []netip.AddrPort{
			netip.MustParseAddrPort("192.0.2.1:5353"), netip.MustParseAddrPort("[2001:db8::1]:5353"),
		}},
		{Name: "ns2.example.test", Addrs: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.2:5353")}},
	}
	if !slices.EqualFunc(got, want, func(a, b Nameserver) bool { return a.Name == b.Name && slices.Equal(a.Addrs, b.Addrs) }) {
		t.Errorf("Nameservers = %+v, want %+v", got, want)
	}
	if _, err := r.Nameservers(context.Background(), "other.test"); err == nil || err.Error() != "found no nameserver for other.test" {
		t.Errorf("Nameservers of an unknown zone = %v", err)
	}
}

// A TXT question to a zone's own server goes without recursion, and a
// refusal says who refused what.
func TestTXTAsksOneServer(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(dnstest.TXT("_acme-challenge.example.test", "one"), dnstest.TXT("_acme-challenge.example.test", "two"))
	got, err := TXT(context.Background(), srv.Addr, "_acme-challenge.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("TXT = %q", got)
	}
	_, err = TXT(context.Background(), srv.Addr, "missing.example.test", false)
	var rc *RCodeError
	if !errors.As(err, &rc) || rc.RCode != dnsmessage.RCodeNameError {
		t.Errorf("err = %v, want NXDOMAIN", err)
	}
}
