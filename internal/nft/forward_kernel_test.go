package nft

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
	"ostiole/internal/policy"
)

// A port forward is no way around the zone it arrives on. The kernel runs
// the ruleset, a SYN for the forwarded port comes in on the WAN, and the
// counters say which rule decided: a source the WAN zone drops is dropped
// there, and anyone else reaches the forward.
func TestPortForwardsPassThroughTheZoneInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}

	cfg := loadConfig(t, "testdata/minimal.json")
	cfg.Rules = append(cfg.Rules, model.Rule{
		ID: "drop-abusers", Enabled: true, Zone: "wan", Action: model.ActionDrop, Protocol: model.ProtocolAny,
		Source: model.Endpoint{Addresses: []string{"203.0.113.0/24"}},
	})
	cfg.NAT.PortForwards = []model.PortForward{
		{ID: "pf-web", Enabled: true, Zone: "wan", Protocol: model.ProtocolTCP, Ports: []string{"8080"}, Target: "192.168.1.10"},
	}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// eth0 is the WAN, fed from its veth peer. eth1 is the LAN, a dummy
	// that drops what it is given once the forward hook has seen it.
	if err := netlink.AddVeth("eth0", "peer0"); err != nil {
		t.Fatalf("add veth: %v", err)
	}
	wan, err := netlink.LinkByName("eth0")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := netlink.LinkByName("peer0")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []netlink.Link{wan, peer} {
		if err := netlink.SetLinkUp(l.Index); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.AddAddr(wan.Index, netip.MustParsePrefix("198.51.100.2/24")); err != nil {
		t.Fatal(err)
	}
	netnstest.Dummy(t, "eth1", "192.168.1.1/24")
	for path, value := range map[string]string{
		"/proc/sys/net/ipv4/ip_forward":          "1",
		"/proc/sys/net/ipv4/conf/all/rp_filter":  "0",
		"/proc/sys/net/ipv4/conf/eth0/rp_filter": "0",
	} {
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	defer syscall.Close(fd)
	to := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_IP), Ifindex: peer.Index, Halen: 6}
	copy(to.Addr[:], wan.HardwareAddr)
	for _, src := range []string{"203.0.113.9", "192.0.2.9"} {
		frame := synFrame(wan.HardwareAddr, peer.HardwareAddr,
			net.ParseIP(src), net.ParseIP("198.51.100.2"), 40000, 8080)
		if err := syscall.Sendto(fd, frame, 0, to); err != nil {
			t.Fatalf("send from %s: %v", src, err)
		}
	}

	// The peer's frames are taken in by the other end asynchronously. Both
	// are translated before the filter decides, so the forward counts two.
	want := map[string]uint64{"drop-abusers": 1, "zone_wan/port-forwards": 1, "pf-web": 2}
	var got Counters
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, err := x.ListTableJSON(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got, err = ParseCounters(raw); err != nil {
			t.Fatal(err)
		}
		if got["zone_wan/port-forwards"].Packets+got["drop-abusers"].Packets >= 2 {
			break
		}
	}
	for key, n := range want {
		if got[key].Packets != n {
			t.Errorf("%s counted %d packets, want %d", key, got[key].Packets, n)
		}
	}
}

