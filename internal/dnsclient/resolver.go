package dnsclient

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/netip"
	"slices"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// ErrNoResolver says there is nothing to ask.
var ErrNoResolver = errors.New("no resolver to ask")

// maxChain bounds a CNAME chase.
const maxChain = 10

// Resolver asks recursive resolvers about names. It has no fallback: with
// no servers, nothing is asked.
type Resolver struct {
	// Servers are asked in turn until one answers.
	Servers []netip.AddrPort
	// Port is where a zone's own nameservers are asked; zero is 53.
	Port uint16
}

// Nameserver is one of a zone's authoritative servers.
type Nameserver struct {
	Name  string
	Addrs []netip.AddrPort
}

// RCodeError is an answer that refused or failed.
type RCodeError struct {
	Server netip.AddrPort
	Name   string
	Type   dnsmessage.Type
	RCode  dnsmessage.RCode
}

func (e *RCodeError) Error() string {
	return fmt.Sprintf("%s answered %s for %s %s", e.Server, RCodeName(e.RCode), e.Name, TypeName(e.Type))
}

// Zone finds the zone holding name: the first name at or above it with a
// SOA of its own.
func (r Resolver) Zone(ctx context.Context, name string) (string, error) {
	for n := range parents(FQDN(name)) {
		resp, err := r.ask(ctx, n, dnsmessage.TypeSOA)
		if err != nil {
			return "", err
		}
		if resp.RCode == dnsmessage.RCodeSuccess && apex(resp, n) {
			return trim(n), nil
		}
	}
	return "", fmt.Errorf("no zone holds %s", trim(name))
}

// apex reports whether the answer holds n's own SOA. A CNAME in it means
// the SOA is another name's: a CNAME cannot sit at a zone's apex.
func apex(resp dnsmessage.Message, n string) bool {
	soa := false
	for _, rr := range resp.Answers {
		switch rr.Body.(type) {
		case *dnsmessage.CNAMEResource:
			return false
		case *dnsmessage.SOAResource:
			soa = soa || strings.EqualFold(rr.Header.Name.String(), n)
		}
	}
	return soa
}

// parents are name and each name above it, short of the root.
func parents(name string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for name != "" && name != "." {
			if !yield(name) {
				return
			}
			_, name, _ = strings.Cut(name, ".")
		}
	}
}

// Nameservers are a zone's authoritative servers with their addresses. A
// server whose name has no address is left out, as nobody can ask it.
func (r Resolver) Nameservers(ctx context.Context, zone string) ([]Nameserver, error) {
	resp, err := r.ask(ctx, zone, dnsmessage.TypeNS)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, rr := range resp.Answers {
		if ns, ok := rr.Body.(*dnsmessage.NSResource); ok && strings.EqualFold(rr.Header.Name.String(), FQDN(zone)) {
			names = append(names, strings.ToLower(trim(ns.NS.String())))
		}
	}
	slices.Sort(names)
	port := r.Port
	if port == 0 {
		port = 53
	}
	var out []Nameserver
	for _, name := range slices.Compact(names) {
		addrs, err := r.Addresses(ctx, name)
		if err != nil {
			return nil, err
		}
		if len(addrs) == 0 {
			continue
		}
		ns := Nameserver{Name: name}
		for _, a := range addrs {
			ns.Addrs = append(ns.Addrs, netip.AddrPortFrom(a, port))
		}
		out = append(out, ns)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("found no nameserver for %s", trim(zone))
	}
	return out, nil
}

// Addresses are name's IPv4 addresses, then its IPv6 ones.
func (r Resolver) Addresses(ctx context.Context, name string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, t := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		resp, err := r.ask(ctx, name, t)
		if err != nil {
			return nil, err
		}
		for _, rr := range resp.Answers {
			switch b := rr.Body.(type) {
			case *dnsmessage.AResource:
				out = append(out, netip.AddrFrom4(b.A))
			case *dnsmessage.AAAAResource:
				out = append(out, netip.AddrFrom16(b.AAAA))
			}
		}
	}
	return out, nil
}

// Follow walks the CNAME chain from name and returns the name it ends at:
// name itself when there is no CNAME there.
func (r Resolver) Follow(ctx context.Context, name string) (string, error) {
	cur := FQDN(name)
	seen := map[string]bool{strings.ToLower(cur): true}
	for {
		resp, err := r.ask(ctx, cur, dnsmessage.TypeCNAME)
		if err != nil {
			return "", err
		}
		next := cur
		// A resolver may send more of the chain in one answer.
		for target, ok := cname(resp, next); ok; target, ok = cname(resp, next) {
			if seen[strings.ToLower(target)] {
				return "", fmt.Errorf("the CNAMEs from %s go round in a loop", trim(name))
			}
			if len(seen) > maxChain {
				return "", fmt.Errorf("the CNAMEs from %s run past %d names", trim(name), maxChain)
			}
			seen[strings.ToLower(target)] = true
			next = target
		}
		if next == cur {
			return trim(cur), nil
		}
		cur = next
	}
}

// cname is the target of the CNAME at name in the answer.
func cname(resp dnsmessage.Message, name string) (string, bool) {
	for _, rr := range resp.Answers {
		if c, ok := rr.Body.(*dnsmessage.CNAMEResource); ok && strings.EqualFold(rr.Header.Name.String(), name) {
			return c.CNAME.String(), true
		}
	}
	return "", false
}

// TXT asks server for the TXT values at name, each record's strings
// joined. A zone's own server is asked without recursion.
func TXT(ctx context.Context, server netip.AddrPort, name string, recurse bool) ([]string, error) {
	q, err := Query(name, dnsmessage.TypeTXT, recurse)
	if err != nil {
		return nil, err
	}
	resp, err := Exchange(ctx, server, q)
	if err != nil {
		return nil, err
	}
	if resp.RCode != dnsmessage.RCodeSuccess {
		return nil, &RCodeError{Server: server, Name: trim(name), Type: dnsmessage.TypeTXT, RCode: resp.RCode}
	}
	var out []string
	for _, rr := range resp.Answers {
		if txt, ok := rr.Body.(*dnsmessage.TXTResource); ok {
			out = append(out, strings.Join(txt.TXT, ""))
		}
	}
	return out, nil
}

// ask puts one question to the resolvers in turn. The first to answer
// NOERROR or NXDOMAIN decides; one that fails or refuses passes the
// question on.
func (r Resolver) ask(ctx context.Context, name string, t dnsmessage.Type) (dnsmessage.Message, error) {
	if len(r.Servers) == 0 {
		return dnsmessage.Message{}, ErrNoResolver
	}
	var last error
	for _, s := range r.Servers {
		q, err := Query(name, t, true)
		if err != nil {
			return dnsmessage.Message{}, err
		}
		resp, err := Exchange(ctx, s, q)
		switch {
		case ctx.Err() != nil:
			return dnsmessage.Message{}, ctx.Err()
		case err != nil:
			last = err
		case resp.RCode == dnsmessage.RCodeSuccess, resp.RCode == dnsmessage.RCodeNameError:
			return resp, nil
		default:
			last = &RCodeError{Server: s, Name: trim(FQDN(name)), Type: t, RCode: resp.RCode}
		}
	}
	return dnsmessage.Message{}, last
}
