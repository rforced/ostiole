package dnsprovider

import (
	"context"
	"net/netip"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

func TestEveryKindOfferedHasAClient(t *testing.T) {
	t.Parallel()
	if err := checkKinds(); err != nil {
		t.Fatal(err)
	}
}

// testSettings build each kind's client.
var testSettings = map[string]map[string]string{
	"rfc2136":    {"nameserver": "192.0.2.53", "tsigKey": "k", "tsigSecret": "c2VjcmV0"},
	"cloudflare": {"token": "t"},
	"exec":       {"program": "/bin/true"},
	"hetzner":    {"token": "t"},
	"porkbun":    {"apiKey": "k", "secretKey": "s"},
	"route53":    {"accessKeyId": "AKIDEXAMPLE", "secretAccessKey": "s"},
}

func txtAt(name, value string) Record {
	return Record{Zone: "example.com", Name: name, Value: value}
}

// addressKeeper is ddns.Provider, which this package cannot import.
type addressKeeper interface {
	Lookup(ctx context.Context, zone, name string, t RecordType) ([]netip.Addr, error)
	Set(ctx context.Context, zone, name string, t RecordType, addr netip.Addr) ([]netip.Addr, error)
	Test(ctx context.Context, domains []string) (*TestResult, error)
}

// The kinds offered for dynamic DNS are the ones whose clients keep
// address records.
func TestDynamicDNSKindsKeepAddresses(t *testing.T) {
	t.Parallel()
	for _, k := range model.ProviderKinds {
		c, err := Build(model.DNSProvider{ID: "p", Kind: k.Kind, Settings: testSettings[k.Kind]}, Options{})
		if err != nil {
			t.Errorf("%s: %v", k.Kind, err)
			continue
		}
		if _, ok := c.(addressKeeper); ok != k.DynamicDNS {
			t.Errorf("%s: DynamicDNS is %v, but the client keeps address records: %v", k.Kind, k.DynamicDNS, ok)
		}
	}
}