// A client behind a PPPoE line offers the segment size of its own 1500-byte
// Ethernet. The kernel runs the ruleset, the client's SYN leaves by the
// line, and the size it carries out is what fits in the line's 1492.
func TestPPPoEClampsTheSegmentSizeInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	ruleset, err := Render(loadConfig(t, "testdata/pppoe.json"))
	if err != nil {
		t.Fatal(err)
	}

	// eth1 is the LAN, fed from its veth peer. ppp0 stands in for the line:
	// a dummy at the PPPoE MTU, which drops what it is given once a packet
	// socket has seen it leave.
	if err := netlink.AddVeth("eth1", "peer1"); err != nil {
		t.Fatalf("add veth: %v", err)
	}
	lan := netnstest.Link(t, "eth1")
	peer := netnstest.Link(t, "peer1")
	for _, l := range []netlink.Link{lan, peer} {
		if err := netlink.SetLinkUp(l.Index); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.AddAddr(lan.Index, netip.MustParsePrefix("192.168.1.1/24")); err != nil {
		t.Fatal(err)
	}
	line := netnstest.Dummy(t, "ppp0", "203.0.113.2/24")
	if err := netlink.SetLinkMTU(line.Index, model.PPPoEMTU); err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddRoute(netlink.Route{LinkIndex: line.Index, Gw: net.ParseIP("203.0.113.1")}); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		"/proc/sys/net/ipv4/ip_forward":         "1",
		"/proc/sys/net/ipv4/conf/all/rp_filter": "0",
	} {
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := (&Exec{}).Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}

	out, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	defer syscall.Close(out)
	if err := syscall.Bind(out, &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ALL), Ifindex: line.Index}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.SetsockoptTimeval(out, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Sec: 3}); err != nil {
		t.Fatal(err)
	}

	in, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	defer syscall.Close(in)
	to := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_IP), Ifindex: peer.Index, Halen: 6}
	copy(to.Addr[:], lan.HardwareAddr)
	mss1460 := []byte{2, 4, 0x05, 0xb4}
	frame := synFrame(lan.HardwareAddr, peer.HardwareAddr,
		net.ParseIP("192.168.1.10"), net.ParseIP("198.51.100.7"), 40000, 443, mss1460...)
	if err := syscall.Sendto(in, frame, 0, to); err != nil {
		t.Fatalf("send: %v", err)
	}

	buf := make([]byte, 2048)
	for {
		n, _, err := syscall.Recvfrom(out, buf, 0)
		if err != nil {
			t.Fatalf("no SYN left by ppp0: %v", err)
		}
		mss, ok := synMSS(buf[:n], 443)
		if !ok {
			continue
		}
		if want := model.PPPoEMTU - 40; mss != want {
			t.Errorf("SYN left with MSS %d, want %d", mss, want)
		}
		return
	}
}

// A group set to block holds its traffic to its own line before the daemon
// has routed it, as at boot. With no ip rules, the SYN the kiosk rule marks
// for wan2_only takes the main table towards eth0, and the kill switch drops
// it there. After one pass of the policy installer the next SYN leaves by
// the group's line.
func TestABlockingGroupHoldsItsTrafficBeforeItIsRoutedInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	cfg := loadConfig(t, "testdata/policy.json")
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// eth3 is the guest network, fed from its veth peer. eth0 holds the main
	// table's default route and eth1 is the group's line: dummies, which drop
	// what they are given once a packet socket has seen it leave.
	guest, peer := vethPair(t, "eth3", "peer3", "192.168.9.1/24")
	wan := netnstest.Dummy(t, "eth0", "203.0.113.2/24")
	line := netnstest.Dummy(t, "eth1", "198.51.100.2/24")
	if err := netlink.AddRoute(netlink.Route{LinkIndex: wan.Index, Gw: net.ParseIP("203.0.113.1")}); err != nil {
		t.Fatal(err)
	}
	forwardIPv4(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	outWAN := capture(t, wan)
	outLine := capture(t, line)
	toSite := func(s tcpSegment) bool { return s.dport == 443 }

	send := func(sport uint16) {
		sendFrame(t, peer, guest, tcpFrame(guest.HardwareAddr, peer.HardwareAddr,
			net.ParseIP("192.168.9.50"), net.ParseIP("198.18.0.7"), sport, 443, tcpSYN, 1, 0))
	}
	send(40000)
	if n := waitForCounter(ctx, t, x, "forward/kill-switch:wan2_only", 1); n != 1 {
		t.Fatalf("the kill switch counted %d packets, want 1", n)
	}
	if s, ok := nextSegment(t, outWAN, toSite, 300*time.Millisecond); ok {
		t.Errorf("%+v left by the WAN", s)
	}

	hops := map[string]policy.Hop{
		"backup": {Gateway: "backup", Address: "198.51.100.1", Interface: "eth1", Online: true},
	}
	if err := policy.NewInstaller(slog.New(slog.DiscardHandler)).Sync(policy.Plan(cfg, hops)); err != nil {
		t.Fatal(err)
	}
	send(40001)
	if _, ok := nextSegment(t, outLine, toSite, 3*time.Second); !ok {
		t.Error("after the installer's pass, no SYN left by the group's line")
	}
	if s, ok := nextSegment(t, outWAN, toSite, 300*time.Millisecond); ok {
		t.Errorf("%+v left by the WAN", s)
	}
}

