// Package dnstest answers DNS questions for tests, from records held in
// memory, over UDP and TCP on one port.
package dnstest

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rforced/ostiole/internal/dnsclient/tsig"
)

// Server is one DNS server. It follows CNAMEs among its own records, as a
// resolver would, and answers NXDOMAIN for a name it holds nothing at or
// under.
type Server struct {
	// Addr is where it listens, UDP and TCP alike.
	Addr netip.AddrPort

	udp   net.PacketConn
	tcp   net.Listener
	wg    sync.WaitGroup
	conns sync.Map

	mu       sync.Mutex
	records  []dnsmessage.Resource
	rcodes   map[string]dnsmessage.RCode
	truncate bool
	drop     int
	spoof    bool
	asked    []string
	key      *tsig.Key
	now      func() time.Time
}

// New starts a server on 127.0.0.1, stopped when the test ends.
func New(t testing.TB) *Server {
	return NewAt(t, netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 0))
}

// NewAt starts a server at addr, on any free port when its port is zero.
// Servers on 127.0.0.2, 127.0.0.3 and so on can share a port, as a zone's
// nameservers all listen on 53.
func NewAt(t testing.TB, addr netip.AddrPort) *Server {
	t.Helper()
	s := &Server{rcodes: map[string]dnsmessage.RCode{}}
	var err error
	// UDP takes a free port and TCP has to get the same one; another
	// test can hold it for TCP, so try a few.
	for range 20 {
		if s.udp, err = net.ListenPacket("udp", addr.String()); err != nil {
			t.Fatal(err)
		}
		bound := s.udp.LocalAddr().(*net.UDPAddr).AddrPort()
		if s.tcp, err = net.Listen("tcp", bound.String()); err == nil {
			s.Addr = netip.AddrPortFrom(addr.Addr(), bound.Port())
			break
		}
		_ = s.udp.Close()
		if addr.Port() != 0 {
			t.Fatal(err)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	s.wg.Add(2)
	go s.serveUDP()
	go s.serveTCP()
	t.Cleanup(func() {
		_ = s.udp.Close()
		_ = s.tcp.Close()
		s.conns.Range(func(c, _ any) bool {
			_ = c.(net.Conn).Close()
			return true
		})
		s.wg.Wait()
	})
	return s
}

// Add puts records in.
func (s *Server) Add(rrs ...dnsmessage.Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rrs...)
}

// Remove takes out every record of type t at name.
func (s *Server) Remove(name string, t dnsmessage.Type) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.records[:0]
	for _, rr := range s.records {
		if rr.Header.Type != t || !same(rr.Header.Name.String(), name) {
			kept = append(kept, rr)
		}
	}
	s.records = kept
}

// Records are the records of type t at name.
func (s *Server) Records(name string, t dnsmessage.Type) []dnsmessage.Resource {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(name, t)
}

// SetRCode answers every question about name with code and nothing else;
// an empty name is every question.
func (s *Server) SetRCode(name string, code dnsmessage.RCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rcodes[strings.ToLower(fqdn(name))] = code
}

// Truncate answers questions over UDP with the TC bit and no records.
func (s *Server) Truncate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.truncate = true
}

// Drop leaves the next n questions over UDP unanswered.
func (s *Server) Drop(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drop = n
}

// Spoof sends an answer with the wrong ID ahead of each real one over UDP.
func (s *Server) Spoof() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spoof = true
}

// AcceptUpdates takes RFC 2136 updates to the zones the server holds,
// signed with key. now is the server's clock; nil is time.Now.
func (s *Server) AcceptUpdates(key tsig.Key, now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key, s.now = &key, now
	if s.now == nil {
		s.now = time.Now
	}
}

// Asked lists the questions put so far, as "udp example.test. SOA".
func (s *Server) Asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func (s *Server) serveUDP() {
	defer s.wg.Done()
	buf := make([]byte, 0xffff)
	for {
		n, from, err := s.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		resp, spoof := s.answer("udp", buf[:n])
		if resp == nil {
			continue
		}
		if spoof {
			wrong := append([]byte(nil), resp...)
			wrong[1]++
			_, _ = s.udp.WriteTo(wrong, from)
		}
		_, _ = s.udp.WriteTo(resp, from)
	}
}

