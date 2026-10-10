package discovery

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	mdnsPort     = 5353
	flushBit     = 1 << 15
	servicesMeta = "_services._dns-sd._udp."
	typeNSEC     = dnsmessage.Type(47)
)

var (
	errTruncated = errors.New("mdns: truncated packet")
	errLabel     = errors.New("mdns: bad label")
	linkLocal4   = netip.MustParsePrefix("169.254.0.0/16")
	linkLocal6   = netip.MustParsePrefix("fe80::/10")
)

// Filter is the allow list of service types ("_googlecast._tcp"), lower case; empty allows all.
type Filter []string

// NewFilter builds a Filter from service types as typed.
func NewFilter(types []string) Filter {
	if len(types) == 0 {
		return nil
	}
	f := make(Filter, 0, len(types))
	for _, t := range types {
		f = append(f, strings.ToLower(strings.TrimSuffix(t, ".")))
	}
	return f
}

// Allows reports whether a DNS name may cross the relay.
func (f Filter) Allows(name string) bool {
	if len(f) == 0 {
		return true
	}
	if strings.HasPrefix(strings.ToLower(name), servicesMeta) {
		return false
	}
	st := ServiceType(name)
	return st == "" || slices.Contains(f, st)
}

// ServiceType returns the "_x._tcp" pair in a name, lower case, or "".
func ServiceType(name string) string {
	labels := strings.Split(strings.TrimSuffix(name, "."), ".")
	for i := 0; i+1 < len(labels); i++ {
		proto := strings.ToLower(labels[i+1])
		if len(labels[i]) > 1 && labels[i][0] == '_' && (proto == "_tcp" || proto == "_udp") {
			return strings.ToLower(labels[i]) + "." + proto
		}
	}
	return ""
}

// Record is one resource record of an mDNS message.
type Record struct {
	Section int
	Name    string
	Type    dnsmessage.Type
	Class   uint16 // cache-flush bit stripped
	Flush   bool   // the cache-flush bit
	TTL     uint32
	Target  string
	Port    uint16
	Addr    netip.Addr
	Text    []string
}

// MDNS is a parsed mDNS message.
type MDNS struct {
	Kind      Kind
	ID        uint16
	Questions []string
	Records   []Record
}

// ParseMDNS reads an mDNS payload. srcPort decides query against legacy.
func ParseMDNS(payload []byte, srcPort int) (*MDNS, error) {
	var p dnsmessage.Parser
	h, err := p.Start(payload)
	if err != nil {
		return nil, fmt.Errorf("mdns: %w", err)
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return nil, fmt.Errorf("mdns: %w", err)
	}
	m := &MDNS{ID: h.ID}
	for _, q := range qs {
		m.Questions = append(m.Questions, q.Name.String())
	}
	authority := false
	for section := 1; section <= 3; section++ {
		rs, err := sectionResources(&p, section)
		if err != nil {
			return nil, fmt.Errorf("mdns: %w", err)
		}
		authority = authority || (section == 2 && len(rs) > 0)
		for _, r := range rs {
			m.Records = append(m.Records, record(section, r))
		}
	}
	switch {
	case h.Response:
		m.Kind = KindAnswer
	case srcPort != mdnsPort:
		m.Kind = KindLegacy
	case authority:
		m.Kind = KindProbe
	default:
		m.Kind = KindQuery
	}
	return m, nil
}

// Name is the first question or the first record name, for the log; "" when none.
func (m *MDNS) Name() string {
	if len(m.Questions) > 0 {
		return m.Questions[0]
	}
	if len(m.Records) > 0 {
		return m.Records[0].Name
	}
	return ""
}

// ClearQU clears the unicast-response bit of every question in place and reports whether any was set.
func ClearQU(payload []byte) (bool, error) {
	if err := walkQuestions(payload, nil); err != nil {
		return false, err
	}
	set := false
	_ = walkQuestions(payload, func(classOff int) {
		if payload[classOff]&0x80 != 0 {
			payload[classOff] &^= 0x80
			set = true
		}
	})
	return set, nil
}

