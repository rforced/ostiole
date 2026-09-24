package nft

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
)

// A port forward is no way around the zone it arrives on. The kernel runs
// the ruleset, a SYN for the forwarded port comes in on the WAN, and the
// counters say which rule decided: a source the WAN zone drops is dropped
// there, and anyone else reaches the forward.
func TestPortForwardsPassThroughTheZoneInKernel(t *testing.T) {
	const env = "OSTIOLE_NFT_NETNS"
	if os.Getenv(env) == "" {
		runInNamespace(t, env, "TestPortForwardsPassThroughTheZoneInKernel")
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
	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "eth0"}, PeerName: "peer0"}
	if err := netlink.LinkAdd(veth); err != nil {
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
		if err := netlink.LinkSetUp(l); err != nil {
			t.Fatal(err)
		}
	}
	addr, _ := netlink.ParseAddr("198.51.100.2/24")
	if err := netlink.AddrAdd(wan, addr); err != nil {
		t.Fatal(err)
	}
	dummyLink(t, "eth1", "192.168.1.1/24")
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
	to := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_IP), Ifindex: peer.Attrs().Index, Halen: 6}
	copy(to.Addr[:], wan.Attrs().HardwareAddr)
	for _, src := range []string{"203.0.113.9", "192.0.2.9"} {
		frame := synFrame(wan.Attrs().HardwareAddr, peer.Attrs().HardwareAddr,
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

func htons(v uint16) uint16 { return v<<8 | v>>8 }

// synFrame builds an Ethernet frame carrying a TCP SYN with valid
// checksums, so conntrack takes it as a new connection.
func synFrame(dstMAC, srcMAC net.HardwareAddr, src, dst net.IP, sport, dport uint16) []byte {
	src, dst = src.To4(), dst.To4()
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], 40)
	binary.BigEndian.PutUint16(ip[6:], 0x4000)
	ip[8], ip[9] = 64, syscall.IPPROTO_TCP
	copy(ip[12:], src)
	copy(ip[16:], dst)
	binary.BigEndian.PutUint16(ip[10:], checksum(ip))

	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:], sport)
	binary.BigEndian.PutUint16(tcp[2:], dport)
	binary.BigEndian.PutUint32(tcp[4:], 1)
	tcp[12], tcp[13] = 5<<4, 0x02
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