func (s *Server) serveTCP() {
	defer s.wg.Done()
	for {
		conn, err := s.tcp.Accept()
		if err != nil {
			return
		}
		s.conns.Store(conn, true)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.conns.Delete(conn)
			defer conn.Close()
			for {
				var n [2]byte
				if _, err := io.ReadFull(conn, n[:]); err != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(n[:]))
				if _, err := io.ReadFull(conn, query); err != nil {
					return
				}
				resp, _ := s.answer("tcp", query)
				if resp == nil {
					return
				}
				out := binary.BigEndian.AppendUint16(nil, uint16(len(resp)))
				if _, err := conn.Write(append(out, resp...)); err != nil {
					return
				}
			}
		}()
	}
}

// answer packs the reply to one query, or nil to send none.
func (s *Server) answer(proto string, query []byte) ([]byte, bool) {
	var q dnsmessage.Message
	if err := q.Unpack(query); err != nil || len(q.Questions) == 0 {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	question := q.Questions[0]
	name := question.Name.String()
	s.asked = append(s.asked, proto+" "+name+" "+strings.TrimPrefix(question.Type.String(), "Type"))
	if proto == "udp" && s.drop > 0 {
		s.drop--
		return nil, false
	}
	if _, set := s.rcodes[strings.ToLower(name)]; q.OpCode == opUpdate && !set {
		return s.update(query, q), false
	}
	resp := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID: q.ID, Response: true, OpCode: q.OpCode, Authoritative: true,
			RecursionDesired: q.RecursionDesired, RecursionAvailable: q.RecursionDesired,
		},
		Questions: q.Questions,
	}
	code, ok := s.rcodes[strings.ToLower(name)]
	if !ok {
		code, ok = s.rcodes["."]
	}
	switch {
	case ok:
		resp.RCode = code
	case proto == "udp" && s.truncate:
		resp.Truncated = true
	default:
		resp.Answers, resp.RCode = s.resolve(name, question.Type)
	}
	raw, err := resp.Pack()
	if err != nil {
		return nil, false
	}
	return raw, proto == "udp" && s.spoof
}

// resolve answers from the records, following CNAMEs.
func (s *Server) resolve(name string, t dnsmessage.Type) ([]dnsmessage.Resource, dnsmessage.RCode) {
	var out []dnsmessage.Resource
	for range 16 {
		if found := s.find(name, t); len(found) > 0 {
			return append(out, found...), dnsmessage.RCodeSuccess
		}
		c := s.find(name, dnsmessage.TypeCNAME)
		if t == dnsmessage.TypeCNAME || len(c) == 0 {
			break
		}
		out = append(out, c[0])
		name = c[0].Body.(*dnsmessage.CNAMEResource).CNAME.String()
	}
	if len(out) == 0 && !s.exists(name) {
		return nil, dnsmessage.RCodeNameError
	}
	return out, dnsmessage.RCodeSuccess
}

func (s *Server) find(name string, t dnsmessage.Type) []dnsmessage.Resource {
	var out []dnsmessage.Resource
	for _, rr := range s.records {
		if rr.Header.Type == t && same(rr.Header.Name.String(), name) {
			out = append(out, rr)
		}
	}
	return out
}

// exists reports whether anything is held at name or under it.
func (s *Server) exists(name string) bool {
	suffix := "." + strings.ToLower(fqdn(name))
	for _, rr := range s.records {
		owner := strings.ToLower(rr.Header.Name.String())
		if same(owner, name) || strings.HasSuffix(owner, suffix) {
			return true
		}
	}
	return false
}

func same(a, b string) bool { return strings.EqualFold(fqdn(a), fqdn(b)) }

func fqdn(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

func header(name string, t dnsmessage.Type) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(fqdn(name)), Type: t, Class: dnsmessage.ClassINET, TTL: 60}
}

// TXT is a TXT record holding one string per value.
func TXT(name string, strs ...string) dnsmessage.Resource {
	return dnsmessage.Resource{Header: header(name, dnsmessage.TypeTXT), Body: &dnsmessage.TXTResource{TXT: strs}}
}

// CNAME points name at target.
func CNAME(name, target string) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: header(name, dnsmessage.TypeCNAME),
		Body:   &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(fqdn(target))},
	}
}

// SOA makes zone a zone.
func SOA(zone string) dnsmessage.Resource {
	return dnsmessage.Resource{Header: header(zone, dnsmessage.TypeSOA), Body: &dnsmessage.SOAResource{
		NS: dnsmessage.MustNewName("ns1." + fqdn(zone)), MBox: dnsmessage.MustNewName("hostmaster." + fqdn(zone)),
		Serial: 1, Refresh: 3600, Retry: 600, Expire: 86400, MinTTL: 60,
	}}
}

