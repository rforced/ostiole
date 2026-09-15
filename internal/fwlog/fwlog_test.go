package fwlog

import (
	"encoding/binary"
	"testing"
	"time"
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
	cases := map[string][3]string{
		"ostiole:allow-lan: ":    {"allow-lan", "", "rule"},
		"ostiole:lan:drop: ":     {"", "lan", "zone-drop"},
		"ostiole:input:drop: ":   {"", "", "default-drop"},
		"ostiole:forward:drop: ": {"", "", "default-drop"},
		"something else":         {"", "", "other"},
		"":                       {"", "", "other"},
	}
	for in, want := range cases {
		id, zone, kind := ParsePrefix(in)
		if id != want[0] || zone != want[1] || kind != want[2] {
			t.Errorf("ParsePrefix(%q) = %q, %q, %q; want %v", in, id, zone, kind, want)
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
	if len(got) != 3 || got[0].Length != 3 || got[2].Length != 5 {
		t.Errorf("Recent = %v", lengths(got))
	}
	if got := r.Recent(2); len(got) != 2 || got[0].Length != 4 {
		t.Errorf("Recent(2) = %v", lengths(got))
	}
	if first := <-ch; first.Length != 1 {
		t.Errorf("subscriber got %d first", first.Length)
	}
	if r.Dropped() != 4 {
		t.Errorf("dropped = %d, want 4 (buffer of 1)", r.Dropped())
	}
}

func lengths(es []Entry) []int {
	out := make([]int, len(es))
	for i, e := range es {
		out[i] = e.Length
	}
	return out
}
