package nft

import (
	"fmt"
	"strings"
)

// Bootstrap renders the ruleset a router runs between being installed
// and its first confirmed apply. The installer retires the old firewall
// as part of installing, and a router should never be without one, so
// this goes into the kernel first: it keeps the session the install is
// driven from and the ports the wizard is reached on, and nothing else.
//
// Input accepts what an unconfigured router has to accept — its own
// loopback, the replies to what it started, ICMP, the DHCP and DHCPv6
// replies to its own lease requests, and the management ports from
// anywhere, since the wizard has not yet said which side is inside — and
// drops the rest. Nothing is forwarded: a firewall that does not know its
// zones yet has no business passing traffic between them. It is the same
// table the real ruleset uses, so the first apply replaces it atomically.
func Bootstrap(ports []uint16) string {
	seen := map[uint16]bool{}
	var list []string
	for _, p := range ports {
		if p != 0 && !seen[p] {
			seen[p] = true
			list = append(list, fmt.Sprint(p))
		}
	}
	var b strings.Builder
	b.WriteString(Header + "\n")
	b.WriteString("# Bootstrap ruleset written by the installer; the first apply replaces it.\n")
	b.WriteString("table " + Table + "\n")
	b.WriteString("delete table " + Table + "\n")
	b.WriteString("table " + Table + " {\n")
	b.WriteString("\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority filter; policy drop;\n")
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tct state invalid drop\n")
	b.WriteString("\t\tct state established,related accept\n")
	b.WriteString("\t\tmeta l4proto { icmp, ipv6-icmp } accept comment \"bootstrap:icmp\"\n")
	b.WriteString("\t\tudp dport 68 accept comment \"bootstrap:dhcp-client\"\n")
	b.WriteString("\t\tudp dport 546 accept comment \"bootstrap:dhcpv6-client\"\n")
	if len(list) > 0 {
		b.WriteString("\t\ttcp dport " + setOrSingle(list) + " accept comment \"bootstrap:management\"\n")
	}
	b.WriteString("\t}\n")
	b.WriteString("\tchain forward {\n")
	b.WriteString("\t\ttype filter hook forward priority filter; policy drop;\n")
	b.WriteString("\t}\n")
	b.WriteString("\tchain output {\n")
	b.WriteString("\t\ttype filter hook output priority filter; policy accept;\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String()
}
