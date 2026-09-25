// Package wol wakes a machine by sending the magic packet its network card
// listens for while the machine sleeps.
package wol

import (
	"bytes"
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// EtherType is the frame type registered for Wake on LAN. A card finds the
// pattern anywhere in a frame; the type keeps the frame from being read as
// anything else on the way.
const EtherType = 0x0842

// ErrNoLink is returned for an interface the kernel does not have.
var ErrNoLink = errors.New("no such interface on this router")

var broadcast = net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// Packet is the payload a sleeping card looks for: six 0xff bytes, then its
// own address sixteen times.
func Packet(mac net.HardwareAddr) []byte {
	out := bytes.Repeat([]byte{0xff}, 6)
	for range 16 {
		out = append(out, mac...)
	}
	return out
}

// Send puts one magic packet for mac on the link named ifname, addressed to
// every machine on it. The frame is built below IP, so the link needs no
// address and the firewall never sees it. It needs CAP_NET_RAW, which the
// daemon has as root.
func Send(ifname string, mac net.HardwareAddr) error {
	if len(mac) != 6 {
		return fmt.Errorf("%s is not a six-byte MAC address", mac)
	}
	ifi, err := net.InterfaceByName(ifname)
	if err != nil {
		return fmt.Errorf("%s: %w", ifname, ErrNoLink)
	}
	// Protocol zero receives nothing: this socket only sends.
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("packet socket: %w", err)
	}
	defer unix.Close(fd)
	to := &unix.SockaddrLinklayer{Protocol: htons(EtherType), Ifindex: ifi.Index, Halen: 6}
	copy(to.Addr[:], broadcast)
	if err := unix.Sendto(fd, Packet(mac), 0, to); err != nil {
		return fmt.Errorf("send on %s: %w", ifname, err)
	}
	return nil
}

// htons puts a protocol number in network byte order, which is what the
// packet socket wants.
func htons(v uint16) uint16 { return v<<8 | v>>8 }
