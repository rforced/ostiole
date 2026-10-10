package discovery

import (
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"

	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
)

// kcable joins this namespace to ns with a veth, near here and far there, both up and addressed.
func kcable(t *testing.T, near, nearAddr, far, farAddr string, ns *os.File) {
	t.Helper()
	if err := netlink.AddVeth(near, far); err != nil {
		t.Fatal(err)
	}
	if err := netlink.SetLinkNamespace(netnstest.Link(t, far).Index, int(ns.Fd())); err != nil {
		t.Fatal(err)
	}
	up := func(name, addr string) {
		l := netnstest.Link(t, name)
		if err := netlink.AddAddr(l.Index, netip.MustParsePrefix(addr)); err != nil {
			t.Fatal(err)
		}
		if err := netlink.SetLinkUp(l.Index); err != nil {
			t.Fatal(err)
		}
	}
	up(near, nearAddr)
	netnstest.Do(t, ns, func() { up(far, farAddr) })
}

// peer is a socket in a child namespace on its one link.
type peer struct {
	c   conn4
	ifi *net.Interface
}

func newPeer(t *testing.T, ns *os.File, link, address string, groups ...netip.Addr) peer {
	t.Helper()
	var p peer
	netnstest.Do(t, ns, func() {
		ifi, err := net.InterfaceByName(link)
		if err != nil {
			t.Fatal(err)
		}
		c, err := listen4(address, sockOpts{}, 64, 255)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.p.SetControlMessage(ipv4.FlagTTL|ipv4.FlagInterface|ipv4.FlagDst, true); err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if err := c.join(ifi, g); err != nil {
				t.Fatal(err)
			}
		}
		p = peer{c, ifi}
	})
	t.Cleanup(func() { _ = p.c.Close() })
	return p
}

func (p peer) send(t *testing.T, b []byte, to string) {
	t.Helper()
	if err := p.c.write(b, p.ifi.Index, netip.MustParseAddrPort(to)); err != nil {
		t.Fatal(err)
	}
}

type got struct {
	payload []byte
	src     netip.AddrPort
	ttl     int
}

// recv returns what arrives within d, false when nothing does.
func (p peer) recv(t *testing.T, d time.Duration) (got, bool) {
	t.Helper()
	b := make([]byte, maxDatagram)
	_ = p.c.p.SetReadDeadline(time.Now().Add(d))
	n, cm, src, err := p.c.p.ReadFrom(b)
	if err != nil {
		return got{}, false
	}
	g := got{payload: b[:n], src: addrPort(src)}
	if cm != nil {
		g.ttl = cm.TTL
	}
	return g, true
}

type sink struct {
	mu sync.Mutex
	ev []Event
}

func (s *sink) Add(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ev = append(s.ev, e)
}

func mdnsMessage(t *testing.T, id uint16, response bool, build func(b *dnsmessage.Builder)) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: response, Authoritative: response})
	b.EnableCompression()
	build(&b)
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func ptrQuestion(t *testing.T, qu bool) func(b *dnsmessage.Builder) {
	return func(b *dnsmessage.Builder) {
		class := dnsmessage.ClassINET
		if qu {
			class |= 1 << 15
		}
		if err := b.StartQuestions(); err != nil {
			t.Fatal(err)
		}
		if err := b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("_ipp._tcp.local."), Type: dnsmessage.TypePTR, Class: class}); err != nil {
			t.Fatal(err)
		}
	}
}