// Only a connection's first packet picks a gateway. A port forward's
// connection comes in by the WAN with no mark, and the answer of the server
// behind it matches the rule that sends the server out the backup line:
// marked, it would leave there with the WAN's address on it. It leaves by
// the WAN the connection came in on.
func TestAPortForwardAnswersByTheWANItCameInOnInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	cfg := loadConfig(t, "testdata/minimal.json")
	cfg.Zones = append(cfg.Zones, model.Zone{Name: "wan2", External: true})
	cfg.Interfaces = append(cfg.Interfaces, model.Interface{
		Name: "eth2", Zone: "wan2", Enabled: true,
		IPv4: model.IPv4{Mode: model.AddrStatic, Address: "203.0.113.2/24", Gateway: "203.0.113.1"},
		IPv6: model.IPv6{Mode: model.AddrNone},
	})
	cfg.Gateways = []model.Gateway{{Name: "backup", Enabled: true, Interface: "eth2", Address: "203.0.113.1"}}
	cfg.Rules = append([]model.Rule{{
		ID: "server-out-backup", Enabled: true, Zone: "lan", Action: model.ActionAccept, Protocol: model.ProtocolAny,
		Source: model.Endpoint{Addresses: []string{"192.168.1.10"}}, Gateway: "backup",
	}}, cfg.Rules...)
	cfg.NAT.PortForwards = []model.PortForward{
		{ID: "pf-web", Enabled: true, Zone: "wan", Protocol: model.ProtocolTCP, Ports: []string{"8080"}, Target: "192.168.1.10"},
	}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// eth0 is the WAN and eth1 the LAN, each fed from a veth peer. eth2 is
	// the backup line, a dummy. The server and the WAN's router answer ARP
	// through static entries, so nothing waits on a neighbour.
	wan, wanPeer := vethPair(t, "eth0", "peer0", "198.51.100.2/24")
	lan, lanPeer := vethPair(t, "eth1", "peer1", "192.168.1.1/24")
	backup := netnstest.Dummy(t, "eth2", "203.0.113.2/24")
	if err := netlink.AddRoute(netlink.Route{LinkIndex: wan.Index, Gw: net.ParseIP("198.51.100.1")}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []netlink.Neighbour{
		{LinkIndex: lan.Index, IP: net.ParseIP("192.168.1.10").To4(), HardwareAddr: lanPeer.HardwareAddr},
		{LinkIndex: wan.Index, IP: net.ParseIP("198.51.100.1").To4(), HardwareAddr: wanPeer.HardwareAddr},
	} {
		n.Family, n.State = syscall.AF_INET, 0x80 // NUD_PERMANENT
		if err := netlink.AddNeighbour(n); err != nil {
			t.Fatal(err)
		}
	}
	forwardIPv4(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	hops := map[string]policy.Hop{
		"backup": {Gateway: "backup", Address: "203.0.113.1", Interface: "eth2", Online: true},
	}
	if err := policy.NewInstaller(slog.New(slog.DiscardHandler)).Sync(policy.Plan(cfg, hops)); err != nil {
		t.Fatal(err)
	}
	outWAN := capture(t, wan)
	outBackup := capture(t, backup)

	client := net.ParseIP("192.0.2.9")
	sendFrame(t, wanPeer, wan, tcpFrame(wan.HardwareAddr, wanPeer.HardwareAddr,
		client, net.ParseIP("198.51.100.2"), 40000, 8080, tcpSYN, 1, 0))
	if n := waitForCounter(ctx, t, x, "pf-web", 1); n != 1 {
		t.Fatalf("the port forward counted %d packets, want 1", n)
	}
	sendFrame(t, lanPeer, lan, tcpFrame(lan.HardwareAddr, lanPeer.HardwareAddr,
		net.ParseIP("192.168.1.10"), client, 8080, 40000, tcpSYN|tcpACK, 5000, 2))

	answer := func(s tcpSegment) bool { return s.dport == 40000 }
	s, ok := nextSegment(t, outWAN, answer, 3*time.Second)
	switch {
	case !ok:
		t.Error("the server's answer did not leave by the WAN")
	case !s.src.Equal(net.ParseIP("198.51.100.2")) || s.sport != 8080 || s.flags != tcpSYN|tcpACK:
		t.Errorf("left by the WAN: %+v, want the SYN-ACK from 198.51.100.2:8080", s)
	}
	if s, ok := nextSegment(t, outBackup, answer, 300*time.Millisecond); ok {
		t.Errorf("%+v left by the backup line", s)
	}
}

// vethPair makes a veth pair with an address on the near end, both ends up.
func vethPair(t *testing.T, name, peerName, addr string) (link, peer netlink.Link) {
	t.Helper()
	if err := netlink.AddVeth(name, peerName); err != nil {
		t.Fatalf("add veth: %v", err)
	}
	link, peer = netnstest.Link(t, name), netnstest.Link(t, peerName)
	for _, l := range []netlink.Link{link, peer} {
		if err := netlink.SetLinkUp(l.Index); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.AddAddr(link.Index, netip.MustParsePrefix(addr)); err != nil {
		t.Fatal(err)
	}
	return link, peer
}

// forwardIPv4 turns forwarding on in the namespace and reverse path
// filtering off, since the test's senders are not where routes say.
func forwardIPv4(t *testing.T) {
	t.Helper()
	for path, value := range map[string]string{
		"/proc/sys/net/ipv4/ip_forward":         "1",
		"/proc/sys/net/ipv4/conf/all/rp_filter": "0",
	} {
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

// capture opens a packet socket that sees every frame leaving link.
func capture(t *testing.T, link netlink.Link) int {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ALL), Ifindex: link.Index}); err != nil {
		t.Fatal(err)
	}
	return fd
}

// sendFrame puts frame on the wire at from, the peer of to.
func sendFrame(t *testing.T, from, to netlink.Link, frame []byte) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	defer syscall.Close(fd)
	addr := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_IP), Ifindex: from.Index, Halen: 6}
	copy(addr.Addr[:], to.HardwareAddr)
	if err := syscall.Sendto(fd, frame, 0, addr); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// waitForCounter reads the ruleset's counters until key reaches want, or
