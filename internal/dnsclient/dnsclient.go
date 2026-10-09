// Package dnsclient puts questions to DNS servers over UDP and TCP. The
// dns-01 pre-check and RFC 2136 updates use it. Neither goes through the
// system's resolver: every question goes to the server the caller names.
package dnsclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// timeout bounds one exchange with one server.
const timeout = 10 * time.Second

// resend is how long a question over UDP waits for its answer before it
// goes again.
const resend = 2 * time.Second

// payload is the UDP size offered with EDNS(0): room for a few TXT
// records, and small enough to arrive unfragmented (DNS flag day 2020).
const payload = 1232

// headerLen is a DNS header's size on the wire.
const headerLen = 12

// Query is a question for name with a random ID and EDNS(0). recurse asks
// the server to resolve it; a zone's own server is asked without.
func Query(name string, t dnsmessage.Type, recurse bool) (dnsmessage.Message, error) {
	n, err := dnsmessage.NewName(FQDN(name))
	if err != nil {
		return dnsmessage.Message{}, fmt.Errorf("%q is not a DNS name", name)
	}
	var opt dnsmessage.ResourceHeader
	if err := opt.SetEDNS0(payload, dnsmessage.RCodeSuccess, false); err != nil {
		return dnsmessage.Message{}, err
	}
	return dnsmessage.Message{
		ID: NewID(), RecursionDesired: recurse,
		Questions:   []dnsmessage.Question{{Name: n, Type: t, Class: dnsmessage.ClassINET}},
		Additionals: []dnsmessage.Resource{{Header: opt, Body: &dnsmessage.OPTResource{}}},
	}, nil
}

// NewID is a message ID nobody off the path can guess.
func NewID() uint16 {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint16(b[:])
}

// Exchange sends m to server and returns the answer, asking again over
// TCP when the answer over UDP comes back truncated.
func Exchange(ctx context.Context, server netip.AddrPort, m dnsmessage.Message) (dnsmessage.Message, error) {
	query, err := m.Pack()
	if err != nil {
		return dnsmessage.Message{}, err
	}
	raw, err := ExchangeWire(ctx, server, query)
	if err != nil {
		return dnsmessage.Message{}, err
	}
	var resp dnsmessage.Message
	if err := resp.Unpack(raw); err != nil {
		return dnsmessage.Message{}, fmt.Errorf("%s sent an answer that does not parse: %w", server, err)
	}
	return resp, nil
}

// ExchangeWire is Exchange for a message already packed. TSIG needs the
// bytes: its signature covers what goes out and what comes back.
func ExchangeWire(ctx context.Context, server netip.AddrPort, query []byte) ([]byte, error) {
	if len(query) < headerLen || len(query) > 0xffff {
		return nil, errors.New("the query is not a DNS message")
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := exchangeUDP(bounded, server, query)
	if err == nil && resp[2]&0x02 != 0 {
		resp, err = exchangeTCP(bounded, server, query)
	}
	switch {
	case err == nil:
		return resp, nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case bounded.Err() != nil:
		return nil, fmt.Errorf("no answer from %s in %s", server, timeout)
	}
	return nil, fmt.Errorf("asking %s: %w", server, err)
}

func exchangeUDP(ctx context.Context, server netip.AddrPort, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", server.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Closing the socket is what ends a read when the context does.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	// ExchangeWire always sets one.
	deadline, _ := ctx.Deadline()
	buf := make([]byte, 0xffff)
	for {
		if _, err := conn.Write(query); err != nil {
			return nil, err
		}
		wait := time.Now().Add(resend)
		if deadline.Before(wait) {
			wait = deadline
		}
		if err := conn.SetReadDeadline(wait); err != nil {
			return nil, err
		}
		for {
			n, err := conn.Read(buf)
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			if err != nil {
				return nil, err
			}
			// Anything else arriving on the socket is not the answer.
			if answers(query, buf[:n]) {
				return bytes.Clone(buf[:n]), nil
			}
		}
		if !time.Now().Before(deadline) {
			// The context ends at this moment too; wait for it to say so.
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
}

func exchangeTCP(ctx context.Context, server netip.AddrPort, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", server.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	msg := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(query)), uint16(len(query))) //nolint:gosec // bounded in ExchangeWire
	if _, err := conn.Write(append(msg, query...)); err != nil {
		return nil, err
	}
	var n [2]byte
	if _, err := io.ReadFull(conn, n[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(n[:]))
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	if !answers(query, resp) {
		return nil, errors.New("the answer over TCP is not to the question asked")
	}
	return resp, nil
}

// answers reports whether resp answers query: a response with the same ID
// and the same question. An error answer may leave the question out.
func answers(query, resp []byte) bool {
	if len(resp) < headerLen || resp[0] != query[0] || resp[1] != query[1] || resp[2]&0x80 == 0 {
		return false
	}
	var qp, rp dnsmessage.Parser
	if _, err := qp.Start(query); err != nil {
		return false
	}
	if _, err := rp.Start(resp); err != nil {
		return false
	}
	want, err := qp.Question()
	if err != nil {
		return true
	}
	got, err := rp.Question()
	if errors.Is(err, dnsmessage.ErrSectionDone) {
		return true
	}
	return err == nil && got.Type == want.Type && got.Class == want.Class &&
		strings.EqualFold(got.Name.String(), want.Name.String())
}

// FQDN is name with the trailing dot a message carries.
func FQDN(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// trim is name without its trailing dot, as the rest of Ostiole writes it.
func trim(name string) string {
	if name == "." {
		return name
	}
	return strings.TrimSuffix(name, ".")
}

// rcodeNames are the names RFC 6895 gives the codes, which is what dig
// prints. BADSIG to BADTIME only ever appear in a TSIG record.
var rcodeNames = map[dnsmessage.RCode]string{
	0: "NOERROR", 1: "FORMERR", 2: "SERVFAIL", 3: "NXDOMAIN", 4: "NOTIMP",
	5: "REFUSED", 6: "YXDOMAIN", 7: "YXRRSET", 8: "NXRRSET", 9: "NOTAUTH",
	10: "NOTZONE", 16: "BADSIG", 17: "BADKEY", 18: "BADTIME",
}

// RCodeName names a response code.
func RCodeName(c dnsmessage.RCode) string {
	if name, ok := rcodeNames[c]; ok {
		return name
	}
	return fmt.Sprintf("RCODE%d", c)
}

// TypeName names a record type the way a zone file does.
func TypeName(t dnsmessage.Type) string {
	if name, ok := strings.CutPrefix(t.String(), "Type"); ok {
		return name
	}
	return fmt.Sprintf("TYPE%d", t)
}
