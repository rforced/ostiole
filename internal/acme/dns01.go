package acme

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"ostiole/internal/dnsclient"
	"ostiole/internal/dnsprovider"
	"ostiole/internal/model"
)

// challengeValue is the TXT value that answers a dns-01 challenge (RFC
// 8555 §8.4).
func challengeValue(keyAuth string) string {
	sum := sha256.Sum256([]byte(keyAuth))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// challengeRecord is the record that answers one dns-01 challenge for
// domain, a name without its wildcard. It goes where the CNAMEs from
// _acme-challenge end, which is how a zone hands its challenges to
// another.
func challengeRecord(ctx context.Context, r dnsclient.Resolver, p model.DNSProvider, domain, token, keyAuth string) (dnsprovider.Record, error) {
	name, err := r.Follow(ctx, "_acme-challenge."+domain)
	if err != nil {
		return dnsprovider.Record{}, fmt.Errorf("looking for a CNAME at _acme-challenge.%s: %w", domain, err)
	}
	zone, err := zoneOf(ctx, r, p, name)
	if err != nil {
		return dnsprovider.Record{}, fmt.Errorf("finding the zone of %s: %w", name, err)
	}
	return dnsprovider.Record{
		Zone: zone, Name: name, Value: challengeValue(keyAuth),
		Domain: domain, Token: token, KeyAuth: keyAuth,
	}, nil
}

// zoneOf is the zone a record goes in: the provider's own domain holding
// name (ADR-0030), or else the zone the resolvers find.
func zoneOf(ctx context.Context, r dnsclient.Resolver, p model.DNSProvider, name string) (string, error) {
	name = model.NormalizeDomain(name)
	best := ""
	for _, d := range p.Domains {
		d = model.NormalizeDomain(d)
		if d != "" && len(d) > len(best) && (name == d || strings.HasSuffix(name, "."+d)) {
			best = d
		}
	}
	if best != "" {
		return best, nil
	}
	zone, err := r.Zone(ctx, name)
	return strings.ToLower(zone), err
}

// waitOr is how long a provider's records get to reach every nameserver:
// what the provider says, or the kind's own.
func waitOr(p model.DNSProvider, fallback time.Duration) time.Duration {
	if p.PropagationSeconds > 0 {
		return time.Duration(p.PropagationSeconds) * time.Second
	}
	return fallback
}

// presentTimeout bounds writing or removing one challenge record: the
// CNAME chase, the zone, and the provider's own requests or program.
const presentTimeout = 5 * time.Minute

// dns01 answers dns-01 challenges through one DNS provider.
type dns01 struct {
	client   dnsprovider.Client
	spec     dnsprovider.Kind
	provider model.DNSProvider
	resolver dnsclient.Resolver
	// wait is how long a record gets to reach every nameserver.
	wait time.Duration
	// relax leaves out the check at the zone's own nameservers. Only the
	// test against pebble sets it: the challenge server it writes to is
	// not a zone and answers no SOA.
	relax bool
}

func (*dns01) kind() string           { return "dns-01" }
func (*dns01) together() bool         { return true }
func (d *dns01) apart() time.Duration { return d.spec.Sequential }

func (d *dns01) present(ctx context.Context, p *pending) error {
	ctx, cancel := context.WithTimeout(ctx, presentTimeout)
	defer cancel()
	r, err := challengeRecord(ctx, d.resolver, d.provider, p.authz.Identifier.Value, p.chal.Token, p.keyAuth)
	if err != nil {
		return err
	}
	if err := d.client.AddTXT(ctx, r); err != nil {
		return err
	}
	p.state = r
	return nil
}

func (d *dns01) remove(ctx context.Context, p *pending) error {
	r, ok := p.state.(dnsprovider.Record)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, presentTimeout)
	defer cancel()
	return d.client.RemoveTXT(ctx, r)
}

// ready waits until every nameserver of the record's zone answers with the
// value, asked at the kind's pace until the wait runs out. The zone and its
// servers are found through the router's resolvers.
func (d *dns01) ready(ctx context.Context, p *pending) error {
	r, ok := p.state.(dnsprovider.Record)
	if !ok || d.relax {
		return nil
	}
	give := time.Now().Add(d.wait)
	var servers []dnsclient.Nameserver
	var last error
	for {
		if err := sleep(ctx, d.spec.Poll); err != nil {
			return err
		}
		if servers == nil {
			servers, last = d.nameservers(ctx, r.Name)
		}
		if servers != nil {
			var done bool
			if done, last = seen(ctx, servers, r.Name, r.Value); done {
				return nil
			}
		}
		if time.Now().Add(d.spec.Poll).After(give) {
			return fmt.Errorf("the record at %s had not reached every nameserver after %s: %w", r.Name, d.wait, last)
		}
	}
}

// nameservers are the servers of the zone holding name.
func (d *dns01) nameservers(ctx context.Context, name string) ([]dnsclient.Nameserver, error) {
	zone, err := d.resolver.Zone(ctx, name)
	if err != nil {
		return nil, err
	}
	return d.resolver.Nameservers(ctx, zone)
}

// seen asks each nameserver for the value at name, without recursion, at
// the first of its addresses that answers.
func seen(ctx context.Context, servers []dnsclient.Nameserver, name, value string) (bool, error) {
	for _, ns := range servers {
		var values []string
		var err error
		for _, addr := range ns.Addrs {
			values, err = dnsclient.TXT(ctx, addr, name, false)
			var rc *dnsclient.RCodeError
			if err == nil || errors.As(err, &rc) {
				break
			}
		}
		if err != nil {
			return false, fmt.Errorf("%s: %w", ns.Name, err)
		}
		if !slices.Contains(values, value) {
			return false, fmt.Errorf("%s does not answer with the value yet", ns.Name)
		}
	}
	return true, nil
}