// NS names one of zone's nameservers.
func NS(zone, host string) dnsmessage.Resource {
	return dnsmessage.Resource{
		Header: header(zone, dnsmessage.TypeNS),
		Body:   &dnsmessage.NSResource{NS: dnsmessage.MustNewName(fqdn(host))},
	}
}

// Addr is an A or AAAA record, whichever addr is.
func Addr(name string, addr netip.Addr) dnsmessage.Resource {
	if addr.Is4() {
		return dnsmessage.Resource{Header: header(name, dnsmessage.TypeA), Body: &dnsmessage.AResource{A: addr.As4()}}
	}
	return dnsmessage.Resource{Header: header(name, dnsmessage.TypeAAAA), Body: &dnsmessage.AAAAResource{AAAA: addr.As16()}}
}

// opUpdate is the opcode of an RFC 2136 update.
const opUpdate = 5

// classNone marks an update record to delete (RFC 2136 §2.5.4).
const classNone = 254

// update applies an RFC 2136 update as BIND would: signed with the key
// or refused, every record inside the zone or none of them applied.
// Called with s.mu held.
func (s *Server) update(query []byte, q dnsmessage.Message) []byte {
	resp := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, OpCode: q.OpCode}, Questions: q.Questions}
	raw := func(code dnsmessage.RCode) []byte {
		resp.RCode = code
		out, _ := resp.Pack()
		return out
	}
	refuse := func(tsigErr uint16) []byte {
		out, _ := tsig.Refuse(raw(notAuth), query, tsigErr)
		return out
	}
	if s.key == nil {
		return raw(dnsmessage.RCodeRefused)
	}
	req, err := s.key.Verify(query, nil, s.now())
	answer := func(code dnsmessage.RCode, tsigErr uint16) []byte {
		out, _ := s.key.SignAnswer(raw(code), req, s.now(), tsigErr)
		return out
	}
	switch {
	case errors.Is(err, tsig.ErrOtherKey):
		return refuse(tsig.BadKey)
	case errors.Is(err, tsig.ErrTime):
		return answer(notAuth, tsig.BadTime)
	case err != nil:
		return refuse(tsig.BadSig)
	}
	zone := q.Questions[0].Name.String()
	if len(s.find(zone, dnsmessage.TypeSOA)) == 0 {
		return answer(notAuth, 0)
	}
	for _, rr := range q.Authorities {
		owner := strings.ToLower(rr.Header.Name.String())
		if !same(owner, zone) && !strings.HasSuffix(owner, "."+strings.ToLower(fqdn(zone))) {
			return answer(notZone, 0)
		}
	}
	for _, rr := range q.Authorities {
		name, typ := rr.Header.Name.String(), rr.Header.Type
		switch rr.Header.Class {
		case dnsmessage.ClassINET:
			if !s.holds(rr) {
				rr.Header.Length = 0
				s.records = append(s.records, rr)
			}
		case classNone:
			s.records = slices.DeleteFunc(s.records, func(held dnsmessage.Resource) bool {
				return sameRecord(held, rr)
			})
		case dnsmessage.ClassANY:
			s.records = slices.DeleteFunc(s.records, func(held dnsmessage.Resource) bool {
				return same(held.Header.Name.String(), name) && held.Header.Type == typ
			})
		}
	}
	return answer(dnsmessage.RCodeSuccess, 0)
}

// The response codes RFC 2136 adds.
const (
	notAuth dnsmessage.RCode = 9
	notZone dnsmessage.RCode = 10
)

// holds reports whether rr is held already. Called with s.mu held.
func (s *Server) holds(rr dnsmessage.Resource) bool {
	return slices.ContainsFunc(s.records, func(held dnsmessage.Resource) bool { return sameRecord(held, rr) })
}

// sameRecord compares name, type and data, as an update does.
func sameRecord(a, b dnsmessage.Resource) bool {
	if !same(a.Header.Name.String(), b.Header.Name.String()) || a.Header.Type != b.Header.Type {
		return false
	}
	at, aok := a.Body.(*dnsmessage.TXTResource)
	bt, bok := b.Body.(*dnsmessage.TXTResource)
	if aok && bok {
		return slices.Equal(at.TXT, bt.TXT)
	}
	return a.Body.GoString() == b.Body.GoString()
}
