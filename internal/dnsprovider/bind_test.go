//go:build acme

// Updates against BIND 9, the server most RFC 2136 setups run. It is
// behind a tag because it needs a container; scripts/ci/acme-test.sh
// starts it.
package dnsprovider

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rforced/ostiole/internal/dnsclient"
	"github.com/rforced/ostiole/internal/model"
)

func bindClient(t *testing.T, key, secret string) Client {
	t.Helper()
	c, err := Build(model.DNSProvider{ID: "bind", Kind: "rfc2136", Settings: map[string]string{
		"nameserver": os.Getenv("BIND_SERVER"), "tsigKey": key, "tsigSecret": secret,
	}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBINDTakesUpdates(t *testing.T) {
	server, secret := os.Getenv("BIND_SERVER"), os.Getenv("BIND_SECRET")
	if server == "" || secret == "" {
		t.Skip("BIND_SERVER and BIND_SECRET are unset; run scripts/ci/acme-test.sh")
	}
	addr := netip.MustParseAddrPort(server)
	ctx := context.Background()
	c := bindClient(t, "ostiole-test", secret)
	name := "_acme-challenge.www.example.test"
	held := func() []string {
		got, err := dnsclient.TXT(ctx, addr, name, false)
		if rc := (*dnsclient.RCodeError)(nil); errors.As(err, &rc) && rc.RCode == dnsmessage.RCodeNameError {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		return got
	}
	for _, v := range []string{"one", "two"} {
		if err := c.AddTXT(ctx, Record{Zone: "example.test", Name: name, Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	if got := held(); !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("BIND holds %q, want both values", got)
	}
	if err := c.RemoveTXT(ctx, Record{Zone: "example.test", Name: name, Value: "one"}); err != nil {
		t.Fatal(err)
	}
	if got := held(); !slices.Equal(got, []string{"two"}) {
		t.Errorf("BIND holds %q, want the other value alone", got)
	}
	if err := c.RemoveTXT(ctx, Record{Zone: "example.test", Name: name, Value: "two"}); err != nil {
		t.Fatal(err)
	}
	if got := held(); got != nil {
		t.Errorf("BIND holds %q after both went", got)
	}

	for _, tc := range []struct {
		name, key, secret, zone, want string
	}{
		{"wrong secret", "ostiole-test", "d3Jvbmctc2VjcmV0", "example.test", "did not accept the TSIG secret for ostiole-test"},
		{"unknown key", "nobody", secret, "example.test", "does not know the TSIG key nobody"},
		{"other zone", "ostiole-test", secret, "other.test", "answered NOTAUTH to the update of other.test"},
	} {
		err := bindClient(t, tc.key, tc.secret).AddTXT(ctx, Record{Zone: tc.zone, Name: "_acme-challenge." + tc.zone, Value: "v"})
		if err == nil || !strings.HasSuffix(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}