// Rewrite returns what to relay: payload itself, a re-serialised message, or nil, and what was removed.
func Rewrite(payload []byte, m *MDNS, allow Filter) ([]byte, string) {
	keepQ := make([]bool, len(m.Questions))
	keepR := make([]bool, len(m.Records))
	linkLocal, filtered := false, false
	for i, q := range m.Questions {
		keepQ[i] = allow.Allows(q)
		filtered = filtered || !keepQ[i]
	}
	for i, r := range m.Records {
		keep := true
		switch r.Type {
		case dnsmessage.TypeA, dnsmessage.TypeAAAA:
			if a := r.Addr.Unmap(); linkLocal4.Contains(a) || linkLocal6.Contains(a) {
				keep, linkLocal = false, true
			}
		case dnsmessage.TypePTR:
			if !allow.Allows(r.Name) && !allow.Allows(r.Target) {
				keep, filtered = false, true
			}
		case dnsmessage.TypeSRV, dnsmessage.TypeTXT:
			if !allow.Allows(r.Name) {
				keep, filtered = false, true
			}
		}
		keepR[i] = keep
	}
	if !linkLocal && !filtered {
		return payload, ""
	}
	reason := "link-local"
	if filtered {
		reason = "filtered"
	}
	if m.Kind == KindAnswer {
		answered := false
		for i, r := range m.Records {
			answered = answered || (r.Section == 1 && keepR[i])
		}
		if !answered {
			return nil, reason
		}
	} else if !slices.Contains(keepQ, true) {
		return nil, reason
	}
	out, err := rebuild(payload, keepQ, keepR)
	if err != nil {
		return nil, reason
	}
	return out, reason
}

func record(section int, r dnsmessage.Resource) Record {
	rec := Record{
		Section: section,
		Name:    r.Header.Name.String(),
		Type:    r.Header.Type,
		Class:   uint16(r.Header.Class) &^ flushBit,
		Flush:   uint16(r.Header.Class)&flushBit != 0,
		TTL:     r.Header.TTL,
	}
	switch b := r.Body.(type) {
	case *dnsmessage.AResource:
		rec.Addr = netip.AddrFrom4(b.A)
	case *dnsmessage.AAAAResource:
		rec.Addr = netip.AddrFrom16(b.AAAA)
	case *dnsmessage.PTRResource:
		rec.Target = b.PTR.String()
	case *dnsmessage.SRVResource:
		rec.Target = b.Target.String()
		rec.Port = b.Port
	case *dnsmessage.TXTResource:
		rec.Text = b.TXT
	}
	return rec
}

func sectionResources(p *dnsmessage.Parser, section int) ([]dnsmessage.Resource, error) {
	switch section {
	case 1:
		return p.AllAnswers()
	case 2:
		return p.AllAuthorities()
	default:
		return p.AllAdditionals()
	}
}

func startSection(b *dnsmessage.Builder, section int) error {
	switch section {
	case 1:
		return b.StartAnswers()
	case 2:
		return b.StartAuthorities()
	default:
		return b.StartAdditionals()
	}
}

func rebuild(payload []byte, keepQ, keepR []bool) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(payload)
	if err != nil {
		return nil, err
	}
	qs, err := p.AllQuestions()
	if err != nil {
		return nil, err
	}
	offs, err := rdataOffsets(payload)
	if err != nil {
		return nil, err
	}
	if len(qs) != len(keepQ) || len(offs) != len(keepR) {
		return nil, errors.New("mdns: message does not match its parse")
	}
	b := dnsmessage.NewBuilder(make([]byte, 0, len(payload)), h)
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	for i, q := range qs {
		if keepQ[i] {
			if err := b.Question(q); err != nil {
				return nil, err
			}
		}
	}
	i := 0
	for section := 1; section <= 3; section++ {
		rs, err := sectionResources(&p, section)
		if err != nil {
			return nil, err
		}
		if err := startSection(&b, section); err != nil {
			return nil, err
		}
		for _, r := range rs {
			if keepR[i] {
				if err := put(&b, r, payload, offs[i]); err != nil {
					return nil, err
				}
			}
			i++
		}
	}
	return b.Finish()
}

