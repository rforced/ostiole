package dnsprovider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rforced/ostiole/internal/dnsclient"
	"github.com/rforced/ostiole/internal/dnsclient/tsig"
	"github.com/rforced/ostiole/internal/model"
)

// rfc2136TTL is what a challenge record is written with.
const rfc2136TTL = 120

// opUpdate is the opcode of an RFC 2136 update.
const opUpdate = 5

// classNone deletes the one record that follows it (RFC 2136 §2.5.4).
const classNone = 254

// rfc2136 writes records by DNS UPDATE to the zone's primary, signed with
// a TSIG key.
type rfc2136 struct {
	// server is the nameserver as the operator wrote it: an address or a
	// name, with a port or without.
	server    string
	key       tsig.Key
	resolvers []netip.AddrPort
}

func newRFC2136(p model.DNSProvider, o Options) (Client, error) {
	server := strings.TrimSpace(p.Settings["nameserver"])
	if server == "" {
		return nil, errors.New("the RFC 2136 provider names no nameserver")
	}
	key, err := tsig.NewKey(p.Settings["tsigKey"], p.Settings["tsigAlgorithm"], p.Settings["tsigSecret"])
	if err != nil {
		return nil, err
	}
	return &rfc2136{server: server, key: key, resolvers: o.Resolvers}, nil
}

func (c *rfc2136) AddTXT(ctx context.Context, r Record) error {
	return c.update(ctx, r, dnsmessage.ClassINET, rfc2136TTL)
}

func (c *rfc2136) RemoveTXT(ctx context.Context, r Record) error {
	return c.update(ctx, r, classNone, 0)
}

// update adds the record, or with class NONE deletes it, leaving any other
// value at the name as it was.
func (c *rfc2136) update(ctx context.Context, r Record, class dnsmessage.Class, ttl uint32) error {
	server, err := c.address(ctx)
	if err != nil {
		return err
	}
	zone, err := dnsmessage.NewName(dnsclient.FQDN(r.Zone))
	if err != nil {
		return fmt.Errorf("%q is not a zone", r.Zone)
	}
	name, err := dnsmessage.NewName(dnsclient.FQDN(r.Name))
	if err != nil {
		return fmt.Errorf("%q is not a DNS name", r.Name)
	}
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: dnsclient.NewID(), OpCode: opUpdate},
		Questions: []dnsmessage.Question{{Name: zone, Type: dnsmessage.TypeSOA, Class: dnsmessage.ClassINET}},
		Authorities: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeTXT, Class: class, TTL: ttl},
			Body:   &dnsmessage.TXTResource{TXT: []string{r.Value}},
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return err
	}
	signed, mac, err := c.key.Sign(packed, time.Now())
	if err != nil {
		return err
	}
	resp, err := dnsclient.ExchangeWire(ctx, server, signed)
	if err != nil {
		return err
	}
	return c.check(server, r.Zone, resp, mac)
}

// check reads the answer to an update: a refusal of the key, a refusal of
// the update, or success, which only a signed answer can say.
func (c *rfc2136) check(server netip.AddrPort, zone string, resp, mac []byte) error {
	rcode := dnsmessage.RCode(resp[3] & 0x0f)
	signed, err := c.key.Verify(resp, mac, time.Now())
	key := strings.TrimSuffix(c.key.Name(), ".")
	switch {
	case err == nil && signed.Error == tsig.BadKey:
		return &Error{Message: fmt.Sprintf("%s does not know the TSIG key %s", server, key)}
	case err == nil && signed.Error == tsig.BadSig:
		return &Error{Message: fmt.Sprintf("%s did not accept the TSIG secret for %s", server, key)}
	case err == nil && signed.Error == tsig.BadTime:
		return &Error{Message: fmt.Sprintf("%s says this router's clock is more than five minutes out", server)}
	case errors.Is(err, tsig.ErrUnsigned) && rcode != dnsmessage.RCodeSuccess, err == nil && rcode != dnsmessage.RCodeSuccess:
		return &Error{Message: fmt.Sprintf("%s answered %s to the update of %s", server, dnsclient.RCodeName(rcode), zone)}
	case err != nil:
		return fmt.Errorf("the answer from %s: %w", server, err)
	}
	return nil
}

// address is where the nameserver is: its address, or its name looked up
// through the router's resolvers, at port 53 unless it says.
func (c *rfc2136) address(ctx context.Context) (netip.AddrPort, error) {
	if a, err := netip.ParseAddrPort(c.server); err == nil {
		return a, nil
	}
	if a, err := netip.ParseAddr(c.server); err == nil {
		return netip.AddrPortFrom(a, 53), nil
	}
	host, port := c.server, "53"
	if h, p, err := net.SplitHostPort(c.server); err == nil {
		host, port = h, p
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("the nameserver %q has no port", c.server)
	}
	addrs, err := (dnsclient.Resolver{Servers: c.resolvers}).Addresses(ctx, host)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("looking up the nameserver %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return netip.AddrPort{}, fmt.Errorf("the nameserver %s has no address", host)
	}
	return netip.AddrPortFrom(addrs[0], uint16(n)), nil
}