func printerAnswer(t *testing.T) func(b *dnsmessage.Builder) {
	return func(b *dnsmessage.Builder) {
		if err := b.StartAnswers(); err != nil {
			t.Fatal(err)
		}
		h := func(name string) dnsmessage.ResourceHeader {
			return dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Class: dnsmessage.ClassINET, TTL: 120}
		}
		err := b.PTRResource(h("_ipp._tcp.local."), dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("Lumen Jet._ipp._tcp.local.")})
		if err == nil {
			err = b.AResource(h("lumenjet.local."), dnsmessage.AResource{A: [4]byte{10, 0, 1, 2}})
		}
		if err == nil {
			err = b.AAAAResource(h("lumenjet.local."), dnsmessage.AAAAResource{AAAA: netip.MustParseAddr("fe80::1").As16()})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRelayKernel(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	clientNS, deviceNS := netnstest.NewNS(t), netnstest.NewNS(t)
	kcable(t, "r0", "10.0.0.1/24", "c0", "10.0.0.2/24", clientNS)
	kcable(t, "r1", "10.0.1.1/24", "d1", "10.0.1.2/24", deviceNS)

	ev := &sink{}
	r := New(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})), ev)
	cfg := Config{MDNS: true, SSDP: true, Links: []Link{{Name: "r0", Asks: true}, {Name: "r1", Answers: true}}, ReplyPorts: [2]int{61900, 61999}}
	r.Configure(cfg)
	go r.Run(t.Context())
	deadline := time.Now().Add(3 * time.Second)
	for !r.Status().Running {
		if time.Now().After(deadline) {
			t.Fatalf("relay not running: %+v", r.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := r.Status(); st.Problem != "" {
		t.Logf("problem: %s", st.Problem)
	}

	group4 := netip.MustParseAddr("224.0.0.251")
	client := newPeer(t, clientNS, "c0", "0.0.0.0:5353", group4)
	device := newPeer(t, deviceNS, "d1", "0.0.0.0:5353", group4)

	client.send(t, mdnsMessage(t, 0, false, ptrQuestion(t, true)), "224.0.0.251:5353")
	q, ok := device.recv(t, 2*time.Second)
	if !ok {
		t.Fatalf("query never reached the device; events %+v", ev.ev)
	}
	if q.src.Addr() != netip.MustParseAddr("10.0.1.1") || q.src.Port() != 5353 || q.ttl != 255 {
		t.Errorf("query at the device from %v TTL %d, want 10.0.1.1:5353 TTL 255", q.src, q.ttl)
	}
	var p dnsmessage.Parser
	if _, err := p.Start(q.payload); err != nil {
		t.Fatal(err)
	}
	if qq, err := p.Question(); err != nil || qq.Class&(1<<15) != 0 {
		t.Errorf("question %+v, %v: the QU bit should be clear", qq, err)
	}
	if back, ok := client.recv(t, 300*time.Millisecond); ok {
		t.Errorf("client heard %d bytes from %v after its own query", len(back.payload), back.src)
	}

	device.send(t, mdnsMessage(t, 0, true, printerAnswer(t)), "224.0.0.251:5353")
	a, ok := client.recv(t, 2*time.Second)
	if !ok {
		t.Fatalf("answer never reached the client; events %+v", ev.ev)
	}
	if a.src.Addr() != netip.MustParseAddr("10.0.0.1") {
		t.Errorf("answer at the client from %v, want 10.0.0.1", a.src)
	}
	m, err := ParseMDNS(a.payload, 5353)
	if err != nil {
		t.Fatal(err)
	}
	var hasA bool
	for _, rec := range m.Records {
		hasA = hasA || rec.Type == dnsmessage.TypeA
		if rec.Type == dnsmessage.TypeAAAA {
			t.Errorf("link-local AAAA %v crossed", rec.Addr)
		}
	}
	if !hasA {
		t.Error("the A record went missing")
	}

	legacy := newPeer(t, clientNS, "c0", "0.0.0.0:0")
	legacy.send(t, mdnsMessage(t, 0x4242, false, ptrQuestion(t, false)), "224.0.0.251:5353")
	lq, ok := device.recv(t, 2*time.Second)
	if !ok {
		t.Fatalf("legacy query never reached the device; events %+v", ev.ev)
	}
	if lq.src.Port() == 5353 {
		t.Fatalf("legacy query relayed from port 5353")
	}
	reply := mdnsMessage(t, 0, true, printerAnswer(t))
	copy(reply[:2], lq.payload[:2])
	device.send(t, reply, lq.src.String())
	lr, ok := legacy.recv(t, 2*time.Second)
	if !ok {
		t.Fatalf("legacy answer never reached the client; events %+v", ev.ev)
	}
	if lr.src != netip.MustParseAddrPort("10.0.0.1:5353") || lr.payload[0] != 0x42 || lr.payload[1] != 0x42 {
		t.Errorf("legacy answer from %v with ID % x, want 10.0.0.1:5353 and 42 42", lr.src, lr.payload[:2])
	}

	ssdpGroupAddr := netip.MustParseAddr("239.255.255.250")
	ssdpClient := newPeer(t, clientNS, "c0", "0.0.0.0:1900", ssdpGroupAddr)
	ssdpDevice := newPeer(t, deviceNS, "d1", "0.0.0.0:1900", ssdpGroupAddr)
	searcher := newPeer(t, clientNS, "c0", "0.0.0.0:0")
	searcher.send(t, []byte("M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 1\r\nST: ssdp:all\r\n\r\n"), "239.255.255.250:1900")
	search, ok := ssdpDevice.recv(t, 2*time.Second)
	if !ok {
		t.Fatalf("search never reached the device; events %+v", ev.ev)
	}
	if search.src.Addr() != netip.MustParseAddr("10.0.1.1") || search.src.Port() < 61902 {
		t.Errorf("search at the device from %v", search.src)
	}
	ssdpDevice.send(t, []byte("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nST: upnp:rootdevice\r\nUSN: uuid:lumen-1::upnp:rootdevice\r\nLOCATION: http://10.0.1.2:8080/d.xml\r\n\r\n"), search.src.String())
	sr, ok := searcher.recv(t, 2*time.Second)
	if !ok || sr.src.Addr() != netip.MustParseAddr("10.0.0.1") || !strings.Contains(string(sr.payload), "uuid:lumen-1") {
		t.Fatalf("search reply at the client: %v from %v; events %+v", ok, sr.src, ev.ev)
	}
	ssdpDevice.send(t, []byte("NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nNT: upnp:rootdevice\r\nNTS: ssdp:alive\r\nUSN: uuid:lumen-1::upnp:rootdevice\r\nLOCATION: http://10.0.1.2:8080/d.xml\r\nCACHE-CONTROL: max-age=1800\r\n\r\n"), "239.255.255.250:1900")
	n, ok := ssdpClient.recv(t, 2*time.Second)
	if !ok || n.src.Addr() != netip.MustParseAddr("10.0.0.1") || !strings.HasPrefix(string(n.payload), "NOTIFY") {
		t.Fatalf("NOTIFY at the client: %v from %v; events %+v", ok, n.src, ev.ev)
	}
	if len(r.Announcements()) == 0 {
		t.Error("nothing announced")
	}

	cfg.Links = []Link{{Name: "r0", Asks: true}, {Name: "r1", Asks: true}}
	r.Configure(cfg)
	client.send(t, mdnsMessage(t, 0, false, ptrQuestion(t, false)), "224.0.0.251:5353")
	if _, ok := device.recv(t, 500*time.Millisecond); ok {
		t.Error("a query crossed to an interface that only asks")
	}
	device.send(t, mdnsMessage(t, 0, true, printerAnswer(t)), "224.0.0.251:5353")
	if _, ok := client.recv(t, 500*time.Millisecond); ok {
		t.Error("an answer crossed from an interface that only asks")
	}
}