// three seconds pass, and returns what it last read.
func waitForCounter(ctx context.Context, t *testing.T, x *Exec, key string, want uint64) uint64 {
	t.Helper()
	var n uint64
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, err := x.ListTableJSON(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseCounters(raw)
		if err != nil {
			t.Fatal(err)
		}
		if n = got[key].Packets; n >= want {
			break
		}
	}
	return n
}

// tcpSegment is what a test reads off a captured frame.
type tcpSegment struct {
	src, dst     net.IP
	sport, dport uint16
	flags        byte
}

// nextSegment reads fd until a TCP segment that match accepts arrives, or
// the wait runs out.
func nextSegment(t *testing.T, fd int, match func(tcpSegment) bool, wait time.Duration) (tcpSegment, bool) {
	t.Helper()
	tv := syscall.NsecToTimeval(wait.Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return tcpSegment{}, false
		}
		if s, ok := parseTCP(buf[:n]); ok && match(s) {
			return s, true
		}
	}
	return tcpSegment{}, false
}

// parseTCP reads the addresses, ports and flags of an Ethernet frame
// carrying IPv4 TCP.
func parseTCP(frame []byte) (tcpSegment, bool) {
	if len(frame) < 14+20 || binary.BigEndian.Uint16(frame[12:]) != syscall.ETH_P_IP {
		return tcpSegment{}, false
	}
	ip := frame[14:]
	ihl := int(ip[0]&0x0f) * 4
	if ip[9] != syscall.IPPROTO_TCP || len(ip) < ihl+20 {
		return tcpSegment{}, false
	}
	tcp := ip[ihl:]
	return tcpSegment{
		src: net.IP(append([]byte{}, ip[12:16]...)), dst: net.IP(append([]byte{}, ip[16:20]...)),
		sport: binary.BigEndian.Uint16(tcp[0:]), dport: binary.BigEndian.Uint16(tcp[2:]),
		flags: tcp[13],
	}, true
}

