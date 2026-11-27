package nft

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
)

// A scan is refused TCP and UDP. Pings are not one: a monitor that pings
// once a second is refused sixty times a minute and is never held, where
// a source that knocks on closed ports is. The frames come in on the WAN
// from addresses nobody there owns, as a forged source's would.
func TestOnlyRefusedPortsHoldASourceInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	ruleset, err := Render(protectedConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddVeth("eth0", "peer0"); err != nil {
		t.Fatalf("add veth: %v", err)
	}
	wan, peer := netnstest.Link(t, "eth0"), netnstest.Link(t, "peer0")
	for _, l := range []netlink.Link{wan, peer} {
		if err := netlink.SetLinkUp(l.Index); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.AddAddr(wan.Index, netip.MustParsePrefix("198.51.100.2/24")); err != nil {
		t.Fatal(err)
	}
	netnstest.Dummy(t, "eth1", "192.168.1.1/24")
	for _, path := range []string{"/proc/sys/net/ipv4/conf/all/rp_filter", "/proc/sys/net/ipv4/conf/eth0/rp_filter"} {
		if err := os.WriteFile(path, []byte("0"), 0o644); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := (&Exec{}).Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	defer syscall.Close(fd)
	to := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_IP), Ifindex: peer.Index, Halen: 6}
	copy(to.Addr[:], wan.HardwareAddr)
	router := net.ParseIP("198.51.100.2")
	for i := range uint16(15) {
		for _, frame := range [][]byte{
			echoFrame(wan.HardwareAddr, peer.HardwareAddr, net.ParseIP("192.0.2.9"), router, i),
			synFrame(wan.HardwareAddr, peer.HardwareAddr, net.ParseIP("192.0.2.10"), router, 40000+i, 1000+i),
		} {
			if err := syscall.Sendto(fd, frame, 0, to); err != nil {
				t.Fatalf("send: %v", err)
			}
		}
	}

	var held string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		out, err := exec.CommandContext(ctx, "nft", "-j", "list", "set", "inet", "ostiole", "scanners_wan_v4").CombinedOutput()
		if err != nil {
			t.Fatalf("list the hold: %v\n%s", err, out)
		}
		if held = string(out); strings.Contains(held, "192.0.2.10") {
			break
		}
	}
	if !strings.Contains(held, "192.0.2.10") {
		t.Errorf("the source knocking on closed ports was not held: %s", held)
	}
	if strings.Contains(held, "192.0.2.9") {
		t.Errorf("the source that only pinged was held: %s", held)
	}
}

// Floods are counted by /64 on an external zone, where one subscriber
// holds the whole network and can send from any address in it, and by
// address on an inside one, where each address is a device of its own.
// Two addresses in one /64 send four SYNs each to the router, against a
// burst of five: on the WAN they share one allowance, so some are dropped
// and the flood set holds one network; on the guest network each has its
// own, so none are and the set holds both addresses.
func TestOutsideFloodsAreCountedByNetworkInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	quietIPv6(t)
	cfg := loadConfig(t, "testdata/protection-management.json")
	cfg.Protection.SynFlood = &model.RateLimit{Rate: 1, Unit: model.PerMinute, Burst: 5, PerSource: true}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	wan, wanPeer := vethPair(t, "eth0", "peer0", "198.51.100.2/24")
	guest, guestPeer := vethPair(t, "eth3", "peer3", "192.168.30.1/24")
	netnstest.AddAddr(t, wan.Index, netip.MustParsePrefix("2001:db8:ff::2/64"))
	netnstest.AddAddr(t, guest.Index, netip.MustParsePrefix("2001:db8:30::1/64"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}

	for _, side := range []struct {
		link, peer netlink.Link
		router     string
		sources    []string
	}{
		{wan, wanPeer, "2001:db8:ff::2", []string{"2001:db8:77::10", "2001:db8:77::11"}},
		{guest, guestPeer, "2001:db8:30::1", []string{"2001:db8:30::10", "2001:db8:30::11"}},
	} {
		for i, src := range side.sources {
			for j := range uint16(4) {
				sendFrame(t, side.peer, side.link, syn6Frame(side.link.HardwareAddr, side.peer.HardwareAddr,
					net.ParseIP(src), net.ParseIP(side.router), 40000+uint16(i)*10+j, 443))
			}
		}
	}

	if n := waitForCounter(ctx, t, x, "zone_wan/protect:synflood", 1); n < 1 {
		t.Error("the WAN dropped none of eight SYNs from one /64 against a burst of five")
	}
	var wanHeld, guestHeld []string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if wanHeld, guestHeld = setElements(ctx, t, "synflood_wan_v6"), setElements(ctx, t, "synflood_guest_v6"); len(guestHeld) >= 2 {
			break
		}
	}
	if !slices.Equal(wanHeld, []string{"2001:db8:77::"}) {
		t.Errorf("the WAN's flood set holds %q, want the one /64", wanHeld)
	}
	slices.Sort(guestHeld)
	if !slices.Equal(guestHeld, []string{"2001:db8:30::10", "2001:db8:30::11"}) {
		t.Errorf("the guest network's flood set holds %q, want both addresses", guestHeld)
	}
	if n := waitForCounter(ctx, t, x, "zone_guest/protect:synflood", 0); n != 0 {
		t.Errorf("the guest network dropped %d SYNs, want none", n)
	}
}

