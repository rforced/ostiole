package discovery

import (
	"encoding/binary"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"
)

func TestDestinations(t *testing.T) {
	t.Parallel()
	links := []Link{
		{Name: "lan", Asks: true, Answers: true},
		{Name: "iot", Answers: true},
		{Name: "guest", Asks: true},
	}
	cases := []struct {
		kind Kind
		from string
		want []string
	}{
		{KindQuery, "lan", []string{"iot"}},
		{KindLegacy, "guest", []string{"lan", "iot"}},
		{KindProbe, "lan", []string{"iot"}},
		{KindSearch, "guest", []string{"lan", "iot"}},
		{KindQuery, "iot", nil},
		{KindAnswer, "iot", []string{"lan", "guest"}},
		{KindAlive, "lan", []string{"guest"}},
		{KindByebye, "iot", []string{"lan", "guest"}},
		{KindUpdate, "guest", nil},
		{KindAnswer, "guest", nil},
		{KindReply, "iot", nil},
		{KindQuery, "wan", nil},
	}
	for _, c := range cases {
		if got := destinations(c.kind, c.from, links); !slices.Equal(got, c.want) {
			t.Errorf("%s from %s goes to %v, want %v", c.kind, c.from, got, c.want)
		}
	}
}

func TestDedupeWindow(t *testing.T) {
	t.Parallel()
	d := dedupe{}
	now := time.Unix(1000, 0)
	p := []byte("payload")
	if d.seen(p, 1, now) {
		t.Fatal("first copy dropped")
	}
	if !d.seen(p, 1, now.Add(99*time.Millisecond)) {
		t.Error("echo within 100 ms passed")
	}
	if d.seen(p, 2, now.Add(10*time.Millisecond)) {
		t.Error("same payload on another interface dropped")
	}
	if d.seen(p, 1, now.Add(250*time.Millisecond)) {
		t.Error("a repeat at 250 ms dropped")
	}
	d.sweep(now.Add(time.Second))
	if len(d) != 0 {
		t.Errorf("sweep left %d", len(d))
	}
}

func TestBucket(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	b := newBucket(now)
	for i := range linkCap {
		if !b.take(now) {
			t.Fatalf("packet %d refused inside the burst", i)
		}
	}
	if b.take(now) {
		t.Error("packet past the burst passed")
	}
	if !b.take(now.Add(2 * time.Millisecond)) {
		t.Error("refill after 2 ms refused")
	}
	later := now.Add(time.Hour)
	for range linkCap {
		b.take(later)
	}
	if b.take(later) {
		t.Error("an idle hour gave more than one burst")
	}
}

func TestSlotTable(t *testing.T) {
	t.Parallel()
	var tbl slotTable
	now := time.Unix(1000, 0)
	client := netip.MustParseAddrPort("10.0.0.2:40000")
	n, ok := tbl.take(0x1234, client, "lan", now)
	if !ok {
		t.Fatal("no slot in an empty table")
	}
	s, ok := tbl.lookup(n, now.Add(time.Second))
	if !ok || s.id != 0x1234 || s.client != client || s.link != "lan" {
		t.Fatalf("lookup = %+v, %v", s, ok)
	}
	if _, ok := tbl.lookup(n, now.Add(time.Second)); !ok {
		t.Error("a second answer found no slot")
	}
	if _, ok := tbl.lookup(n, now.Add(slotLife)); ok {
		t.Error("slot answered past its life")
	}
	if _, ok := tbl.lookup(slotCount+5, now); ok {
		t.Error("an ID past the table found a slot")
	}

	full := slotTable{}
	for i := range slotCount {
		if _, ok := full.take(uint16(i), client, "lan", now); !ok {
			t.Fatalf("slot %d refused", i)
		}
	}
	if _, ok := full.take(1, client, "lan", now.Add(time.Second)); ok {
		t.Error("a full table gave a slot")
	}
	if _, ok := full.take(1, client, "lan", now.Add(slotLife)); !ok {
		t.Error("expired slots were not reused")
	}
}

