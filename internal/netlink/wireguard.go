package netlink

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"time"

	"golang.org/x/sys/unix"
)

// WireGuardDevice is what the kernel says of one WireGuard device. The
// dump carries the device's private key and each peer's preshared key;
// both are skipped where they are parsed and never held.
type WireGuardDevice struct {
	Name       string
	PublicKey  []byte
	ListenPort int
	Peers      []WireGuardPeer
}

// WireGuardPeer is one peer as the device sees it now.
type WireGuardPeer struct {
	PublicKey []byte
	// Endpoint is where the peer was last heard from, or where it is
	// dialled; zero for one that has done neither.
	Endpoint netip.AddrPort
	// LastHandshake is zero for a peer that never completed one.
	LastHandshake    time.Time
	RxBytes, TxBytes uint64
}

// WireGuard reads a WireGuard device by name. It needs CAP_NET_ADMIN.
func WireGuard(name string) (WireGuardDevice, error) {
	c, err := dial(unix.NETLINK_GENERIC, 0)
	if err != nil {
		return WireGuardDevice{}, err
	}
	defer func() { _ = c.close() }()
	family, err := genlFamily(c, unix.WG_GENL_NAME)
	if err != nil {
		return WireGuardDevice{}, err
	}
	var e encoder
	e.genlmsghdr(unix.WG_CMD_GET_DEVICE)
	e.attr(unix.WGDEVICE_A_IFNAME, append([]byte(name), 0))
	if e.err != nil {
		return WireGuardDevice{}, e.err
	}
	for attempt := 1; ; attempt++ {
		var dev WireGuardDevice
		err := c.exchange(family, unix.NLM_F_DUMP, e.b, func(m message) error { return dev.parse(m.data) })
		if errors.Is(err, errInterrupted) && attempt < dumpAttempts {
			continue
		}
		return dev, err
	}
}

// parse reads one message of a device's dump. The device's own attributes
// come in the first; the peers follow over as many messages as they need,
// and a peer cut short carries on in the next under the same key with
// only its allowed addresses, which are not read.
func (d *WireGuardDevice) parse(b []byte) error {
	if len(b) < unix.GENL_HDRLEN {
		return errors.New("netlink: a short WireGuard message")
	}
	as, err := attrs(b[unix.GENL_HDRLEN:])
	if err != nil {
		return err
	}
	for _, a := range as {
		switch a.typ {
		case unix.WGDEVICE_A_IFNAME:
			d.Name = cstring(a.data)
		case unix.WGDEVICE_A_PUBLIC_KEY:
			d.PublicKey = wgKey(a.data)
		case unix.WGDEVICE_A_LISTEN_PORT:
			d.ListenPort = int(u16(a.data))
		case unix.WGDEVICE_A_PEERS:
			peers, err := attrs(a.data)
			if err != nil {
				return err
			}
			for _, pa := range peers {
				p, err := parseWireGuardPeer(pa.data)
				if err != nil {
					return err
				}
				if n := len(d.Peers); n > 0 && bytes.Equal(d.Peers[n-1].PublicKey, p.PublicKey) {
					continue
				}
				d.Peers = append(d.Peers, p)
			}
		}
	}
	return nil
}

func parseWireGuardPeer(b []byte) (WireGuardPeer, error) {
	as, err := attrs(b)
	if err != nil {
		return WireGuardPeer{}, err
	}
	var p WireGuardPeer
	for _, a := range as {
		switch a.typ {
		case unix.WGPEER_A_PUBLIC_KEY:
			p.PublicKey = wgKey(a.data)
		case unix.WGPEER_A_ENDPOINT:
			p.Endpoint = sockaddr(a.data)
		case unix.WGPEER_A_LAST_HANDSHAKE_TIME:
			// struct __kernel_timespec: two 64-bit numbers, zero for never.
			if len(a.data) >= 16 {
				sec, nsec := int64(native.Uint64(a.data)), int64(native.Uint64(a.data[8:])) //nolint:gosec // the kernel's s64s
				if sec != 0 || nsec != 0 {
					p.LastHandshake = time.Unix(sec, nsec)
				}
			}
		case unix.WGPEER_A_RX_BYTES:
			p.RxBytes = u64(a.data)
		case unix.WGPEER_A_TX_BYTES:
			p.TxBytes = u64(a.data)
		}
	}
	return p, nil
}

// wgKey copies a key out of a datagram; anything but 32 bytes is none.
func wgKey(b []byte) []byte {
	if len(b) != 32 {
		return nil
	}
	return append([]byte(nil), b...)
}

// sockaddr reads the struct sockaddr_in or sockaddr_in6 an endpoint comes
// in: its family in host order, its port in network order.
func sockaddr(b []byte) netip.AddrPort {
	if len(b) < 4 {
		return netip.AddrPort{}
	}
	port := binary.BigEndian.Uint16(b[2:])
	switch native.Uint16(b) {
	case unix.AF_INET:
		if a, ok := netip.AddrFromSlice(b[4:min(len(b), 8)]); ok {
			return netip.AddrPortFrom(a, port)
		}
	case unix.AF_INET6:
		if len(b) >= 24 {
			if a, ok := netip.AddrFromSlice(b[8:24]); ok {
				return netip.AddrPortFrom(a, port)
			}
		}
	}
	return netip.AddrPort{}
}

// genlFamily asks generic netlink's controller for a family's id. A family
// whose module is not loaded is ENOENT.
func genlFamily(c *conn, name string) (uint16, error) {
	var e encoder
	e.genlmsghdr(unix.CTRL_CMD_GETFAMILY)
	e.attr(unix.CTRL_ATTR_FAMILY_NAME, append([]byte(name), 0))
	var id uint16
	err := c.exchange(unix.GENL_ID_CTRL, unix.NLM_F_ACK, e.b, func(m message) error {
		if len(m.data) < unix.GENL_HDRLEN {
			return errors.New("netlink: a short answer from the generic netlink controller")
		}
		as, err := attrs(m.data[unix.GENL_HDRLEN:])
		if err != nil {
			return err
		}
		for _, a := range as {
			if a.typ == unix.CTRL_ATTR_FAMILY_ID {
				id = u16(a.data)
			}
		}
		return nil
	})
	if err == nil && id == 0 {
		err = errors.New("netlink: the generic netlink controller named no family " + name)
	}
	return id, err
}