func put(b *dnsmessage.Builder, r dnsmessage.Resource, payload []byte, rdata int) error {
	h := r.Header
	switch body := r.Body.(type) {
	case *dnsmessage.AResource:
		return b.AResource(h, *body)
	case *dnsmessage.AAAAResource:
		return b.AAAAResource(h, *body)
	case *dnsmessage.PTRResource:
		return b.PTRResource(h, *body)
	case *dnsmessage.SRVResource:
		return b.SRVResource(h, *body)
	case *dnsmessage.TXTResource:
		return b.TXTResource(h, *body)
	case *dnsmessage.CNAMEResource:
		return b.CNAMEResource(h, *body)
	case *dnsmessage.MXResource:
		return b.MXResource(h, *body)
	case *dnsmessage.NSResource:
		return b.NSResource(h, *body)
	case *dnsmessage.SOAResource:
		return b.SOAResource(h, *body)
	case *dnsmessage.OPTResource:
		return b.OPTResource(h, *body)
	case *dnsmessage.SVCBResource:
		return b.SVCBResource(h, *body)
	case *dnsmessage.HTTPSResource:
		return b.HTTPSResource(h, *body)
	case *dnsmessage.UnknownResource:
		u := *body
		if u.Type == typeNSEC {
			// mDNS compresses the NSEC next name; a pointer would be wrong once records move.
			name, end, err := expandName(payload, rdata)
			if err != nil {
				return err
			}
			if end-rdata > len(u.Data) {
				return errTruncated
			}
			u.Data = append(name, u.Data[end-rdata:]...)
		}
		return b.UnknownResource(h, u)
	}
	return fmt.Errorf("mdns: cannot write a %v record", r.Header.Type)
}

func skipName(p []byte, off int) (int, error) {
	for {
		if off >= len(p) {
			return 0, errTruncated
		}
		c := int(p[off])
		switch {
		case c == 0:
			return off + 1, nil
		case c&0xC0 == 0xC0:
			if off+2 > len(p) {
				return 0, errTruncated
			}
			return off + 2, nil
		case c&0xC0 != 0:
			return 0, errLabel
		}
		off += 1 + c
	}
}

func expandName(p []byte, off int) ([]byte, int, error) {
	var name []byte
	end := -1
	for hops := 0; hops < 128; {
		if off >= len(p) {
			return nil, 0, errTruncated
		}
		c := int(p[off])
		switch {
		case c == 0:
			if end < 0 {
				end = off + 1
			}
			return append(name, 0), end, nil
		case c&0xC0 == 0xC0:
			if off+2 > len(p) {
				return nil, 0, errTruncated
			}
			if end < 0 {
				end = off + 2
			}
			off = int(binary.BigEndian.Uint16(p[off:]) & 0x3FFF)
			hops++
		case c&0xC0 != 0:
			return nil, 0, errLabel
		default:
			if off+1+c > len(p) {
				return nil, 0, errTruncated
			}
			name = append(name, p[off:off+1+c]...)
			if len(name) > 254 {
				return nil, 0, errLabel
			}
			off += 1 + c
		}
	}
	return nil, 0, errLabel
}

func walkQuestions(p []byte, class func(off int)) error {
	if len(p) < 12 {
		return errTruncated
	}
	off := 12
	for range binary.BigEndian.Uint16(p[4:]) {
		end, err := skipName(p, off)
		if err != nil {
			return err
		}
		if end+4 > len(p) {
			return errTruncated
		}
		if class != nil {
			class(end + 2)
		}
		off = end + 4
	}
	return nil
}

func rdataOffsets(p []byte) ([]int, error) {
	if len(p) < 12 {
		return nil, errTruncated
	}
	off := 12
	for range binary.BigEndian.Uint16(p[4:]) {
		end, err := skipName(p, off)
		if err != nil {
			return nil, err
		}
		off = end + 4
	}
	n := int(binary.BigEndian.Uint16(p[6:])) + int(binary.BigEndian.Uint16(p[8:])) + int(binary.BigEndian.Uint16(p[10:]))
	offs := make([]int, 0, min(n, len(p)/11))
	for range n {
		end, err := skipName(p, off)
		if err != nil {
			return nil, err
		}
		if end+10 > len(p) {
			return nil, errTruncated
		}
		off = end + 10
		offs = append(offs, off)
		off += int(binary.BigEndian.Uint16(p[end+8:]))
		if off > len(p) {
			return nil, errTruncated
		}
	}
	return offs, nil
}
