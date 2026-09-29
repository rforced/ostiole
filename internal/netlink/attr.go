package netlink

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"

	"golang.org/x/sys/unix"
)

// attr is one attribute: its type, without the nested and byte-order
// flags, and its payload.
type attr struct {
	typ  uint16
	data []byte
}

// typeMask takes the flags off an attribute's type.
const typeMask = ^uint16(unix.NLA_F_NESTED | unix.NLA_F_NET_BYTEORDER)

// align rounds a length up to the four bytes netlink pads everything to.
func align(n int) int { return (n + unix.NLA_ALIGNTO - 1) &^ (unix.NLA_ALIGNTO - 1) }

// attrs splits a run of attributes.
func attrs(b []byte) ([]attr, error) {
	var out []attr
	for len(b) >= 4 {
		n := int(native.Uint16(b))
		if n < 4 || n > len(b) {
			return nil, fmt.Errorf("netlink: an attribute of %d bytes in %d", n, len(b))
		}
		out = append(out, attr{typ: native.Uint16(b[2:]) & typeMask, data: b[4:n]})
		b = b[min(align(n), len(b)):]
	}
	return out, nil
}

// u16, u32 and u64 read host-order attributes, zero when one is short.
func u16(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return native.Uint16(b)
}

func u32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return native.Uint32(b)
}

func u64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return native.Uint64(b)
}

// be16, be32 and be64 read netfilter's big-endian attributes.
func be16(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}

func be32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func be64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// ip copies an address out of a datagram.
func ip(b []byte) net.IP {
	if len(b) != net.IPv4len && len(b) != net.IPv6len {
		return nil
	}
	return net.IP(append([]byte(nil), b...))
}

// family says which address family an address belongs to.
func family(a net.IP) int {
	if a.To4() != nil {
		return unix.AF_INET
	}
	return unix.AF_INET6
}

// encoder builds a request: a fixed header, then attributes. The first
// value that does not fit its field is kept in err, and the request is
// never sent.
type encoder struct {
	b   []byte
	err error
}

func (e *encoder) fail(format string, args ...any) {
	if e.err == nil {
		e.err = fmt.Errorf("netlink: "+format, args...)
	}
}

// attr adds one attribute.
func (e *encoder) attr(typ uint16, data []byte) {
	n := 4 + len(data)
	if n > math.MaxUint16 {
		e.fail("an attribute of %d bytes", n)
		return
	}
	e.b = native.AppendUint16(e.b, uint16(n))
	e.b = native.AppendUint16(e.b, typ)
	e.b = append(e.b, data...)
	e.pad()
}

func (e *encoder) pad() {
	for len(e.b)%unix.NLA_ALIGNTO != 0 {
		e.b = append(e.b, 0)
	}
}

// ifname adds a device name, with the NUL the kernel expects of one.
func (e *encoder) ifname(name string) { e.attr(unix.IFLA_IFNAME, append([]byte(name), 0)) }

func (e *encoder) u32(typ uint16, v uint32) { e.attr(typ, native.AppendUint32(nil, v)) }

// ip adds an address in the length its family takes.
func (e *encoder) ip(typ uint16, fam int, a net.IP) {
	if fam == unix.AF_INET {
		a = a.To4()
	} else {
		a = a.To16()
	}
	if a == nil {
		e.fail("an address that is not of family %d", fam)
		return
	}
	e.attr(typ, a)
}

// nest adds an attribute holding whatever fill adds.
func (e *encoder) nest(typ uint16, fill func()) {
	start := len(e.b)
	e.b = append(e.b, 0, 0, 0, 0)
	fill()
	e.patchLength(start)
	native.PutUint16(e.b[start+2:], typ)
}

// patchLength writes the length of everything added since start into the
// 16 bits at start.
func (e *encoder) patchLength(start int) {
	n := len(e.b) - start
	if n < 0 || n > math.MaxUint16 {
		e.fail("%d bytes in a 16-bit length", n)
		return
	}
	native.PutUint16(e.b[start:], uint16(n))
}

// n32 narrows a value a request carries in 32 bits: an index, a table, a
// metric. One that does not fit fails the request.
func (e *encoder) n32(v int, what string) uint32 {
	if v < 0 || v > math.MaxUint32 {
		e.fail("%s %d does not fit 32 bits", what, v)
		return 0
	}
	return uint32(v)
}

// n8 narrows a value a request carries in 8 bits.
func (e *encoder) n8(v int, what string) uint8 {
	if v < 0 || v > math.MaxUint8 {
		e.fail("%s %d does not fit 8 bits", what, v)
		return 0
	}
	return uint8(v)
}

// ifinfomsg adds struct ifinfomsg.
func (e *encoder) ifinfomsg(index int, flags, change uint32) {
	e.b = append(e.b, unix.AF_UNSPEC, 0, 0, 0)
	e.b = native.AppendUint32(e.b, e.n32(index, "link index"))
	e.b = native.AppendUint32(e.b, flags)
	e.b = native.AppendUint32(e.b, change)
}

// ifaddrmsg adds struct ifaddrmsg.
func (e *encoder) ifaddrmsg(fam, prefixlen, index int) {
	e.b = append(e.b, e.n8(fam, "family"), e.n8(prefixlen, "prefix length"), 0, 0)
	e.b = native.AppendUint32(e.b, e.n32(index, "link index"))
}

// ndmsg adds struct ndmsg.
func (e *encoder) ndmsg(fam, index, state, flags int) {
	e.b = append(e.b, e.n8(fam, "family"), 0, 0, 0)
	e.b = native.AppendUint32(e.b, e.n32(index, "link index"))
	if state < 0 || state > math.MaxUint16 {
		e.fail("neighbour state %d", state)
	}
	e.b = native.AppendUint16(e.b, uint16(state)) //nolint:gosec // checked above
	e.b = append(e.b, e.n8(flags, "neighbour flags"), 0)
}

// tcmsg adds struct tcmsg.
func (e *encoder) tcmsg(index int) {
	e.b = append(e.b, unix.AF_UNSPEC, 0, 0, 0)
	e.b = native.AppendUint32(e.b, e.n32(index, "link index"))
	e.b = append(e.b, make([]byte, 12)...)
}

// genlmsghdr adds struct genlmsghdr. Both families asked here, the
// controller and WireGuard, take version 1.
func (e *encoder) genlmsghdr(cmd uint8) { e.b = append(e.b, cmd, 1, 0, 0) }

// nfgenmsg adds struct nfgenmsg, whose resource id is big-endian.
func (e *encoder) nfgenmsg(fam int, res uint16) {
	e.b = append(e.b, e.n8(fam, "family"), unix.NFNETLINK_V0)
	e.b = binary.BigEndian.AppendUint16(e.b, res)
}
