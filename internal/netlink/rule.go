package netlink

import (
	"errors"
	"net/netip"

	"golang.org/x/sys/unix"
)

// Rule is one ip rule that sends the packets it matches to a table.
type Rule struct {
	Family   int
	Priority int
	// Mark is matched under Mask. A Mask of zero sends none.
	Mark uint32
	Mask uint32
	// Src matches the packet's source address; the zero prefix matches
	// any.
	Src netip.Prefix
	// IPProto and Sport match the transport: a protocol, and the source
	// ports within it. Zero matches any.
	IPProto uint8
	Sport   PortRange
	Table   int
	// SuppressPrefixlen hides routes with a prefix this long or shorter
	// from the lookup, so 0 hides the default route alone. -1, which
	// NewRule sets, hides nothing.
	SuppressPrefixlen int
}

// PortRange is the ports from Start to End, both included.
type PortRange struct{ Start, End uint16 }

// NewRule returns a rule that suppresses nothing.
func NewRule() Rule { return Rule{SuppressPrefixlen: -1} }

// Rules lists the rules of one family.
func Rules(fam int) ([]Rule, error) {
	var e encoder
	e.rtmsg(rtmsg{family: e.n8(fam, "family")})
	msgs, err := dump(unix.RTM_GETRULE, &e)
	if err != nil {
		return nil, err
	}
	var out []Rule
	for _, m := range msgs {
		if m.typ != unix.RTM_NEWRULE {
			continue
		}
		r, err := parseRule(m.data)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// AddRule adds a rule that looks up its table.
func AddRule(r Rule) error {
	var e encoder
	e.rule(r)
	return request(unix.RTM_NEWRULE, unix.NLM_F_CREATE|unix.NLM_F_EXCL, &e, nil)
}

// DeleteRule removes the rule that matches r.
func DeleteRule(r Rule) error {
	var e encoder
	e.rule(r)
	return request(unix.RTM_DELRULE, 0, &e, nil)
}

// rule adds struct fib_rule_hdr and a rule's attributes.
func (e *encoder) rule(r Rule) {
	h := rtmsg{family: e.n8(r.Family, "family"), typ: unix.FR_ACT_TO_TBL}
	if r.Table < 256 {
		h.table = e.n8(r.Table, "table")
	}
	if r.Src.IsValid() {
		h.srcLen = e.n8(r.Src.Bits(), "source prefix length")
	}
	e.rtmsg(h)
	e.u32(unix.FRA_TABLE, e.n32(r.Table, "table"))
	// Without one the kernel picks a priority; 0 is the local table's.
	if r.Priority > 0 {
		e.u32(unix.FRA_PRIORITY, e.n32(r.Priority, "rule priority"))
	}
	if r.Src.IsValid() {
		e.attr(unix.FRA_SRC, r.Src.Addr().Unmap().AsSlice())
	}
	if r.Mark != 0 || r.Mask != 0 {
		e.u32(unix.FRA_FWMARK, r.Mark)
	}
	if r.Mask != 0 {
		e.u32(unix.FRA_FWMASK, r.Mask)
	}
	if r.IPProto != 0 {
		e.attr(unix.FRA_IP_PROTO, []byte{r.IPProto})
	}
	// struct fib_rule_port_range, in host order.
	if r.Sport != (PortRange{}) {
		e.attr(unix.FRA_SPORT_RANGE, native.AppendUint16(native.AppendUint16(nil, r.Sport.Start), r.Sport.End))
	}
	if r.SuppressPrefixlen >= 0 {
		e.u32(unix.FRA_SUPPRESS_PREFIXLEN, e.n32(r.SuppressPrefixlen, "suppressed prefix length"))
	}
}

func parseRule(b []byte) (Rule, error) {
	if len(b) < unix.SizeofRtMsg {
		return Rule{}, errors.New("netlink: a short rule")
	}
	r := NewRule()
	r.Family, r.Table = int(b[0]), int(b[4])
	srcLen := int(b[2])
	as, err := attrs(b[unix.SizeofRtMsg:])
	if err != nil {
		return Rule{}, err
	}
	for _, a := range as {
		switch a.typ {
		case unix.FRA_TABLE:
			r.Table = int(u32(a.data))
		case unix.FRA_PRIORITY:
			r.Priority = int(u32(a.data))
		case unix.FRA_SRC:
			addr, ok := netip.AddrFromSlice(a.data)
			if !ok {
				return Rule{}, errors.New("netlink: a rule's source of the wrong length")
			}
			if r.Src, err = addr.Prefix(srcLen); err != nil {
				return Rule{}, err
			}
		case unix.FRA_FWMARK:
			r.Mark = u32(a.data)
		case unix.FRA_FWMASK:
			r.Mask = u32(a.data)
		case unix.FRA_IP_PROTO:
			if len(a.data) > 0 {
				r.IPProto = a.data[0]
			}
		case unix.FRA_SPORT_RANGE:
			if len(a.data) >= 4 {
				r.Sport = PortRange{native.Uint16(a.data), native.Uint16(a.data[2:])}
			}
		case unix.FRA_SUPPRESS_PREFIXLEN:
			r.SuppressPrefixlen = int(int32(u32(a.data))) //nolint:gosec // the kernel's int, as it wrote it
		}
	}
	return r, nil
}
