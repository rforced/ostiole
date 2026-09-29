package nft

import (
	"fmt"
	"strings"

	"github.com/rforced/ostiole/internal/model"
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
	return minimal("# Bootstrap ruleset written by the installer; the first apply replaces it.",
		"bootstrap", ports, nil)
}

// Fallback renders what goes into the kernel when the saved ruleset will
// not, so a router never runs without a firewall. It accepts what
// Bootstrap accepts and forwards nothing. With a configuration, the
// management ports are the ones it names, open only on the interfaces of
// its anti-lockout zones, which also answer DHCP so a client can get an
// address to reach the UI from. Without one, or with no marked zone that
// has an interface, the ports are open from anywhere, as on a router that
// was just installed.
func Fallback(cfg *model.Config, ports []uint16) string {
	var ifaces []string
	if cfg != nil {
		m := cfg.System.Management
		if m.WebPort != 0 || m.SSHPort != 0 {
			ports = []uint16{m.WebPort, m.SSHPort}
		}
		for _, z := range cfg.Zones {
			if z.AntiLockout {
				ifaces = append(ifaces, cfg.ZoneInterfaces(z.Name)...)
			}
		}
	}
	return minimal("# Fallback ruleset: the saved one did not load. The next apply replaces it.",
		"fallback", ports, ifaces)
}

// minimal renders the input-only table Bootstrap and Fallback share. With
// ifaces, management is accepted on them alone, and DHCP with it.
func minimal(title, tag string, ports []uint16, ifaces []string) string {
	seen := map[uint16]bool{}
	var list []string
	for _, p := range ports {
		if p != 0 && !seen[p] {
			seen[p] = true
			list = append(list, fmt.Sprint(p))
		}
	}
	from := ""
	if len(ifaces) > 0 {
		from = "iifname " + ifnameSet(ifaces) + " "
	}
	var b strings.Builder
	b.WriteString(Header + "\n")
	b.WriteString(title + "\n")
	b.WriteString("table " + Table + "\n")
	b.WriteString("delete table " + Table + "\n")
	b.WriteString("table " + Table + " {\n")
	b.WriteString("\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority filter; policy drop;\n")
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tct state invalid drop\n")
	b.WriteString("\t\tct state established,related accept\n")
	b.WriteString("\t\tmeta l4proto { icmp, ipv6-icmp } accept comment \"" + tag + ":icmp\"\n")
	b.WriteString("\t\tudp dport 68 accept comment \"" + tag + ":dhcp-client\"\n")
	b.WriteString("\t\tudp dport 546 accept comment \"" + tag + ":dhcpv6-client\"\n")
	if len(list) > 0 {
		b.WriteString("\t\t" + from + "tcp dport " + setOrSingle(list) + " accept comment \"" + tag + ":management\"\n")
	}
	if from != "" {
		b.WriteString("\t\t" + from + "udp dport { 67, 547 } accept comment \"" + tag + ":dhcp\"\n")
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
