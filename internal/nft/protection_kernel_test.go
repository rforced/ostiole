package nft

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

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
