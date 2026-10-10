package discovery

import (
	"bytes"
	"net/netip"
	"slices"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

type rr struct {
	section int
	name    string
	class   uint16
	body    dnsmessage.ResourceBody
}

func question(name string, t dnsmessage.Type, class uint16) dnsmessage.Question {
	return dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: t, Class: dnsmessage.Class(class)}
}

func a(name, addr string) rr {
	return rr{1, name, 0x8001, &dnsmessage.AResource{A: netip.MustParseAddr(addr).As4()}}
}

func aaaa(name, addr string) rr {
	return rr{1, name, 0x8001, &dnsmessage.AAAAResource{AAAA: netip.MustParseAddr(addr).As16()}}
}

func ptr(name, target string) rr {
	return rr{1, name, 1, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName(target)}}
}

func srv(name, target string) rr {
	return rr{1, name, 0x8001, &dnsmessage.SRVResource{Port: 8009, Target: dnsmessage.MustNewName(target)}}
}

func in(section int, r rr) rr {
	r.section = section
	return r
}

func pack(t *testing.T, h dnsmessage.Header, qs []dnsmessage.Question, rrs ...rr) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, h)
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		if err := b.Question(q); err != nil {
			t.Fatal(err)
		}
	}
	for section := 1; section <= 3; section++ {
		if err := startSection(&b, section); err != nil {
			t.Fatal(err)
		}
		for _, r := range rrs {
			if r.section != section {
				continue
			}
			rh := dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(r.name), Class: dnsmessage.Class(r.class), TTL: 120}
			var err error
			switch body := r.body.(type) {
			case *dnsmessage.AResource:
				err = b.AResource(rh, *body)
			case *dnsmessage.AAAAResource:
				err = b.AAAAResource(rh, *body)
			case *dnsmessage.PTRResource:
				err = b.PTRResource(rh, *body)
			case *dnsmessage.SRVResource:
				err = b.SRVResource(rh, *body)
			case *dnsmessage.TXTResource:
				err = b.TXTResource(rh, *body)
			case *dnsmessage.UnknownResource:
				err = b.UnknownResource(rh, *body)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func parse(t *testing.T, payload []byte, port int) *MDNS {
	t.Helper()
	m, err := ParseMDNS(payload, port)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var response = dnsmessage.Header{Response: true, Authoritative: true}

func TestClearQU(t *testing.T) {
	tests := []struct {
		name string
		qs   []dnsmessage.Question
		set  bool
	}{
		{"none set", []dnsmessage.Question{question("tv.local.", dnsmessage.TypeA, 1)}, false},
		{"mixed with compressed names", []dnsmessage.Question{
			question("_googlecast._tcp.local.", dnsmessage.TypePTR, 0x8001),
			question("tv.local.", dnsmessage.TypeA, 1),
			question("tv.local.", dnsmessage.TypeAAAA, 0x8001),
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := pack(t, dnsmessage.Header{ID: 0x1234}, tt.qs, a("tv.local.", "192.0.2.10"))
			got := slices.Clone(orig)
			set, err := ClearQU(got)
			if err != nil {
				t.Fatal(err)
			}
			if set != tt.set {
				t.Errorf("set = %v, want %v", set, tt.set)
			}
			changed := 0
			for i := range orig {
				if orig[i] == got[i] {
					continue
				}
				changed++
				if orig[i]&0x80 == 0 || got[i] != orig[i]&^0x80 {
					t.Errorf("byte %d went from %#x to %#x", i, orig[i], got[i])
				}
			}
			want := 0
			for _, q := range tt.qs {
				if q.Class&0x8000 != 0 {
					want++
				}
			}
			if changed != want {
				t.Errorf("%d bytes changed, want %d", changed, want)
			}
			m := parse(t, got, mdnsPort)
			if m.Records[0].Class != 1 || !m.Records[0].Flush {
				t.Errorf("answer class %d flush %v, want 1 and the flush bit untouched", m.Records[0].Class, m.Records[0].Flush)
			}
			var p dnsmessage.Parser
			if _, err := p.Start(got); err != nil {
				t.Fatal(err)
			}
			qs, _ := p.AllQuestions()
			for _, q := range qs {
				if q.Class != dnsmessage.ClassINET {
					t.Errorf("question %s class %#x", q.Name, uint16(q.Class))
				}
			}
		})
	}
}

func TestTruncated(t *testing.T) {
	full := pack(t, dnsmessage.Header{}, []dnsmessage.Question{question("_ipp._tcp.local.", dnsmessage.TypePTR, 0x8001)})
	for _, n := range []int{0, 11, 14, len(full) - 1} {
		if _, err := ClearQU(slices.Clone(full[:n])); err == nil {
			t.Errorf("ClearQU on %d of %d bytes: no error", n, len(full))
		}
		if _, err := ParseMDNS(full[:n], mdnsPort); err == nil {
			t.Errorf("ParseMDNS on %d of %d bytes: no error", n, len(full))
		}
	}
	short := full[:len(full)-1]
	before := slices.Clone(short)
	_, _ = ClearQU(short)
	if !bytes.Equal(short, before) {
		t.Error("ClearQU changed a truncated packet")
	}
}

func TestParseMDNSKind(t *testing.T) {
	q := []dnsmessage.Question{question("tv.local.", dnsmessage.TypeA, 1)}
	tests := []struct {
		name string
		h    dnsmessage.Header
		rrs  []rr
		port int
		want Kind
	}{
		{"query", dnsmessage.Header{}, nil, 5353, KindQuery},
		{"legacy", dnsmessage.Header{ID: 77}, nil, 49152, KindLegacy},
		{"probe", dnsmessage.Header{}, []rr{in(2, a("tv.local.", "192.0.2.10"))}, 5353, KindProbe},
		{"legacy probe", dnsmessage.Header{}, []rr{in(2, a("tv.local.", "192.0.2.10"))}, 40000, KindLegacy},
		{"known answer is still a query", dnsmessage.Header{}, []rr{a("tv.local.", "192.0.2.10")}, 5353, KindQuery},
		{"answer", response, []rr{a("tv.local.", "192.0.2.10")}, 5353, KindAnswer},
		{"answer to a legacy query", response, []rr{a("tv.local.", "192.0.2.10")}, 61900, KindAnswer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parse(t, pack(t, tt.h, q, tt.rrs...), tt.port).Kind; got != tt.want {
				t.Errorf("Kind = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestParseMDNSRecords(t *testing.T) {
	txt := rr{3, "Den._googlecast._tcp.local.", 0x8001, &dnsmessage.TXTResource{TXT: []string{"fn=Den", "md=Box"}}}
	m := parse(t, pack(t, response, nil,
		ptr("_googlecast._tcp.local.", "Den._googlecast._tcp.local."),
		in(3, srv("Den._googlecast._tcp.local.", "den.local.")),
		txt,
		in(3, aaaa("den.local.", "fd00::5")),
	), mdnsPort)
	want := []Record{
		{Section: 1, Name: "_googlecast._tcp.local.", Type: dnsmessage.TypePTR, Class: 1, TTL: 120, Target: "Den._googlecast._tcp.local."},
		{Section: 3, Name: "Den._googlecast._tcp.local.", Type: dnsmessage.TypeSRV, Class: 1, Flush: true, TTL: 120, Target: "den.local.", Port: 8009},
		{Section: 3, Name: "Den._googlecast._tcp.local.", Type: dnsmessage.TypeTXT, Class: 1, Flush: true, TTL: 120, Text: []string{"fn=Den", "md=Box"}},
		{Section: 3, Name: "den.local.", Type: dnsmessage.TypeAAAA, Class: 1, Flush: true, TTL: 120, Addr: netip.MustParseAddr("fd00::5")},
	}
	if len(m.Records) != len(want) {
		t.Fatalf("%d records, want %d", len(m.Records), len(want))
	}
	for i, r := range m.Records {
		w := want[i]
		if r.Section != w.Section || r.Name != w.Name || r.Type != w.Type || r.Class != w.Class || r.Flush != w.Flush ||
			r.TTL != w.TTL || r.Target != w.Target || r.Port != w.Port || r.Addr != w.Addr || !slices.Equal(r.Text, w.Text) {
			t.Errorf("record %d = %+v, want %+v", i, r, w)
		}
	}
	if m.Name() != "_googlecast._tcp.local." {
		t.Errorf("Name() = %q", m.Name())
	}
}

func TestRewriteUnchanged(t *testing.T) {
	payload := pack(t, response, nil,
		ptr("_googlecast._tcp.local.", "Den._googlecast._tcp.local."),
		in(3, a("den.local.", "192.0.2.10")),
		in(3, aaaa("den.local.", "fd00::5")),
	)
	out, removed := Rewrite(payload, parse(t, payload, mdnsPort), NewFilter([]string{"_googlecast._tcp"}))
	if len(out) != len(payload) || &out[0] != &payload[0] || removed != "" {
		t.Errorf("a packet with nothing to remove was copied (removed %q)", removed)
	}
}

type kept struct {
	name string
	t    dnsmessage.Type
}

func TestRewrite(t *testing.T) {
	cast := []string{"_googlecast._tcp"}
	tests := []struct {
		name    string
		h       dnsmessage.Header
		qs      []dnsmessage.Question
		rrs     []rr
		allow   []string
		removed string
		nilOut  bool
		wantQ   []string
		wantR   []kept
	}{
		{
			name: "fe80 AAAA goes, ULA AAAA and IPv4 A stay",
			h:    response,
			rrs: []rr{
				aaaa("den.local.", "fe80::1"),
				aaaa("den.local.", "fd00::5"),
				a("den.local.", "192.0.2.10"),
			},
			removed: "link-local",
			wantR:   []kept{{"den.local.", dnsmessage.TypeAAAA}, {"den.local.", dnsmessage.TypeA}},
		},
		{
			name:    "169.254 A goes",
			h:       response,
			rrs:     []rr{a("den.local.", "169.254.3.4"), a("den.local.", "192.0.2.10")},
			removed: "link-local",
			wantR:   []kept{{"den.local.", dnsmessage.TypeA}},
		},
		{
			name: "host name question passes the allow list",
			qs: []dnsmessage.Question{
				question("den.local.", dnsmessage.TypeA, 1),
				question("_ipp._tcp.local.", dnsmessage.TypePTR, 1),
			},
			allow:   cast,
			removed: "filtered",
			wantQ:   []string{"den.local."},
		},
		{
			name:    "service type compared without case",
			qs:      []dnsmessage.Question{question("_GoogleCast._TCP.local.", dnsmessage.TypePTR, 1), question("_ipp._tcp.local.", dnsmessage.TypePTR, 1)},
			allow:   []string{"_googlecast._tcp"},
			removed: "filtered",
			wantQ:   []string{"_GoogleCast._TCP.local."},
		},
		{
			name: "PTR passes by its target, SRV of another type goes",
			h:    response,
			rrs: []rr{
				ptr("_services._dns-sd._udp.local.", "_googlecast._tcp.local."),
				srv("Office._ipp._tcp.local.", "printer.local."),
				in(3, srv("Den._googlecast._tcp.local.", "den.local.")),
			},
			allow:   cast,
			removed: "filtered",
			wantR: []kept{
				{"_services._dns-sd._udp.local.", dnsmessage.TypePTR},
				{"Den._googlecast._tcp.local.", dnsmessage.TypeSRV},
			},
		},
		{
			name:    "service enumeration question refused",
			qs:      []dnsmessage.Question{question("_services._dns-sd._udp.local.", dnsmessage.TypePTR, 1)},
			allow:   cast,
			removed: "filtered",
			nilOut:  true,
		},
		{
			name:    "query with every question filtered",
			qs:      []dnsmessage.Question{question("_ipp._tcp.local.", dnsmessage.TypePTR, 1), question("_airplay._tcp.local.", dnsmessage.TypePTR, 1)},
			allow:   cast,
			removed: "filtered",
			nilOut:  true,
		},
		{
			name: "response whose only answer was filtered, additionals ignored",
			h:    response,
			rrs: []rr{
				ptr("_ipp._tcp.local.", "Office._ipp._tcp.local."),
				in(3, a("printer.local.", "192.0.2.20")),
			},
			allow:   cast,
			removed: "filtered",
			nilOut:  true,
		},
		{
			name:    "response whose only answer was link-local",
			h:       response,
			rrs:     []rr{aaaa("den.local.", "fe80::1"), in(3, a("den.local.", "192.0.2.10"))},
			removed: "link-local",
			nilOut:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := pack(t, tt.h, tt.qs, tt.rrs...)
			out, removed := Rewrite(payload, parse(t, payload, mdnsPort), NewFilter(tt.allow))
			if removed != tt.removed {
				t.Errorf("removed = %q, want %q", removed, tt.removed)
			}
			if tt.nilOut {
				if out != nil {
					t.Errorf("relayed %d bytes, want nothing", len(out))
				}
				return
			}
			m := parse(t, out, mdnsPort)
			if !slices.Equal(m.Questions, tt.wantQ) {
				t.Errorf("questions %q, want %q", m.Questions, tt.wantQ)
			}
			var got []kept
			for _, r := range m.Records {
				got = append(got, kept{r.Name, r.Type})
			}
			if !slices.Equal(got, tt.wantR) {
				t.Errorf("records %v, want %v", got, tt.wantR)
			}
		})
	}
}

func TestRewriteKeepsWhatItDoesNotRemove(t *testing.T) {
	h := dnsmessage.Header{ID: 0x0bad, Response: true, Authoritative: true, Truncated: true}
	// 49 is den.local. in the A record, after the 37 bytes of the AAAA that goes.
	nsecData := []byte{0xC0, 49, 0x00, 0x04, 0x40, 0x00, 0x00, 0x08}
	payload := pack(t, h, nil,
		aaaa("old.local.", "fe80::1"),
		a("den.local.", "192.0.2.10"),
		in(2, rr{0, "den.local.", 1, &dnsmessage.UnknownResource{Type: 65280, Data: []byte{1, 2, 3}}}),
		in(3, rr{0, "den.local.", 0x8001, &dnsmessage.UnknownResource{Type: typeNSEC, Data: nsecData}}),
	)
	out, _ := Rewrite(payload, parse(t, payload, mdnsPort), nil)
	if out == nil {
		t.Fatal("nothing relayed")
	}
	var p dnsmessage.Parser
	got, err := p.Start(out)
	if err != nil {
		t.Fatal(err)
	}
	if got != h {
		t.Errorf("header %+v, want %+v", got, h)
	}
	if _, err := p.AllQuestions(); err != nil {
		t.Fatal(err)
	}
	ans, _ := p.AllAnswers()
	auth, _ := p.AllAuthorities()
	add, err := p.AllAdditionals()
	if err != nil {
		t.Fatal(err)
	}
	if len(ans) != 1 || ans[0].Header.Class != 0x8001 {
		t.Errorf("answers %+v, want the A with the cache-flush bit", ans)
	}
	if len(auth) != 1 {
		t.Fatalf("%d authority records, want 1", len(auth))
	}
	if u, ok := auth[0].Body.(*dnsmessage.UnknownResource); !ok || u.Type != 65280 || !bytes.Equal(u.Data, []byte{1, 2, 3}) {
		t.Errorf("unknown record became %+v", auth[0].Body)
	}
	if len(add) != 1 || add[0].Header.Class != 0x8001 {
		t.Fatalf("additionals %+v, want the NSEC with the cache-flush bit", add)
	}
	wantNSEC := append([]byte{3, 'd', 'e', 'n', 5, 'l', 'o', 'c', 'a', 'l', 0}, nsecData[2:]...)
	if u := add[0].Body.(*dnsmessage.UnknownResource); !bytes.Equal(u.Data, wantNSEC) {
		t.Errorf("NSEC data %v, want %v", u.Data, wantNSEC)
	}
}

func TestFilterAllows(t *testing.T) {
	f := NewFilter([]string{"_GoogleCast._tcp", "_ipp._tcp"})
	tests := []struct {
		name string
		want bool
	}{
		{"tv.local.", true},
		{"_googlecast._tcp.local.", true},
		{"Den._googlecast._TCP.local.", true},
		{"_printer._sub._ipp._tcp.local.", true},
		{"_airplay._tcp.local.", false},
		{"Den._raop._udp.local.", false},
		{"_services._dns-sd._udp.local.", false},
		{"_Services._DNS-SD._udp.local.", false},
	}
	for _, tt := range tests {
		if got := f.Allows(tt.name); got != tt.want {
			t.Errorf("Allows(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
	if !NewFilter(nil).Allows("_services._dns-sd._udp.local.") {
		t.Error("an empty filter refused service enumeration")
	}
}
