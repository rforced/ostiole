package discovery

import (
	"fmt"
	"net/netip"
	"slices"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type seenClock struct{ t time.Time }

func (c *seenClock) now() time.Time          { return c.t }
func (c *seenClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newSeenClock() *seenClock               { return &seenClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)} }
func seenAnswer(records ...Record) *MDNS     { return &MDNS{Kind: KindAnswer, Records: records} }
func seenPTR(typ, target string, ttl uint32) Record {
	return Record{Section: 1, Name: typ, Type: dnsmessage.TypePTR, Class: 1, TTL: ttl, Target: target}
}

func seenSRV(name, target string, port uint16) Record {
	return Record{Section: 3, Name: name, Type: dnsmessage.TypeSRV, Class: 1, Flush: true, TTL: 120, Target: target, Port: port}
}

func seenAddr(a string, ttl uint32) Record {
	const name = "quill-7.local."
	t := dnsmessage.TypeA
	ip := netip.MustParseAddr(a)
	if ip.Is6() {
		t = dnsmessage.TypeAAAA
	}
	return Record{Section: 3, Name: name, Type: t, Class: 1, Flush: true, TTL: ttl, Addr: ip}
}

func seenTXT(name string) Record {
	return Record{Section: 3, Name: name, Type: dnsmessage.TypeTXT, Class: 1, TTL: 4500, Text: []string{"md=Glimmer"}}
}

const (
	castType = "_glimcast._tcp.local."
	parlour  = "Parlour Speaker._glimcast._tcp.local."
	kitchen  = "Kitchen Speaker._glimcast._tcp.local."
)