func TestProxyTable(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	tbl := newProxyTable(100, 102)
	a := netip.MustParseAddrPort("10.0.0.2:1901")
	b := netip.MustParseAddrPort("10.0.0.3:1901")
	c := netip.MustParseAddrPort("10.0.0.4:1901")
	if got := tbl.free(); !slices.Equal(got, []int{100, 101, 102}) {
		t.Fatalf("free = %v", got)
	}
	tbl.add(&proxy{port: 100, client: a, expires: now.Add(searchLife(4))})
	tbl.add(&proxy{port: 101, client: b, expires: now.Add(searchLife(0))})
	if got := tbl.free(); !slices.Equal(got, []int{102}) {
		t.Fatalf("free = %v", got)
	}
	tbl.add(&proxy{port: 102, client: c, expires: now.Add(searchLife(99))})
	if got := tbl.free(); len(got) != 0 {
		t.Fatalf("free in a full range = %v", got)
	}
	gone := tbl.expired(now.Add(2 * time.Second))
	if len(gone) != 1 || gone[0].client != b {
		t.Fatalf("expired at 2 s = %v", gone)
	}
	if got := tbl.free(); !slices.Equal(got, []int{101}) {
		t.Errorf("freed port not offered: %v", got)
	}
	if gone := tbl.expired(now.Add(5 * time.Second)); len(gone) != 1 || gone[0].client != a {
		t.Errorf("MX 4 proxy at 5 s: %v", gone)
	}
	if gone := tbl.expired(now.Add(6 * time.Second)); len(gone) != 1 || gone[0].client != c {
		t.Errorf("MX over 5 proxy at 6 s: %v", gone)
	}
	if got := newProxyTable(0, -1).free(); got != nil {
		t.Errorf("no range gave ports %v", got)
	}
}

func TestConfigureEqualIsNoop(t *testing.T) {
	t.Parallel()
	r := New(nil, nil)
	c := Config{MDNS: true, Links: []Link{{Name: "a", Asks: true}}, Services: []string{"_ipp._tcp"}}
	r.Configure(c)
	r.running = true
	s := &session{proxies: newProxyTable(0, -1)}
	r.sess = s
	r.Configure(Config{MDNS: true, Links: []Link{{Name: "a", Asks: true}}, Services: []string{"_ipp._tcp"}})
	if r.sess != s || s.closed {
		t.Fatal("an equal Config restarted the relay")
	}
	r.Configure(Config{MDNS: true, Links: []Link{{Name: "b", Asks: true}}})
	if !s.closed || r.sess != nil {
		t.Error("a different Config left the old sockets open")
	}
}

// fakeConn records what is written to it.
type fakeConn struct{ sent []sent }

type sent struct {
	payload []byte
	ifindex int
	to      netip.AddrPort
}

func (f *fakeConn) read([]byte) (int, int, netip.AddrPort, netip.Addr, error) {
	return 0, 0, netip.AddrPort{}, netip.Addr{}, net.ErrClosed
}

func (f *fakeConn) write(b []byte, ifindex int, to netip.AddrPort) error {
	f.sent = append(f.sent, sent{slices.Clone(b), ifindex, to})
	return nil
}

func (f *fakeConn) join(*net.Interface, netip.Addr) error { return nil }
func (f *fakeConn) Close() error                          { return nil }

type events []Event

func (e *events) Add(ev Event) { *e = append(*e, ev) }

// testRelay is a relay with fake sockets on lan (index 1, asks) and iot (index 2, answers).
func testRelay(services ...string) (*Relay, *session, *events, *fakeConn, *fakeConn) {
	evs := &events{}
	r := New(nil, evs)
	now := time.Unix(1000, 0)
	r.Now = func() time.Time { return now }
	m4, l4 := &fakeConn{}, &fakeConn{}
	lan := &rlink{Link: Link{Name: "lan", Asks: true}, index: 1, prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}
	iot := &rlink{Link: Link{Name: "iot", Answers: true}, index: 2, prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.1.0/24")}}
	s := &session{
		filter:  NewFilter(services),
		links:   []*rlink{lan, iot},
		byIndex: map[int]*rlink{1: lan, 2: iot},
		byName:  map[string]*rlink{"lan": lan, "iot": iot},
		own:     map[netip.Addr]bool{netip.MustParseAddr("10.0.0.1"): true, netip.MustParseAddr("10.0.1.1"): true},
		m4:      m4,
		l4:      l4,
		dedupe:  dedupe{},
		buckets: map[string]*bucket{},
		quiet:   map[quietKey]time.Time{},
		proxies: newProxyTable(0, -1),
	}
	r.sess = s
	return r, s, evs, m4, l4
}

// dottedQuery is a query whose instance label holds a dot, which dnsmessage refuses.
func dottedQuery(id uint16, qu bool) []byte {
	p := make([]byte, 12)
	binary.BigEndian.PutUint16(p, id)
	binary.BigEndian.PutUint16(p[4:], 1)
	for _, l := range []string{"Printer v2.0", "_ipp", "_tcp", "local"} {
		p = append(p, byte(len(l)))
		p = append(p, l...)
	}
	class := uint16(1)
	if qu {
		class |= 0x8000
	}
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(append(p, 0), 16), class)
}

