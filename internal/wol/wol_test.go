package wol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/netnstest"
)

func TestPacket(t *testing.T) {
	t.Parallel()
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0x00, 0x11, 0x22}
	p := Packet(mac)
	if len(p) != 102 {
		t.Fatalf("length = %d, want 102", len(p))
	}
	if !bytes.Equal(p[:6], bytes.Repeat([]byte{0xff}, 6)) {
		t.Errorf("sync stream = % x", p[:6])
	}
	for i := range 16 {
		if got := p[6+6*i : 12+6*i]; !bytes.Equal(got, mac) {
			t.Errorf("copy %d = % x", i, got)
		}
	}
}

func TestSendRefusesALongAddress(t *testing.T) {
	t.Parallel()
	if err := Send("lo", make(net.HardwareAddr, 8)); err == nil || !strings.Contains(err.Error(), "six-byte") {
		t.Errorf("err = %v", err)
	}
}

// TestSendInNamespace sends real frames inside an unprivileged user and
// network namespace, where this process has CAP_NET_RAW without being
// root, and reads them back off the link.
func TestSendInNamespace(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	if err := netlink.LinkAdd(&netlink.Dummy{Name: "wake0"}); err != nil {
		t.Fatalf("add link: %v", err)
	}
	link, err := netlink.LinkByName("wake0")
	if err != nil {
		t.Fatal(err)
	}
	mac := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0x00, 0x11, 0x22}

	// A link that is down takes nothing.
	if err := Send("wake0", mac); !errors.Is(err, unix.ENETDOWN) {
		t.Errorf("down link: err = %v, want ENETDOWN", err)
	}
	if err := Send("ghost0", mac); !errors.Is(err, ErrNoLink) {
		t.Errorf("missing link: err = %v, want ErrNoLink", err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	fd := listen(t, link.Attrs().Index)
	if err := Send("wake0", mac); err != nil {
		t.Fatalf("send: %v", err)
	}
	frame := readWake(t, fd)
	if dst := net.HardwareAddr(frame[0:6]); dst.String() != "ff:ff:ff:ff:ff:ff" {
		t.Errorf("destination = %s, want broadcast", dst)
	}
	if src := net.HardwareAddr(frame[6:12]); src.String() != link.Attrs().HardwareAddr.String() {
		t.Errorf("source = %s, want the link's own %s", src, link.Attrs().HardwareAddr)
	}
	if got := frame[14:]; !bytes.Equal(got[:102], Packet(mac)) {
		t.Errorf("payload = % x", got)
	}
}

// listen opens a packet socket that sees every frame on one link, the
// ones this process sends included.
func listen(t *testing.T, index int) int {
	t.Helper()
	proto := htons(unix.ETH_P_ALL)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(proto))
	if err != nil {
		t.Fatalf("capture socket: %v", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: proto, Ifindex: index}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	tv := unix.NsecToTimeval((2 * time.Second).Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		t.Fatal(err)
	}
	return fd
}

// readWake returns the first Wake on LAN frame the socket reads.
func readWake(t *testing.T, fd int) []byte {
	t.Helper()
	buf := make([]byte, 2048)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			t.Fatalf("no wake frame: %v", err)
		}
		if n >= 14+102 && binary.BigEndian.Uint16(buf[12:14]) == EtherType {
			return buf[:n]
		}
	}
}