func TestSeenMDNS(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		messages [][]Record
		after    time.Duration
		want     []Announcement
	}{
		"one message gives one announcement": {
			messages: [][]Record{{
				seenPTR(castType, parlour, 4500), seenSRV(parlour, "quill-7.local.", 8009), seenTXT(parlour),
				seenAddr("fd00::7", 120), seenAddr("192.0.2.7", 120),
			}},
			want: []Announcement{{Type: "_glimcast._tcp", Name: "Parlour Speaker", Host: "quill-7.local", Port: 8009,
				Addresses: []string{"192.0.2.7", "fd00::7"}}},
		},
		"SRV in a later message updates": {
			messages: [][]Record{
				{seenPTR(castType, parlour, 4500)},
				{seenSRV(parlour, "quill-7.local.", 8009), seenAddr("192.0.2.7", 120)},
			},
			want: []Announcement{{Type: "_glimcast._tcp", Name: "Parlour Speaker", Host: "quill-7.local", Port: 8009,
				Addresses: []string{"192.0.2.7"}}},
		},
		"two instances share their host's addresses": {
			messages: [][]Record{
				{seenPTR(castType, parlour, 4500), seenSRV(parlour, "quill-7.local.", 8009)},
				{seenPTR("_tinkle._tcp.local.", "Parlour._tinkle._tcp.local.", 4500), seenSRV("Parlour._tinkle._tcp.local.", "Quill-7.local.", 7000)},
				{seenAddr("192.0.2.7", 120)},
			},
			want: []Announcement{
				{Type: "_glimcast._tcp", Name: "Parlour Speaker", Host: "quill-7.local", Port: 8009, Addresses: []string{"192.0.2.7"}},
				{Type: "_tinkle._tcp", Name: "Parlour", Host: "Quill-7.local", Port: 7000, Addresses: []string{"192.0.2.7"}},
			},
		},
		"goodbye removes the instance": {
			messages: [][]Record{
				{seenPTR(castType, parlour, 4500), seenPTR(castType, kitchen, 4500)},
				{seenPTR(castType, parlour, 0)},
			},
			want: []Announcement{{Type: "_glimcast._tcp", Name: "Kitchen Speaker"}},
		},
		"goodbye on an address removes only it": {
			messages: [][]Record{
				{seenPTR(castType, parlour, 4500), seenSRV(parlour, "quill-7.local.", 8009),
					seenAddr("192.0.2.7", 120), seenAddr("fd00::7", 120)},
				{seenAddr("fd00::7", 0)},
			},
			want: []Announcement{{Type: "_glimcast._tcp", Name: "Parlour Speaker", Host: "quill-7.local", Port: 8009,
				Addresses: []string{"192.0.2.7"}}},
		},
		"expiry prunes": {
			messages: [][]Record{{seenPTR(castType, parlour, 120), seenPTR(castType, kitchen, 4500)}},
			after:    121 * time.Second,
			want:     []Announcement{{Type: "_glimcast._tcp", Name: "Kitchen Speaker"}},
		},
		"expired addresses go and the instance stays": {
			messages: [][]Record{{seenPTR(castType, parlour, 4500), seenSRV(parlour, "quill-7.local.", 8009), seenAddr("192.0.2.7", 120)}},
			after:    121 * time.Second,
			want:     []Announcement{{Type: "_glimcast._tcp", Name: "Parlour Speaker", Host: "quill-7.local", Port: 8009}},
		},
		"escapes are undone": {
			messages: [][]Record{{seenPTR(castType, `Den\032Speaker\.2._glimcast._tcp.local.`, 4500)}},
			want:     []Announcement{{Type: "_glimcast._tcp", Name: "Den Speaker.2"}},
		},
		"enumeration and other records are ignored": {
			messages: [][]Record{{
				seenPTR("_services._dns-sd._udp.local.", castType, 4500),
				seenPTR("7.2.0.192.in-addr.arpa.", "quill-7.local.", 120),
				seenSRV(kitchen, "quill-7.local.", 8009),
				seenTXT(kitchen),
			}},
		},
		"a flushed address replaces an old one": {
			messages: [][]Record{
				{seenPTR(castType, parlour, 4500), seenSRV(parlour, "quill-7.local.", 8009), seenAddr("192.0.2.7", 120)},
				{seenAddr("192.0.2.8", 120)},
			},
			want: []Announcement{{Type: "_glimcast._tcp", Name: "Parlour Speaker", Host: "quill-7.local", Port: 8009,
				Addresses: []string{"192.0.2.8"}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newSeenClock()
			s := NewSeen(c.now)
			for _, records := range tc.messages {
				c.advance(2 * time.Second)
				s.ObserveMDNS("lan", seenAnswer(records...))
			}
			c.advance(tc.after)
			got := s.List()
			if len(got) != len(tc.want) {
				t.Fatalf("got %d announcements, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Protocol != "mdns" || g.Interface != "lan" || g.Type != w.Type || g.Name != w.Name ||
					g.Host != w.Host || g.Port != w.Port || !slices.Equal(g.Addresses, w.Addresses) {
					t.Errorf("announcement %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func TestSeenMDNSKeepsLastSeenAndExpiry(t *testing.T) {
	t.Parallel()
	c := newSeenClock()
	s := NewSeen(c.now)
	s.ObserveMDNS("lan", seenAnswer(seenPTR(castType, parlour, 1_000_000)))
	got := s.List()
	if len(got) != 1 || !got[0].LastSeen.Equal(c.t) || !got[0].Expires.Equal(c.t.Add(24*time.Hour)) {
		t.Fatalf("got %+v, want seen now and expiring in 24 h", got)
	}
}

func TestSeenSSDP(t *testing.T) {
	t.Parallel()
	const usn = "uuid:5e1f::urn:schemas-upnp-org:device:MediaRenderer:1"
	const st = "urn:schemas-upnp-org:device:MediaRenderer:1"
	src := netip.MustParseAddr("192.0.2.20")
	c := newSeenClock()
	s := NewSeen(c.now)

	s.ObserveSSDP("iot", src, &SSDP{Kind: KindAlive, NT: st, USN: usn, Location: "http://192.0.2.20/d.xml"})
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("alive gave %+v", got)
	}
	a := got[0]
	if a.Protocol != "ssdp" || a.Interface != "iot" || a.Type != st || a.Name != usn || a.Location != "http://192.0.2.20/d.xml" ||
		!slices.Equal(a.Addresses, []string{"192.0.2.20"}) || a.Expires.Sub(a.LastSeen) != 1800*time.Second {
		t.Fatalf("alive gave %+v", a)
	}

	c.advance(time.Minute)
	s.ObserveSSDP("iot", src, &SSDP{Kind: KindReply, ST: st, USN: usn, Location: "http://192.0.2.20/d.xml", MaxAge: 120})
	got = s.List()
	if len(got) != 1 || !got[0].LastSeen.Equal(c.t) || !got[0].Expires.Equal(c.t.Add(120*time.Second)) {
		t.Fatalf("reply gave %+v, want one entry seen now", got)
	}

	s.ObserveSSDP("iot", src, &SSDP{Kind: KindSearch, ST: "ssdp:all"})
	s.ObserveSSDP("iot", src, &SSDP{Kind: KindByebye, NT: st, USN: usn})
	if got = s.List(); len(got) != 0 {
		t.Fatalf("byebye left %+v", got)
	}
}

func TestSeenSSDPExpires(t *testing.T) {
	t.Parallel()
	c := newSeenClock()
	s := NewSeen(c.now)
	s.ObserveSSDP("iot", netip.MustParseAddr("192.0.2.20"), &SSDP{Kind: KindAlive, NT: "upnp:rootdevice", USN: "uuid:5e1f::upnp:rootdevice", MaxAge: 60})
	c.advance(61 * time.Second)
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expired entry kept: %+v", got)
	}
}

func TestSeenBoundEvictsOldest(t *testing.T) {
	t.Parallel()
	c := newSeenClock()
	s := NewSeen(c.now)
	src := netip.MustParseAddr("192.0.2.20")
	for i := range maxSeen + 1 {
		c.advance(time.Millisecond)
		s.ObserveMDNS("lan", seenAnswer(seenPTR(castType, fmt.Sprintf("Speaker %04d._glimcast._tcp.local.", i), 4500)))
		s.ObserveSSDP("lan", src, &SSDP{Kind: KindAlive, NT: "upnp:rootdevice", USN: fmt.Sprintf("uuid:%04d", i)})
	}
	var mdns, ssdp []string
	for _, a := range s.List() {
		if a.Protocol == "mdns" {
			mdns = append(mdns, a.Name)
		} else {
			ssdp = append(ssdp, a.Name)
		}
	}
	if len(mdns) != maxSeen || slices.Contains(mdns, "Speaker 0000") || !slices.Contains(mdns, "Speaker 2000") {
		t.Errorf("mdns kept %d, first %q", len(mdns), mdns[0])
	}
	if len(ssdp) != maxSeen || slices.Contains(ssdp, "uuid:0000") || !slices.Contains(ssdp, "uuid:2000") {
		t.Errorf("ssdp kept %d, first %q", len(ssdp), ssdp[0])
	}
}

func TestSeenListIsSorted(t *testing.T) {
	t.Parallel()
	c := newSeenClock()
	s := NewSeen(c.now)
	s.ObserveMDNS("lan", seenAnswer(seenPTR(castType, parlour, 4500), seenPTR("_alpha._tcp.local.", "Zed._alpha._tcp.local.", 4500), seenPTR(castType, kitchen, 4500)))
	s.ObserveMDNS("guest", seenAnswer(seenPTR(castType, parlour, 4500)))
	s.ObserveSSDP("iot", netip.MustParseAddr("192.0.2.20"), &SSDP{Kind: KindAlive, NT: "upnp:rootdevice", USN: "uuid:1"})
	var got []string
	for _, a := range s.List() {
		got = append(got, a.Interface+"|"+a.Type+"|"+a.Name)
	}
	want := []string{
		"guest|_glimcast._tcp|Parlour Speaker",
		"iot|upnp:rootdevice|uuid:1",
		"lan|_alpha._tcp|Zed",
		"lan|_glimcast._tcp|Kitchen Speaker",
		"lan|_glimcast._tcp|Parlour Speaker",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("List order\n got %q\nwant %q", got, want)
	}
}
