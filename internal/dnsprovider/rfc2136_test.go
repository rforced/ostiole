package dnsprovider

import (
	"context"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"ostiole/internal/dnsclient/dnstest"
	"ostiole/internal/dnsclient/tsig"
	"ostiole/internal/model"
)

const testSecret = "b3N0aW9sZS10c2lnLXRlc3Qtc2VjcmV0LTMyYnl0ZXM="

// primary is a zone's primary taking updates signed with test-key.
func primary(t *testing.T) *dnstest.Server {
	t.Helper()
	srv := dnstest.New(t)
	srv.Add(dnstest.SOA("example.test"))
	key, err := tsig.NewKey("test-key", "hmac-sha256", testSecret)
	if err != nil {
		t.Fatal(err)
	}
	srv.AcceptUpdates(key, nil)
	return srv
}

func rfc2136Client(t *testing.T, settings map[string]string, resolvers ...netip.AddrPort) Client {
	t.Helper()
	full := map[string]string{"tsigKey": "test-key", "tsigSecret": testSecret}
	maps.Copy(full, settings)
	c, err := Build(model.DNSProvider{ID: "ns", Kind: "rfc2136", Settings: full}, Options{Resolvers: resolvers})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// heldTXT is what srv holds at _acme-challenge.example.test, as "value ttl".
func heldTXT(srv *dnstest.Server) []string {
	var out []string
	for _, rr := range srv.Records("_acme-challenge.example.test", dnsmessage.TypeTXT) {
		out = append(out, strings.Join(rr.Body.(*dnsmessage.TXTResource).TXT, "")+" "+strconv.Itoa(int(rr.Header.TTL)))
	}
	slices.Sort(out)
	return out
}

func testRecord(value string) Record {
	return Record{Zone: "example.test", Name: "_acme-challenge.example.test", Value: value}
}

// A value is added beside what is there and deleted alone.
func TestRFC2136AddsAndDeletesOneRecord(t *testing.T) {
	t.Parallel()
	srv := primary(t)
	srv.Add(dnstest.TXT("_acme-challenge.example.test", "someone else's"))
	c := rfc2136Client(t, map[string]string{"nameserver": srv.Addr.String()})
	ctx := context.Background()
	for _, v := range []string{"one", "two"} {
		if err := c.AddTXT(ctx, testRecord(v)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := heldTXT(srv), []string{"one 120", "someone else's 60", "two 120"}; !slices.Equal(got, want) {
		t.Errorf("held %q, want %q", got, want)
	}
	if err := c.RemoveTXT(ctx, testRecord("one")); err != nil {
		t.Fatal(err)
	}
	if got, want := heldTXT(srv), []string{"someone else's 60", "two 120"}; !slices.Equal(got, want) {
		t.Errorf("held %q, want %q", got, want)
	}
}

// A nameserver given by name is looked up through the resolvers.
func TestRFC2136FindsTheNameserverByName(t *testing.T) {
	t.Parallel()
	srv := primary(t)
	resolver := dnstest.New(t)
	resolver.Add(dnstest.Addr("ns1.example.test", srv.Addr.Addr()))
	c := rfc2136Client(t, map[string]string{"nameserver": "ns1.example.test:" + strconv.Itoa(int(srv.Addr.Port()))}, resolver.Addr)
	if err := c.AddTXT(context.Background(), testRecord("v")); err != nil {
		t.Fatal(err)
	}
	if got := heldTXT(srv); !slices.Equal(got, []string{"v 120"}) {
		t.Errorf("held %q", got)
	}
	// Without a resolver a name cannot be used.
	err := rfc2136Client(t, map[string]string{"nameserver": "ns1.example.test"}).AddTXT(context.Background(), testRecord("v"))
	if err == nil || !strings.Contains(err.Error(), "no resolver to ask") {
		t.Errorf("err = %v", err)
	}
}

// Each refusal says what was refused.
func TestRFC2136SaysWhatWasRefused(t *testing.T) {
	t.Parallel()
	srv := primary(t)
	ns := srv.Addr.String()
	for _, tc := range []struct {
		name     string
		settings map[string]string
		record   Record
		want     string
	}{
		{"wrong secret", map[string]string{"tsigSecret": "d3Jvbmc="}, testRecord("v"), ns + " did not accept the TSIG secret for test-key"},
		{"unknown key", map[string]string{"tsigKey": "other-key"}, testRecord("v"), ns + " does not know the TSIG key other-key"},
		{"other zone", nil, Record{Zone: "other.test", Name: "_acme-challenge.other.test", Value: "v"}, ns + " answered NOTAUTH to the update of other.test"},
	} {
		settings := map[string]string{"nameserver": ns}
		maps.Copy(settings, tc.settings)
		err := rfc2136Client(t, settings).AddTXT(context.Background(), tc.record)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	if got := heldTXT(srv); got != nil {
		t.Errorf("held %q after refusals", got)
	}
}

func TestRFC2136SaysTheClockIsOut(t *testing.T) {
	t.Parallel()
	srv := dnstest.New(t)
	srv.Add(dnstest.SOA("example.test"))
	key, _ := tsig.NewKey("test-key", "hmac-sha256", testSecret)
	srv.AcceptUpdates(key, func() time.Time { return time.Now().Add(10 * time.Minute) })
	err := rfc2136Client(t, map[string]string{"nameserver": srv.Addr.String()}).AddTXT(context.Background(), testRecord("v"))
	if err == nil || !strings.HasSuffix(err.Error(), "says this router's clock is more than five minutes out") {
		t.Errorf("err = %v", err)
	}
}

// Only a signed answer says an update worked.
func TestRFC2136WantsASignedAnswer(t *testing.T) {
	t.Parallel()
	srv := primary(t)
	srv.SetRCode("example.test", dnsmessage.RCodeSuccess)
	err := rfc2136Client(t, map[string]string{"nameserver": srv.Addr.String()}).AddTXT(context.Background(), testRecord("v"))
	if err == nil || !strings.HasSuffix(err.Error(), "the message is not signed") {
		t.Errorf("err = %v", err)
	}
}
