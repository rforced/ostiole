package ddns

import (
	"net/netip"
	"testing"

	"golang.org/x/sys/unix"

	"ostiole/internal/dnsprovider"
	"ostiole/internal/netnstest"
)

func TestPickPublishesAStablePublicAddress(t *testing.T) {
	t.Parallel()
	a := func(s string, flags uint32) Addr { return Addr{IP: netip.MustParseAddr(s), Flags: flags} }
	for name, tc := range map[string]struct {
		addrs []Addr
		t     dnsprovider.RecordType
		want  string
	}{
		"private and CGNAT publish nothing": {[]Addr{a("192.168.1.2", 0), a("100.64.3.4", 0)}, dnsprovider.TypeA, ""},
		"a secondary is passed over":        {[]Addr{a("198.51.100.9", unix.IFA_F_SECONDARY), a("203.0.113.7", 0)}, dnsprovider.TypeA, "203.0.113.7"},
		"the lowest of two":                 {[]Addr{a("203.0.113.7", 0), a("198.51.100.9", 0)}, dnsprovider.TypeA, "198.51.100.9"},
		"no IPv4 for an A record":           {[]Addr{a("2001:db8::7", 0)}, dnsprovider.TypeA, ""},
		"a temporary IPv6 is passed over":   {[]Addr{a("2001:db8::abcd", unix.IFA_F_TEMPORARY), a("2001:db8::7", 0)}, dnsprovider.TypeAAAA, "2001:db8::7"},
		"deprecated, tentative and failed": {[]Addr{
			a("2001:db8::1", unix.IFA_F_DEPRECATED), a("2001:db8::2", unix.IFA_F_TENTATIVE), a("2001:db8::3", unix.IFA_F_DADFAILED),
		}, dnsprovider.TypeAAAA, ""},
		"link-local and ULA publish nothing": {[]Addr{a("fe80::1", 0), a("fd00::1", 0)}, dnsprovider.TypeAAAA, ""},
		"a mapped address is IPv4":           {[]Addr{a("::ffff:203.0.113.7", 0)}, dnsprovider.TypeA, "203.0.113.7"},
	} {
		got, ok := Pick(tc.addrs, tc.t)
		if (tc.want == "") == ok || (ok && got.String() != tc.want) {
			t.Errorf("%s: Pick = %v %v, want %q", name, got, ok, tc.want)
		}
	}
}

// Addresses reads the named link's addresses from the kernel and no one
// else's.
func TestAddressesReadsTheKernel(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	netnstest.Dummy(t, "wan0", "203.0.113.7/24", "198.51.100.9/24")
	netnstest.Dummy(t, "lan0", "192.168.1.1/24")
	got, err := Addresses("wan0")
	if err != nil {
		t.Fatal(err)
	}
	if ip, ok := Pick(got, dnsprovider.TypeA); !ok || ip.String() != "198.51.100.9" {
		t.Errorf("Pick(%v) = %v, want 198.51.100.9", got, ip)
	}
	for _, a := range got {
		if a.IP.String() == "192.168.1.1" {
			t.Errorf("lan0's address came back for wan0: %v", got)
		}
	}
	if _, err := Addresses("eth9"); err == nil {
		t.Error("a missing link gave no error")
	}
}
