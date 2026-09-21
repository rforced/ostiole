package dnslog

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rforced/ostiole/internal/model"
)

// answer is one reply to build, in the shapes the live router was seen to
// produce: a name dnsmasq made the answer up for has an empty authority
// section, one that came back through unbound carries a SOA.
type answer struct {
	name      string
	qtype     dnsmessage.Type
	rcode     dnsmessage.RCode
	addresses []netip.Addr
	// other is an answer record that is not an address, like a TXT.
	other bool
	// soa is the authority record an upstream negative answer carries.
	soa bool
	// query builds a question rather than a reply.
	query bool
	// questions overrides the one question a reply normally carries.
	questions int
}

func build(t *testing.T, a answer) []byte {
	t.Helper()
	if a.qtype == 0 {
		a.qtype = dnsmessage.TypeA
	}
	name := dnsmessage.MustNewName(a.name)
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: 1, Response: !a.query, RecursionDesired: true, RecursionAvailable: true, RCode: a.rcode,
	})
	q := dnsmessage.Question{Name: name, Type: a.qtype, Class: dnsmessage.ClassINET}
	must(t, b.StartQuestions())
	n := max(a.questions, 1)
	for range n {
		must(t, b.Question(q))
	}
	must(t, b.StartAnswers())
	h := dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: 60}
	for _, addr := range a.addresses {
		if addr.Is4() {
			must(t, b.AResource(h, dnsmessage.AResource{A: addr.As4()}))
		} else {
			must(t, b.AAAAResource(h, dnsmessage.AAAAResource{AAAA: addr.As16()}))
		}
	}
	if a.other {
		must(t, b.TXTResource(h, dnsmessage.TXTResource{TXT: []string{"v=spf1 -all"}}))
	}
	must(t, b.StartAuthorities())
	if a.soa {
		must(t, b.SOAResource(dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: 60},
			dnsmessage.SOAResource{
				NS: dnsmessage.MustNewName("ns.example.com."), MBox: dnsmessage.MustNewName("root.example.com."),
				Serial: 1, Refresh: 1, Retry: 1, Expire: 1, MinTTL: 1,
			}))
	}
	raw, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func ipv4UDP(src, dst netip.Addr, sport, dport uint16, payload []byte) []byte {
	b := make([]byte, 28+len(payload))
	b[0] = 0x45
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	b[8], b[9] = 64, 17
	copy(b[12:16], src.AsSlice())
	copy(b[16:20], dst.AsSlice())
	udp(b[20:], sport, dport, payload)
	return b
}

func ipv6UDP(src, dst netip.Addr, sport, dport uint16, payload []byte) []byte {
	b := make([]byte, 48+len(payload))
	b[0] = 0x60
	binary.BigEndian.PutUint16(b[4:6], uint16(8+len(payload)))
	b[6], b[7] = 17, 64
	copy(b[8:24], src.AsSlice())
	copy(b[24:40], dst.AsSlice())
	udp(b[40:], sport, dport, payload)
	return b
}

func udp(b []byte, sport, dport uint16, payload []byte) {
	binary.BigEndian.PutUint16(b[0:2], sport)
	binary.BigEndian.PutUint16(b[2:4], dport)
	binary.BigEndian.PutUint16(b[4:6], uint16(8+len(payload)))
	copy(b[8:], payload)
}

// classifyPacket runs one packet through the listener's parsing and the
// classifier, the way the hook does.
func classifyPacket(t *testing.T, x *Index, packet []byte) (Entry, []string, bool) {
	t.Helper()
	client, payload, ok := udpReply(packet)
	if !ok {
		return Entry{}, nil, false
	}
	e, lists, ok := classify(x, payload)
	e.Client = client
	return e, lists, ok
}

var (
	router = netip.MustParseAddr("192.168.1.1")
	client = netip.MustParseAddr("192.168.1.50")
)

