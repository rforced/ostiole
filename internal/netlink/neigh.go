package netlink

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// Neighbour is one entry of the ARP or NDP table.
type Neighbour struct {
	LinkIndex int
	Family    int
	// State is the kernel's NUD_* bits, Flags its NTF_* ones.
	State        int
	Flags        int
	IP           net.IP
	HardwareAddr net.HardwareAddr
	// Confirmed is how long ago the neighbour last answered, in the
	// kernel's USER_HZ clock ticks.
	Confirmed uint32
}

// Neighbours lists the neighbour table of one family.
func Neighbours(fam int) ([]Neighbour, error) {
	var e encoder
	e.ndmsg(fam, 0, 0, 0)
	msgs, err := dump(unix.RTM_GETNEIGH, &e)
	if err != nil {
		return nil, err
	}
	var out []Neighbour
	for _, m := range msgs {
		if m.typ != unix.RTM_NEWNEIGH {
			continue
		}
		n, err := parseNeighbour(m.data)
		if err != nil {
			return nil, err
		}
		if fam == unix.AF_UNSPEC || n.Family == fam {
			out = append(out, n)
		}
	}
	return out, nil
}

func parseNeighbour(b []byte) (Neighbour, error) {
	if len(b) < unix.SizeofNdMsg {
		return Neighbour{}, errors.New("netlink: a short neighbour")
	}
	n := Neighbour{
		Family:    int(b[0]),
		LinkIndex: int(native.Uint32(b[4:])),
		State:     int(native.Uint16(b[8:])),
		Flags:     int(b[10]),
	}
	as, err := attrs(b[unix.SizeofNdMsg:])
	if err != nil {
		return Neighbour{}, err
	}
	for _, a := range as {
		switch a.typ {
		case unix.NDA_DST:
			n.IP = ip(a.data)
		case unix.NDA_LLADDR:
			n.HardwareAddr = net.HardwareAddr(append([]byte(nil), a.data...))
		case unix.NDA_CACHEINFO:
			// struct nda_cacheinfo opens with ndm_confirmed.
			n.Confirmed = u32(a.data)
		}
	}
	return n, nil
}