func TestHeaderKind(t *testing.T) {
	t.Parallel()
	q := dottedQuery(0, false)
	answer := slices.Clone(q)
	answer[2] |= 0x80
	probe := slices.Clone(q)
	binary.BigEndian.PutUint16(probe[8:], 1)
	cases := []struct {
		payload []byte
		port    int
		want    Kind
		ok      bool
	}{
		{q, 5353, KindQuery, true},
		{q, 40000, KindLegacy, true},
		{answer, 5353, KindAnswer, true},
		{probe, 5353, KindProbe, true},
		{q[:11], 5353, "", false},
	}
	for i, c := range cases {
		if got, ok := headerKind(c.payload, c.port); got != c.want || ok != c.ok {
			t.Errorf("case %d: %q, %v; want %q, %v", i, got, ok, c.want, c.ok)
		}
	}
}

func TestUnparsableRelayedOnlyWithoutFilter(t *testing.T) {
	t.Parallel()
	if _, err := ParseMDNS(dottedQuery(0, false), 5353); err == nil {
		t.Skip("dnsmessage now reads a dotted label")
	}
	src := netip.MustParseAddrPort("10.0.0.2:5353")
	group := netip.MustParseAddr("224.0.0.251")

	r, s, evs, m4, _ := testRelay()
	r.mdnsPacket(s, false, dottedQuery(0, true), 1, src, group)
	if len(m4.sent) != 1 || m4.sent[0].ifindex != 2 {
		t.Fatalf("sent %+v, want one copy to iot", m4.sent)
	}
	if want := dottedQuery(0, false); !slices.Equal(m4.sent[0].payload, want) {
		t.Errorf("relayed % x\nwant      % x (QU cleared, rest verbatim)", m4.sent[0].payload, want)
	}
	if len(*evs) != 1 || (*evs)[0].Dropped != "" || !slices.Equal((*evs)[0].To, []string{"iot"}) {
		t.Errorf("events %+v", *evs)
	}

	r, s, evs, m4, _ = testRelay("_ipp._tcp")
	r.mdnsPacket(s, false, dottedQuery(0, false), 1, src, group)
	if len(m4.sent) != 0 || len(*evs) != 1 || (*evs)[0].Dropped != "parse" {
		t.Errorf("with a filter: sent %d, events %+v", len(m4.sent), *evs)
	}
}

func TestLegacyUnparsableUsesASlot(t *testing.T) {
	t.Parallel()
	r, s, evs, m4, l4 := testRelay()
	client := netip.MustParseAddrPort("10.0.0.2:40000")
	r.mdnsPacket(s, false, dottedQuery(0xbeef, false), 1, client, netip.MustParseAddr("224.0.0.251"))
	if len(l4.sent) != 1 {
		t.Fatalf("legacy socket sent %d, events %+v", len(l4.sent), *evs)
	}
	slotID := binary.BigEndian.Uint16(l4.sent[0].payload)
	reply := slices.Clone(l4.sent[0].payload)
	reply[2] |= 0x80
	r.legacyReply(s, false, reply, 2, netip.MustParseAddrPort("10.0.1.2:5353"), netip.MustParseAddr("10.0.1.1"))
	if len(m4.sent) != 1 {
		t.Fatalf("reply not sent to the client, events %+v", *evs)
	}
	got := m4.sent[0]
	if got.to != client || got.ifindex != 1 || binary.BigEndian.Uint16(got.payload) != 0xbeef {
		t.Errorf("reply to %v on %d with ID %#x (slot %d), want %v on 1 with 0xbeef", got.to, got.ifindex, binary.BigEndian.Uint16(got.payload), slotID, client)
	}
}