// Every row of the reply-shape table the live router was read for.
func TestClassifyReplyShapes(t *testing.T) {
	t.Parallel()
	c, o := testCache(t)
	x, err := Build(o, c)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		what   string
		reply  answer
		status Status
		reason Reason
		lists  int
	}{
		{
			what:   "a listed name, answered by dnsmasq itself",
			reply:  answer{name: "ads.example.com.", rcode: dnsmessage.RCodeNameError},
			status: StatusBlocked, reason: ReasonList, lists: 2,
		},
		{
			what:   "a listed name asked for as HTTPS",
			reply:  answer{name: "ads.example.com.", qtype: 65, rcode: dnsmessage.RCodeNameError},
			status: StatusBlocked, reason: ReasonList, lists: 2,
		},
		{
			what:   "the Firefox canary",
			reply:  answer{name: "use-application-dns.net.", rcode: dnsmessage.RCodeNameError},
			status: StatusBlocked, reason: ReasonCanary,
		},
		{
			what:   "the deny list",
			reply:  answer{name: "bad.example.org.", rcode: dnsmessage.RCodeNameError},
			status: StatusBlocked, reason: ReasonDeny,
		},
		{
			what:   "a name nobody has, refused upstream with a SOA",
			reply:  answer{name: "missing.example.test.", rcode: dnsmessage.RCodeNameError, soa: true},
			status: StatusNXDomain,
		},
		{
			what:   "a private reverse name dnsmasq refuses on its own",
			reply:  answer{name: "99.99.99.10.in-addr.arpa.", qtype: dnsmessage.TypePTR, rcode: dnsmessage.RCodeNameError},
			status: StatusNXDomain,
		},
		{
			what:   "a real name",
			reply:  answer{name: "www.example.test.", addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
			status: StatusOK,
		},
		{
			what:   "a real name over IPv6",
			reply:  answer{name: "www.example.test.", qtype: dnsmessage.TypeAAAA, addresses: []netip.Addr{netip.MustParseAddr("2606:2800:220:1::1")}},
			status: StatusOK,
		},
		{
			what:   "an answer that carries no address",
			reply:  answer{name: "www.example.test.", qtype: dnsmessage.TypeTXT, other: true},
			status: StatusOK,
		},
		{
			what:   "no data, with the SOA an upstream sends",
			reply:  answer{name: "www.example.test.", qtype: dnsmessage.TypeAAAA, soa: true},
			status: StatusNoData,
		},
		{
			what:   "a listed name with no data and nothing in authority",
			reply:  answer{name: "ads.example.com.", qtype: dnsmessage.TypeAAAA},
			status: StatusBlocked, reason: ReasonList, lists: 2,
		},
		{
			what:   "the resolver failing",
			reply:  answer{name: "www.example.test.", rcode: dnsmessage.RCodeServerFailure},
			status: StatusServFail,
		},
		{
			what:   "a refusal",
			reply:  answer{name: "www.example.test.", rcode: dnsmessage.RCodeRefused},
			status: StatusRefused,
		},
		{
			what:   "something else entirely",
			reply:  answer{name: "www.example.test.", rcode: dnsmessage.RCodeNotImplemented},
			status: StatusOther,
		},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			packet := ipv4UDP(router, client, 53, 40000, build(t, tc.reply))
			e, lists, ok := classifyPacket(t, x, packet)
			if !ok {
				t.Fatal("dropped")
			}
			if e.Status != tc.status {
				t.Errorf("status = %s, want %s", e.Status, tc.status)
			}
			if e.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", e.Reason, tc.reason)
			}
			if len(lists) != tc.lists {
				t.Errorf("lists = %v, want %d", lists, tc.lists)
			}
			if e.Client != client {
				t.Errorf("client = %v", e.Client)
			}
		})
	}
}