// Anti-lockout keeps the management ports open on a WAN to a source the
// scan detector holds, but on an external zone it no longer opens them to
// a flood: the zone's SYN limit comes first. On the LAN, which the
// protection covers too, it lets every connection in as before. The limit
// is a burst of twenty and then one a minute. Two sources knock on fifteen
// closed ports, one in each family, and are held, the IPv6 one with its
// whole /64: its neighbour in it no longer reaches the web site, and both
// still reach the web port. Of a hundred SYNs from one source some are
// cut, on the WAN and on a second WAN that logs what it drops; a hundred
// from a LAN host all get in.
func TestAntiLockoutOutsideAnswersToTheFloodLimitInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	cfg := loadConfig(t, "testdata/protection-management.json")
	cfg.Protection.Zones = append(cfg.Protection.Zones, "lan")
	cfg.Protection.SynFlood = &model.RateLimit{Rate: 1, Unit: model.PerMinute, Burst: 20, PerSource: true}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	quietIPv6(t)
	for _, key := range []string{"all", "default"} {
		if err := os.WriteFile("/proc/sys/net/ipv4/conf/"+key+"/rp_filter", []byte("0"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	type side struct {
		link, peer netlink.Link
		send       func([]byte)
	}
	cabled := func(name, peerName, addr string) side {
		link, peer := vethPair(t, name, peerName, addr)
		return side{link, peer, frameSender(t, peer, link)}
	}
	wan, wan2, lan := cabled("eth0", "peer0", "198.51.100.2/24"), cabled("eth1", "peer1", "203.0.113.2/24"), cabled("eth2", "peer2", "192.168.1.1/24")
	netnstest.AddAddr(t, wan.link.Index, netip.MustParsePrefix("2001:db8:ff::2/64"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}
	syn := func(on side, src, dst string, sport, dport uint16) {
		from, to := net.ParseIP(src), net.ParseIP(dst)
		if from.To4() != nil {
			on.send(synFrame(on.link.HardwareAddr, on.peer.HardwareAddr, from, to, sport, dport))
		} else {
			on.send(syn6Frame(on.link.HardwareAddr, on.peer.HardwareAddr, from, to, sport, dport))
		}
	}
	held := func(set, source string) bool {
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if slices.Contains(setElements(ctx, t, set), source) {
				return true
			}
		}
		return false
	}

	for i := range uint16(15) {
		syn(wan, "192.0.2.10", "198.51.100.2", 40000+i, 1000+i)
		syn(wan, "2001:db8:77::10", "2001:db8:ff::2", 40000+i, 1000+i)
	}
	if !held("scanners_wan_v4", "192.0.2.10") || !held("scanners_wan_v6", "2001:db8:77::") {
		t.Fatal("the sources knocking on closed ports were not held")
	}
	syn(wan, "2001:db8:77::11", "2001:db8:ff::2", 41000, 443)
	syn(wan, "192.0.2.10", "198.51.100.2", 41001, 9443)
	syn(wan, "2001:db8:77::11", "2001:db8:ff::2", 41002, 9443)
	if n := waitForCounter(ctx, t, x, "input/anti-lockout:wan", 2); n != 2 {
		t.Errorf("the held sources' connections to the web port were let in %d times, want twice", n)
	}
	if n := waitForCounter(ctx, t, x, "web-in", 0); n != 0 {
		t.Errorf("the held /64's neighbour reached the web site %d times", n)
	}

	for i := range uint16(100) {
		syn(wan, "192.0.2.20", "198.51.100.2", 42000+i, 9443)
		syn(wan2, "192.0.2.30", "203.0.113.2", 42000+i, 9443)
		syn(lan, "192.168.1.20", "192.168.1.1", 42000+i, 9443)
	}
	for key, where := range map[string]string{
		"input/anti-lockout-flood:wan":           "the WAN",
		"plog_wan2_lockflood/anti-lockout-flood": "the second WAN",
	} {
		if n := waitForCounter(ctx, t, x, key, 1); n < 1 {
			t.Errorf("none of a hundred SYNs from one source was cut on %s", where)
		}
	}
	if n := waitForCounter(ctx, t, x, "input/anti-lockout:lan", 100); n != 100 {
		t.Errorf("the LAN let in %d of a hundred SYNs to the web port, want all", n)
	}
}

// frameSender keeps one packet socket open to put frames on the wire at
// from, the peer of to. sendFrame's socket per frame costs an RCU grace
// period at each close, too slow for a burst.
func frameSender(t *testing.T, from, to netlink.Link) func([]byte) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, 0)
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	addr := &syscall.SockaddrLinklayer{Ifindex: from.Index, Halen: 6}
	copy(addr.Addr[:], to.HardwareAddr)
	return func(frame []byte) {
		t.Helper()
		if err := syscall.Sendto(fd, frame, 0, addr); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
}

// setElements lists the elements of a set in the ruleset's table.
func setElements(ctx context.Context, t *testing.T, set string) []string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "nft", "-j", "list", "set", "inet", "ostiole", set).Output()
	if err != nil {
		t.Fatalf("list %s: %v", set, err)
	}
	var doc struct {
		Nftables []struct {
			Set *struct {
				Elem []json.RawMessage `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	var vals []string
	for _, o := range doc.Nftables {
		if o.Set == nil {
			continue
		}
		for _, raw := range o.Set.Elem {
			var v string
			if json.Unmarshal(raw, &v) == nil {
				vals = append(vals, v)
				continue
			}
			var e struct {
				Elem struct {
					Val string `json:"val"`
				} `json:"elem"`
			}
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatalf("an element of %s: %v: %s", set, err, raw)
			}
			vals = append(vals, e.Elem.Val)
		}
	}
	return vals
}

// echoFrame builds an Ethernet frame carrying an ICMP echo request with
// valid checksums.
func echoFrame(dstMAC, srcMAC net.HardwareAddr, src, dst net.IP, seq uint16) []byte {
	src, dst = src.To4(), dst.To4()
	icmp := make([]byte, 16)
	icmp[0] = 8
	binary.BigEndian.PutUint16(icmp[4:], 1)
	binary.BigEndian.PutUint16(icmp[6:], seq)
	binary.BigEndian.PutUint16(icmp[2:], checksum(icmp))
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:], uint16(len(ip)+len(icmp)))
	ip[8], ip[9] = 64, syscall.IPPROTO_ICMP
	copy(ip[12:], src)
	copy(ip[16:], dst)
	binary.BigEndian.PutUint16(ip[10:], checksum(ip))
	frame := append(append(append([]byte{}, dstMAC...), srcMAC...), 0x08, 0x00)
	return append(append(frame, ip...), icmp...)
}
