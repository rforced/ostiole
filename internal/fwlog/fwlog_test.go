package fwlog

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

func ipv4Packet(proto byte, src, dst [4]byte, l4 []byte) []byte {
	h := make([]byte, 20)
	h[0] = 0x45
	binary.BigEndian.PutUint16(h[2:4], uint16(20+len(l4)))
	h[8] = 64
	h[9] = proto
	copy(h[12:16], src[:])
	copy(h[16:20], dst[:])
	return append(h, l4...)
}

func tcpHeader(sport, dport uint16, flags byte) []byte {
	b := make([]byte, 20)
	binary.BigEndian.PutUint16(b[0:2], sport)
	binary.BigEndian.PutUint16(b[2:4], dport)
	b[12] = 5 << 4
	b[13] = flags
	return b
}

func TestDecode(t *testing.T) {
	t.Parallel()
	var e Entry
	Decode(&e, ipv4Packet(6, [4]byte{192, 168, 1, 10}, [4]byte{10, 0, 0, 1}, tcpHeader(51000, 443, 0x02)))
	if e.Family != "ipv4" || e.Proto != "tcp" || e.Src != "192.168.1.10" || e.Dst != "10.0.0.1" || e.SrcPort != 51000 || e.DstPort != 443 || e.TCPFlags != "SYN" {
		t.Errorf("tcp entry = %+v", e)
	}

	e = Entry{}
	udp := make([]byte, 8)
	binary.BigEndian.PutUint16(udp[0:2], 5353)
	binary.BigEndian.PutUint16(udp[2:4], 53)
	Decode(&e, ipv4Packet(17, [4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, udp))
	if e.Proto != "udp" || e.DstPort != 53 {
		t.Errorf("udp entry = %+v", e)
	}

	e = Entry{}
	Decode(&e, ipv4Packet(1, [4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}, []byte{8, 0, 0, 0}))
	if e.Proto != "icmp" || e.ICMPType != 8 {
		t.Errorf("icmp entry = %+v", e)
	}

	// IPv6 with a hop-by-hop extension header before TCP.
	v6 := make([]byte, 40)
	v6[0] = 0x60
	v6[6] = 0 // hop-by-hop
	copy(v6[8:24], []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	copy(v6[24:40], []byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2})
	hbh := []byte{6, 0, 0, 0, 0, 0, 0, 0} // next=tcp, len 0 => 8 bytes
	e = Entry{}
	Decode(&e, append(append(v6, hbh...), tcpHeader(1000, 22, 0x10)...))
	if e.Family != "ipv6" || e.Proto != "tcp" || e.Src != "2001:db8::1" || e.DstPort != 22 || e.TCPFlags != "ACK" {
		t.Errorf("ipv6 entry = %+v", e)
	}

	e = Entry{}
	Decode(&e, []byte{0x45, 0x00})
	if e.Family != "ipv4" || e.Src != "" {
		t.Errorf("truncated = %+v", e)
	}
	e = Entry{}
	Decode(&e, ipv4Packet(47, [4]byte{1, 1, 1, 1}, [4]byte{2, 2, 2, 2}, nil))
	if e.Proto != "gre" {
		t.Errorf("gre = %+v", e)
	}
}

func TestParsePrefix(t *testing.T) {
	t.Parallel()
	// id, zone, kind, action.
	cases := map[string][4]string{
		"ostiole:r:allow-lan:accept: ":     {"allow-lan", "", "rule", "accept"},
		"ostiole:r:block-guest:drop: ":     {"block-guest", "", "rule", "drop"},
		"ostiole:r:no-smb:reject: ":        {"no-smb", "", "rule", "reject"},
		"ostiole:z:lan:drop: ":             {"", "lan", "zone-drop", "drop"},
		"ostiole:c:input:drop: ":           {"", "", "default-drop", "drop"},
		"ostiole:c:forward:drop: ":         {"", "", "default-drop", "drop"},
		"ostiole:c:input:block-private: ":  {"", "", "block-private", "drop"},
		"ostiole:c:forward:block-bogons: ": {"", "", "block-bogons", "drop"},
		// The drops Ostiole makes on its own account. They name no zone;
		// the interface the kernel reports is what places them.
		"ostiole:s:block-dot:drop: ":         {"", "", "block-dot", "drop"},
		"ostiole:s:block-doh:drop: ":         {"", "", "block-doh", "drop"},
		"ostiole:s:protect-scanner:drop: ":   {"", "", "protect-scanner", "drop"},
		"ostiole:s:protect-synflood:drop: ":  {"", "", "protect-synflood", "drop"},
		"ostiole:s:protect-icmpflood:drop: ": {"", "", "protect-icmpflood", "drop"},
		// A zone named the same as a rule stays unambiguous, which is the
		// whole reason the shapes carry a tag.
		"ostiole:r:lan:drop: ": {"lan", "", "rule", "drop"},
		"ostiole:z:lan:drop":   {"", "lan", "zone-drop", "drop"},
		// Nonsense in a tagged position is not guessed at.
		"ostiole:r:web:mangle: ":    {"", "", "other", ""},
		"ostiole:x:web:drop: ":      {"", "", "other", ""},
		"ostiole:r::accept: ":       {"", "", "other", ""},
		"ostiole:s:block-dot:log: ": {"", "", "other", ""},
		"ostiole:s:invented:drop: ": {"", "", "other", ""},
		"something else":            {"", "", "other", ""},
		"":                          {"", "", "other", ""},
	}
	for in, want := range cases {
		id, zone, kind, action := ParsePrefix(in)
		if got := [4]string{id, zone, kind, action}; got != want {
			t.Errorf("ParsePrefix(%q) = %v; want %v", in, got, want)
		}
	}
}

// TestParsePrefixLegacy covers the shapes a router still runs between the
// upgrade and its next apply: it boots the stored ruleset, which the old
// binary rendered.
func TestParsePrefixLegacy(t *testing.T) {
	t.Parallel()
	cases := map[string][4]string{
		"ostiole:allow-lan: ":            {"allow-lan", "", "rule", ""},
		"ostiole:lan:drop: ":             {"", "lan", "zone-drop", "drop"},
		"ostiole:input:drop: ":           {"", "", "default-drop", "drop"},
		"ostiole:forward:drop: ":         {"", "", "default-drop", "drop"},
		"ostiole:input:block-private: ":  {"", "", "block-private", "drop"},
		"ostiole:forward:block-bogons: ": {"", "", "block-bogons", "drop"},
	}
	for in, want := range cases {
		id, zone, kind, action := ParsePrefix(in)
		if got := [4]string{id, zone, kind, action}; got != want {
			t.Errorf("ParsePrefix(%q) = %v; want %v", in, got, want)
		}
	}
}

func TestRing(t *testing.T) {
	t.Parallel()
	r := NewRing(3)
	if got := r.Recent(10); len(got) != 0 {
		t.Fatalf("empty ring returned %v", got)
	}
	ch, cancel := r.Subscribe(1)
	defer cancel()
	for i := 1; i <= 5; i++ {
		r.Add(Entry{Length: i, Time: time.Now()})
	}

	got := r.Recent(10)
	if len(got) != 3 || got[0].Length != 5 || got[2].Length != 3 {
		t.Errorf("Recent = %v", lengths(got))
	}
	if got := r.Recent(2); len(got) != 2 || got[0].Length != 5 || got[1].Length != 4 {
		t.Errorf("Recent(2) = %v", lengths(got))
	}
	if first := <-ch; first.Length != 1 {
		t.Errorf("subscriber got %d first", first.Length)
	}
	if r.Dropped() != 4 {
		t.Errorf("dropped = %d, want 4 (buffer of 1)", r.Dropped())
	}
}

// A ceiling is configuration, so it changes under a running log. Growing
// keeps everything; shrinking keeps the newest that fit, which is what the
// page is looking at.
func TestRingConfigure(t *testing.T) {
	t.Parallel()
	r := NewRing(3)
	for i := 1; i <= 5; i++ {
		r.Add(Entry{Length: i})
	}
	r.Configure(6)
	if got := lengths(r.Recent(0)); len(got) != 3 || got[0] != 5 {
		t.Errorf("growing lost entries: %v", got)
	}
	for i := 6; i <= 9; i++ {
		r.Add(Entry{Length: i})
	}
	if got := lengths(r.Recent(0)); len(got) != 6 || got[0] != 9 || got[5] != 4 {
		t.Errorf("after growing = %v, want 9..4", got)
	}
	r.Configure(2)
	if got := lengths(r.Recent(0)); len(got) != 2 || got[0] != 9 || got[1] != 8 {
		t.Errorf("shrinking kept %v, want the newest two", got)
	}
	if r.Size() != 2 {
		t.Errorf("size = %d, want 2", r.Size())
	}
	// Adding after a shrink evicts rather than growing back.
	r.Add(Entry{Length: 10})
	if got := lengths(r.Recent(0)); len(got) != 2 || got[0] != 10 || got[1] != 9 {
		t.Errorf("after shrinking = %v, want 10, 9", got)
	}
}

// A big ceiling costs nothing until the packets arrive: the ring doubles
// into it. Allocating a million entries the moment one is configured would
// cost 350 MB on a router that may never see them.
func TestRingGrowsIntoItsCeiling(t *testing.T) {
	t.Parallel()
	r := NewRing(1_000_000)
	if n := r.places(); n != initialRing {
		t.Fatalf("a fresh ring holds %d places, want %d", n, initialRing)
	}
	for i := range initialRing + 1 {
		r.Add(Entry{Length: i})
	}
	if n := r.places(); n != 2*initialRing {
		t.Errorf("a full ring grew to %d places, want %d", n, 2*initialRing)
	}
	if got := r.Recent(0); len(got) != initialRing+1 {
		t.Errorf("growing lost entries: held %d", len(got))
	}
}

// places is how many entries the ring has room for right now, which is not
// its ceiling until it has filled.
func (r *Ring) places() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ring)
}

func lengths(es []Entry) []int {
	out := make([]int, len(es))
	for i, e := range es {
		out[i] = e.Length
	}
	return out
}

// The ceiling follows the configuration the router is really running, so a
// reverted apply takes its ring size with it.
func TestWatcherFollowsEffective(t *testing.T) {
	t.Parallel()
	r := NewRing(10)
	cfg := &model.Config{}
	cfg.System.Management.FirewallLog.Entries = 5000
	source := cfg
	w := &Watcher{Ring: r, Source: func() *model.Config { return source }}
	w.tick()
	if got := r.Size(); got != 5000 {
		t.Errorf("size = %d, want 5000", got)
	}
	// A revert leaves nothing in force; the default is what is left.
	source = nil
	w.tick()
	if got := r.Size(); got != model.DefaultFirewallLogEntries {
		t.Errorf("size = %d, want the default %d", got, model.DefaultFirewallLogEntries)
	}
}