// synMSS reads the segment size an Ethernet frame's TCP SYN to port offers.
func synMSS(frame []byte, port uint16) (int, bool) {
	if len(frame) < 14+20 || binary.BigEndian.Uint16(frame[12:]) != syscall.ETH_P_IP {
		return 0, false
	}
	ip := frame[14:]
	ihl := int(ip[0]&0x0f) * 4
	if ip[9] != syscall.IPPROTO_TCP || len(ip) < ihl+20 {
		return 0, false
	}
	tcp := ip[ihl:]
	off := int(tcp[12]>>4) * 4
	if binary.BigEndian.Uint16(tcp[2:]) != port || tcp[13]&0x02 == 0 || len(tcp) < off {
		return 0, false
	}
	for opts := tcp[20:off]; len(opts) > 0; {
		switch {
		case opts[0] == 0:
			return 0, false
		case opts[0] == 1:
			opts = opts[1:]
		case len(opts) < 2 || int(opts[1]) < 2 || int(opts[1]) > len(opts):
			return 0, false
		case opts[0] == 2 && opts[1] == 4:
			return int(binary.BigEndian.Uint16(opts[2:])), true
		default:
			opts = opts[opts[1]:]
		}
	}
	return 0, false
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// synFrame builds an Ethernet frame carrying a TCP SYN with valid
// checksums, so conntrack takes it as a new connection. The options, if
// any, come in whole 32-bit words.
func synFrame(dstMAC, srcMAC net.HardwareAddr, src, dst net.IP, sport, dport uint16, opts ...byte) []byte {
	return tcpFrame(dstMAC, srcMAC, src, dst, sport, dport, tcpSYN, 1, 0, opts...)
}

// TCP flags, as the header's thirteenth byte holds them.
const (
	tcpSYN = 0x02
	tcpACK = 0x10
)

// tcpFrame builds an Ethernet frame carrying one TCP segment with valid
// checksums. ack is sent only when the flags carry ACK.
func tcpFrame(dstMAC, srcMAC net.HardwareAddr, src, dst net.IP, sport, dport uint16, flags byte, seq, ack uint32, opts ...byte) []byte {
	src, dst = src.To4(), dst.To4()
	tcp := make([]byte, 20+len(opts))
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(len(ip)+len(tcp)))
	binary.BigEndian.PutUint16(ip[6:], 0x4000)
	ip[8], ip[9] = 64, syscall.IPPROTO_TCP
	copy(ip[12:], src)
	copy(ip[16:], dst)
	binary.BigEndian.PutUint16(ip[10:], checksum(ip))

	binary.BigEndian.PutUint16(tcp[0:], sport)
	binary.BigEndian.PutUint16(tcp[2:], dport)
	binary.BigEndian.PutUint32(tcp[4:], seq)
	if flags&tcpACK != 0 {
		binary.BigEndian.PutUint32(tcp[8:], ack)
	}
	tcp[12], tcp[13] = byte(len(tcp)/4)<<4, flags
	copy(tcp[20:], opts)
	binary.BigEndian.PutUint16(tcp[14:], 64240)
	pseudo := append(append(append([]byte{}, src...), dst...), 0, syscall.IPPROTO_TCP, 0, byte(len(tcp)))
	binary.BigEndian.PutUint16(tcp[16:], checksum(append(pseudo, tcp...)))

	frame := append(append(append([]byte{}, dstMAC...), srcMAC...), 0x08, 0x00)
	return append(append(frame, ip...), tcp...)
}

func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	return ^uint16(sum)
}

// A drop that logs keeps a sample, not every packet. A burst of SYNs at a
// closed port reaches the tail of a zone that logs: every one is dropped
// and counted, a handful are logged, and the log rule's own counter says
// how many.
func TestALoggedDropKeepsASampleInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	cfg := loadConfig(t, "testdata/minimal.json")
	for i := range cfg.Zones {
		if cfg.Zones[i].Name == "wan" {
			cfg.Zones[i].LogDrops = true
		}
	}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wan, peer := vethPair(t, "eth0", "peer0", "198.51.100.2/24")
	// The route back to the sender, or reverse path filtering drops the
	// burst before the firewall sees it.
	if err := netlink.AddRoute(netlink.Route{LinkIndex: wan.Index, Gw: net.ParseIP("198.51.100.1")}); err != nil {
		t.Fatal(err)
	}
	forwardIPv4(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}

	lg, err := netlink.OpenLog(LogGroup)
	if err != nil {
		t.Fatal(err)
	}
	var logged atomic.Int64
	reading, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = lg.Read(reading, func(p netlink.LogPacket) {
			if strings.HasPrefix(p.Prefix, "ostiole:z:wan:drop:") {
				logged.Add(1)
			}
		}, func(error) {})
	}()
	defer func() { stop(); <-done; _ = lg.Close() }()

	const burst = 200
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	defer syscall.Close(fd)
	to := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_IP), Ifindex: peer.Index, Halen: 6}
	copy(to.Addr[:], wan.HardwareAddr)
	for i := range burst {
		frame := synFrame(wan.HardwareAddr, peer.HardwareAddr,
			net.ParseIP("203.0.113.9"), net.ParseIP("198.51.100.2"), uint16(30000+i), 2222)
		if err := syscall.Sendto(fd, frame, 0, to); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if n := waitForCounter(ctx, t, x, "zone_wan/zone-default", burst); n != burst {
		t.Fatalf("the zone's tail dropped %d packets, want %d", n, burst)
	}
	// nflog hands packets over in batches, within about a second.
	time.Sleep(1500 * time.Millisecond)
	raw, err := x.ListTableJSON(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCounters(raw)
	if err != nil {
		t.Fatal(err)
	}
	kept := got["zone_wan/log:zone-default"].Packets
	if n := logged.Load(); n == 0 || n > 15 || uint64(n) != kept {
		t.Errorf("logged %d of %d, and the log rule counted %d; want a few, the same both ways", n, burst, kept)
	}
}
