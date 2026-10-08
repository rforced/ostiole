package nft

import (
	"context"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"
	"time"

	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
)

// The proxy's access list in the kernel: SYNs to 443 on the WAN from an
// address in the alias a line accepts, from a source a line above it
// drops, and from anywhere else, which no line takes and the zone's end
// counts.
func TestProxyAccessInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}

	ruleset, err := Render(loadConfig(t, "testdata/proxy-access.json"))
	if err != nil {
		t.Fatal(err)
	}
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
	for sport, src := range map[uint16]string{
		40000: "192.0.2.9",   // in the home alias
		40001: "203.0.113.9", // a scanner
		40002: "198.18.0.9",  // anyone else
	} {
		frame := synFrame(wan.HardwareAddr, peer.HardwareAddr,
			net.ParseIP(src), net.ParseIP("198.51.100.2"), sport, 443)
		if err := syscall.Sendto(fd, frame, 0, to); err != nil {
			t.Fatalf("send from %s: %v", src, err)
		}
	}

	want := map[string]uint64{
		"zone_wan/proxy:https-home": 1,
		"zone_wan/proxy:scanners":   1,
		"zone_wan/zone-unmatched":   1,
	}
	var got Counters
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, err := x.ListTableJSON(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got, err = ParseCounters(raw); err != nil {
			t.Fatal(err)
		}
		sum := uint64(0)
		for key := range want {
			sum += got[key].Packets
		}
		if sum >= 3 {
			break
		}
	}
	for key, n := range want {
		if got[key].Packets != n {
			t.Errorf("%s counted %d packets, want %d", key, got[key].Packets, n)
		}
	}
}
