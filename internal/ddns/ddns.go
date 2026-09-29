// Package ddns keeps dynamic DNS records at their providers pointed at
// this router's own addresses. The interfaces are read every few seconds,
// and a provider hears from the router only when something changed, once
// a day, or when someone asks.
package ddns

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/rforced/ostiole/internal/dnsprovider"
	"github.com/rforced/ostiole/internal/model"
)

// Types are the record types a record keeps, A first.
func Types(r model.DDNSRecord) []dnsprovider.RecordType {
	var out []dnsprovider.RecordType
	if r.IPv4 {
		out = append(out, dnsprovider.TypeA)
	}
	if r.IPv6 {
		out = append(out, dnsprovider.TypeAAAA)
	}
	return out
}

// Provider keeps address records in the zones one DNS provider holds.
// The clients in internal/dnsprovider for the kinds with DynamicDNS set
// in model.ProviderKinds are one.
type Provider interface {
	// Lookup returns the addresses the provider holds for name.
	Lookup(ctx context.Context, zone, name string, t dnsprovider.RecordType) ([]netip.Addr, error)
	// Set points the name's one record of type t at addr, creating it
	// when there is none, and returns what the provider held before.
	Set(ctx context.Context, zone, name string, t dnsprovider.RecordType, addr netip.Addr) ([]netip.Addr, error)
	// Test tries the credentials and reads each domain's records,
	// everything a record needs short of writing one.
	Test(ctx context.Context, domains []string) (*dnsprovider.TestResult, error)
}

// Build makes the client for one provider.
func Build(p model.DNSProvider, o dnsprovider.Options) (Provider, error) {
	if kind, ok := model.ProviderKindOf(p.Kind); !ok || !kind.DynamicDNS {
		return nil, fmt.Errorf("%s cannot keep a dynamic DNS record", p.Kind)
	}
	c, err := dnsprovider.Build(p, o)
	if err != nil {
		return nil, err
	}
	dp, ok := c.(Provider)
	if !ok {
		return nil, fmt.Errorf("%s cannot keep a dynamic DNS record", p.Kind)
	}
	return dp, nil
}
