package ddns

import (
	"net/netip"

	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/dnsprovider"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
)

// Addr is one address on an interface, with the kernel's IFA_F_* flags.
type Addr struct {
	IP    netip.Addr
	Flags uint32
}

// Addresses reads the addresses on the named interface from the kernel.
func Addresses(iface string) ([]Addr, error) {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return nil, err
	}
	all, err := netlink.Addrs(unix.AF_UNSPEC)
	if err != nil {
		return nil, err
	}
	var out []Addr
	for _, a := range all {
		if a.LinkIndex != link.Index || a.IPNet == nil {
			continue
		}
		if ip, ok := netip.AddrFromSlice(a.IPNet.IP); ok {
			out = append(out, Addr{IP: ip.Unmap(), Flags: a.Flags})
		}
	}
	return out, nil
}

// unusable6 are the IPv6 addresses a record must not name: a temporary one
// is replaced every day, a deprecated one is on its way out, and the rest
// are not usable yet or never will be.
const unusable6 = unix.IFA_F_TEMPORARY | unix.IFA_F_DEPRECATED | unix.IFA_F_TENTATIVE | unix.IFA_F_DADFAILED

// Pick returns the address a record of type t publishes: a public address
// of the family, for IPv4 a primary one. Of several, the lowest, so two
// never take turns.
func Pick(addrs []Addr, t dnsprovider.RecordType) (netip.Addr, bool) {
	var best netip.Addr
	for _, a := range addrs {
		ip := a.IP.Unmap()
		switch t {
		case dnsprovider.TypeA:
			// For IPv4 the same bit is IFA_F_SECONDARY: another address in
			// a network the interface already has.
			if !ip.Is4() || a.Flags&unix.IFA_F_SECONDARY != 0 {
				continue
			}
		case dnsprovider.TypeAAAA:
			if !ip.Is6() || a.Flags&unusable6 != 0 {
				continue
			}
		default:
			continue
		}
		if model.PublicAddress(ip) && (!best.IsValid() || ip.Less(best)) {
			best = ip
		}
	}
	return best, best.IsValid()
}
