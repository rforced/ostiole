package nft

import (
	"context"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
	"github.com/rforced/ostiole/internal/netnstest"
)

// A CA reaches the http-01 solver past a proxy that stays inside. The
// kernel runs the ruleset and SYNs come in on the WAN: one to port 80 is
// redirected to the solver's port and let in, and one straight to that
// port is not.
func TestChallengesReachTheSolverInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}

	ruleset, err := Render(loadConfig(t, "testdata/acme-http01-proxy-lan.json"))
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
	// Where the proxy and the solver listen, on every address.
	for _, port := range []string{"80", "8402"} {
		l, err := net.Listen("tcp", ":"+port)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
	}

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		t.Fatalf("packet socket: %v", err)
	}
	defer syscall.Close(fd)
	to := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_IP), Ifindex: peer.Index, Halen: 6}
	copy(to.Addr[:], wan.HardwareAddr)
	for sport, dport := range map[uint16]uint16{40000: 80, 40001: model.ChallengePort} {
		frame := synFrame(wan.HardwareAddr, peer.HardwareAddr,
			net.ParseIP("192.0.2.9"), net.ParseIP("198.51.100.2"), sport, dport)
		if err := syscall.Sendto(fd, frame, 0, to); err != nil {
			t.Fatalf("send to port %d: %v", dport, err)
		}
	}

	want := map[string]uint64{
		"nat_prerouting/service:acme": 1,
		"input/service:acme":          1,
		"zone_wan/zone-unmatched":     1,
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
		if got["input/service:acme"].Packets+got["zone_wan/zone-unmatched"].Packets >= 2 {
			break
		}
	}
	for key, n := range want {
		if got[key].Packets != n {
			t.Errorf("%s counted %d packets, want %d", key, got[key].Packets, n)
		}
	}
}