// Null mode answers a blocked name with 0.0.0.0, which is an answer with
// an address in it and has to be told from a real one.
func TestClassifyNullMode(t *testing.T) {
	t.Parallel()
	c, o := testCache(t)
	o.Mode = model.BlockNull
	x, err := Build(o, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"0.0.0.0", "::"} {
		null := netip.MustParseAddr(addr)
		qtype := dnsmessage.TypeA
		if null.Is6() {
			qtype = dnsmessage.TypeAAAA
		}
		packet := ipv4UDP(router, client, 53, 40000,
			build(t, answer{name: "ads.example.com.", qtype: qtype, addresses: []netip.Addr{null}}))
		e, _, ok := classifyPacket(t, x, packet)
		if !ok || e.Status != StatusBlocked {
			t.Errorf("%s: status = %s", addr, e.Status)
		}
		if e.Answer.IsValid() {
			t.Errorf("%s: answer = %v, want nothing", addr, e.Answer)
		}
	}
	// A listed name that really does resolve to an address is not the
	// null answer, and stays an answer.
	packet := ipv4UDP(router, client, 53, 40000,
		build(t, answer{name: "ads.example.com.", addresses: []netip.Addr{netip.MustParseAddr("10.1.2.3")}}))
	if e, _, _ := classifyPacket(t, x, packet); e.Status != StatusOK {
		t.Errorf("status = %s, want ok", e.Status)
	}
}

// Until the first index is built there is nothing to say a name is
// blocked, and guessing from the shape alone would accuse bogus-priv and
// domain-needed of being blocklists.
func TestClassifyWithoutAnIndex(t *testing.T) {
	t.Parallel()
	packet := ipv4UDP(router, client, 53, 40000,
		build(t, answer{name: "ads.example.com.", rcode: dnsmessage.RCodeNameError}))
	e, lists, ok := classifyPacket(t, nil, packet)
	if !ok {
		t.Fatal("dropped")
	}
	if e.Status != StatusNXDomain || lists != nil {
		t.Errorf("status = %s, lists = %v", e.Status, lists)
	}
}

func TestClassifyIPv6Client(t *testing.T) {
	t.Parallel()
	c, o := testCache(t)
	x, _ := Build(o, c)
	src := netip.MustParseAddr("2001:db8:1::1")
	dst := netip.MustParseAddr("2001:db8:1::50")
	packet := ipv6UDP(src, dst, 53, 40000,
		build(t, answer{name: "ads.example.com.", rcode: dnsmessage.RCodeNameError}))
	e, _, ok := classifyPacket(t, x, packet)
	if !ok {
		t.Fatal("dropped")
	}
	if e.Client != dst {
		t.Errorf("client = %v, want %v", e.Client, dst)
	}
	if e.Status != StatusBlocked {
		t.Errorf("status = %s", e.Status)
	}
}

// What the log is not interested in never reaches the ring.
func TestClassifyDrops(t *testing.T) {
	t.Parallel()
	body := build(t, answer{name: "www.example.test."})
	cases := []struct {
		what   string
		packet []byte
	}{
		{"a query rather than a reply", ipv4UDP(client, router, 40000, 53, build(t, answer{name: "www.example.test.", query: true}))},
		{"something that is not from port 53", ipv4UDP(router, client, 5353, 40000, body)},
		{"a packet with no UDP header", ipv4UDP(router, client, 53, 40000, nil)[:24]},
		{"a truncated IP header", []byte{0x45, 0x00}},
		{"a payload that is not DNS", ipv4UDP(router, client, 53, 40000, []byte("hello"))},
		{"two questions in one reply", ipv4UDP(router, client, 53, 40000, build(t, answer{name: "www.example.test.", questions: 2}))},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			if _, _, ok := classifyPacket(t, nil, tc.packet); ok {
				t.Error("kept")
			}
		})
	}
}

func TestTypeNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		t    uint16
		name string
	}{{1, "A"}, {28, "AAAA"}, {65, "HTTPS"}, {12, "PTR"}, {257, "CAA"}, {9999, "9999"}} {
		if got := TypeName(tc.t); got != tc.name {
			t.Errorf("TypeName(%d) = %q, want %q", tc.t, got, tc.name)
		}
		got, ok := ParseType(tc.name)
		if !ok || got != tc.t {
			t.Errorf("ParseType(%q) = %d, %v", tc.name, got, ok)
		}
	}
	if _, ok := ParseType("not-a-type"); ok {
		t.Error("ParseType accepted nonsense")
	}
	if _, ok := ParseType(""); !ok {
		t.Error("ParseType rejected the empty filter")
	}
}
