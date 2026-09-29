// Package dnsprovider writes records at the DNS services Ostiole knows:
// TXT records that answer dns-01 challenges, and address records for the
// kinds that keep dynamic DNS.
package dnsprovider

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// Client writes challenge records in the zones one DNS provider holds.
type Client interface {
	// AddTXT puts the record's value among the TXT records at its name.
	AddTXT(ctx context.Context, r Record) error
	// RemoveTXT takes the record's value away and leaves the rest.
	RemoveTXT(ctx context.Context, r Record) error
}

// Record is one challenge record.
type Record struct {
	// Zone is the zone the record goes in, and Name the record's own
	// name, where the CNAMEs from _acme-challenge end. Neither has a
	// trailing dot.
	Zone, Name string
	// Value is the TXT value.
	Value string
	// Domain, Token and KeyAuth are the challenge itself, for a program
	// in RAW mode.
	Domain, Token, KeyAuth string
}

// Kind is one DNS service.
type Kind struct {
	// Build makes the client for one provider of this kind.
	Build func(p model.DNSProvider, o Options) (Client, error)
	// Wait is how long a challenge record gets to reach every one of the
	// zone's nameservers when the provider does not say, and Poll how
	// often they are asked.
	Wait, Poll time.Duration
	// Sequential answers one challenge at a time, this long apart; zero
	// answers them together.
	Sequential time.Duration
}

// Kinds are the kinds in model.ProviderKinds, with the waits each had
// when lego v4.35.2 wrote the records, so an order is as patient as it
// was. The TTL a record is written with is its client's.
var Kinds = map[string]Kind{
	"rfc2136":    {Build: newRFC2136, Wait: time.Minute, Poll: 2 * time.Second, Sequential: time.Minute},
	"cloudflare": {Build: newCloudflare, Wait: 2 * time.Minute, Poll: 2 * time.Second},
	"exec":       {Build: newProgram, Wait: time.Minute, Poll: 2 * time.Second, Sequential: time.Minute},
	"hetzner":    {Build: newHetzner, Wait: 2 * time.Minute, Poll: 2 * time.Second},
	"porkbun":    {Build: newPorkbun, Wait: 10 * time.Minute, Poll: 10 * time.Second},
	"route53":    {Build: newRoute53, Wait: 2 * time.Minute, Poll: 4 * time.Second},
}

// Build makes the client for one provider.
func Build(p model.DNSProvider, o Options) (Client, error) {
	k, ok := Kinds[p.Kind]
	if !ok || k.Build == nil {
		return nil, fmt.Errorf("this build cannot write to %s", p.Kind)
	}
	return k.Build(p, o)
}

// checkKinds fails the tests if the kinds offered and the kinds here ever
// disagree.
func checkKinds() error {
	for _, k := range model.ProviderKinds {
		if Kinds[k.Kind].Build == nil {
			return fmt.Errorf("%s is offered but has no client", k.Kind)
		}
	}
	for kind := range Kinds {
		if _, ok := model.ProviderKindOf(kind); !ok {
			return fmt.Errorf("%s has a client but is not offered", kind)
		}
	}
	return nil
}

// Options are what every client is built with.
type Options struct {
	// HTTP makes the requests; nil is a client with a 30-second timeout
	// that follows no redirect.
	HTTP *http.Client
	// UserAgent names the caller.
	UserAgent string
	// CloudflareAPI replaces Cloudflare's API root, for the e2e suite's
	// stand-in; empty is Cloudflare's own.
	CloudflareAPI string
	// Resolvers look up a nameserver given by name. With none, only an
	// address will do.
	Resolvers []netip.AddrPort
}

// RequestTimeout bounds one request to a provider.
const RequestTimeout = 30 * time.Second

func (o Options) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{
		Timeout: RequestTimeout,
		// A redirect is an answer to report, not one to follow with the
		// credentials attached.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Error is a refusal a provider sent back.
type Error struct {
	// Message is the sentence the page shows.
	Message string
	// RetryAfter is how long the provider asked the router to wait; zero
	// when it did not say.
	RetryAfter time.Duration
	// code is the provider's own number for the refusal, when it gave one.
	code int
}

func (e *Error) Error() string { return e.Message }

// RecordType is the kind of address record.
type RecordType string

// The address record types.
const (
	TypeA    RecordType = "A"
	TypeAAAA RecordType = "AAAA"
)

// TestResult is what a provider made of its credentials.
type TestResult struct {
	// Zones are the zones the credentials can see, as many as a page of
	// them holds.
	Zones []string `json:"zones"`
	// More is how many more there are than Zones lists.
	More int `json:"more,omitempty"`
	// Domains are the provider's domains, each with what went wrong
	// reading it.
	Domains []DomainTest `json:"domains"`
}

// DomainTest is one domain's part of a test.
type DomainTest struct {
	Domain string `json:"domain"`
	Error  string `json:"error,omitempty"`
}
