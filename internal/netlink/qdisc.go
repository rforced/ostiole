package netlink

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Qdisc is one queueing discipline on a device.
type Qdisc struct {
	LinkIndex int
	Kind      string
	Handle    uint32
	Parent    uint32
}

// sizeofTcmsg is struct tcmsg, which x/sys/unix does not have.
const sizeofTcmsg = 20

// Qdiscs lists what is queued on one device.
func Qdiscs(index int) ([]Qdisc, error) {
	var e encoder
	e.tcmsg(index)
	msgs, err := dump(unix.RTM_GETQDISC, &e)
	if err != nil {
		return nil, err
	}
	var out []Qdisc
	for _, m := range msgs {
		if m.typ != unix.RTM_NEWQDISC {
			continue
		}
		q, err := parseQdisc(m.data)
		if err != nil {
			return nil, err
		}
		// The kernel dumps every device's.
		if q.LinkIndex == index {
			out = append(out, q)
		}
	}
	return out, nil
}

func parseQdisc(b []byte) (Qdisc, error) {
	if len(b) < sizeofTcmsg {
		return Qdisc{}, errors.New("netlink: a short qdisc")
	}
	q := Qdisc{
		LinkIndex: int(native.Uint32(b[4:])),
		Handle:    native.Uint32(b[8:]),
		Parent:    native.Uint32(b[12:]),
	}
	as, err := attrs(b[sizeofTcmsg:])
	if err != nil {
		return Qdisc{}, err
	}
	for _, a := range as {
		if a.typ == unix.TCA_KIND {
			q.Kind = cstring(a.data)
		}
	}
	return q, nil
}
