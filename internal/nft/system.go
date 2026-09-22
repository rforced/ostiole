package nft

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// SystemRule is a rule Ostiole writes on its own account: the baseline that
// keeps IP working, anti-lockout, the services this router runs, DNS
// enforcement, and the drop at the end. The rules page shows them around
// the operator's rules, in the order the kernel meets them, so nothing that
// decides a packet's fate is hidden.
//
// The renderer records a row where it writes the nft line, so a row can
// only describe a rule that is in the ruleset. A rule both base chains
// carry (the connection state checks, the blocked sources) is recorded once,
// from input, with the counter keys of both.
type SystemRule struct {
	// Chain the rule is in. Rows are ordered by the hook the chain hangs
	// off: nat_prerouting first, then input, block_dns (jumped to from
	// forward), forward, and last the tail of the zone chain.
	Chain string `json:"chain"`
	// After marks the rows evaluated after the zone's own rules.
	After bool `json:"after,omitempty"`
	// Zones whose traffic the rule can match. Empty means every zone.
	Zones []string `json:"zones,omitempty"`
	// Action is accept, drop, redirect, or continue: the rule matched and
	// evaluation carries on with the next one.
	Action      string `json:"action"`
	Protocol    string `json:"protocol"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Description string `json:"description"`
	Log         bool   `json:"log,omitempty"`
	// Keys the rule's packets are counted under, as ParseCounters names
	// them. Empty when the rule keeps no counter.
	Keys []string `json:"keys,omitempty"`
	// Setting names what controls the rule, so the page can link to it:
	// zone, interface, dhcp, dns, enforcement, upnp, proxy, wireguard,
	// tailscale, or nat. Empty for the baseline, which nothing turns off.
	Setting string `json:"setting,omitempty"`
}

// Rendered is a ruleset and the system rules it carries.
type Rendered struct {
	Ruleset string       `json:"ruleset"`
	System  []SystemRule `json:"system"`
}

// Build renders cfg and reports the system rules that went into it, in the
// order the kernel evaluates them.
func Build(cfg *model.Config, feeds map[string][]string) (*Rendered, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	r := &renderer{cfg: cfg, feeds: feeds}
	r.render()
	sort.SliceStable(r.system, func(i, j int) bool {
		return chainRank(r.system[i].Chain) < chainRank(r.system[j].Chain)
	})
	return &Rendered{Ruleset: r.b.String(), System: r.system}, nil
}

// SystemRules is Build without the ruleset, for showing the rows.
func SystemRules(cfg *model.Config, feeds map[string][]string) ([]SystemRule, error) {
	out, err := Build(cfg, feeds)
	if err != nil {
		return nil, err
	}
	return out.System, nil
}

// chainRank orders chains by the hook they hang off, so the rows read in
// the order a packet meets them: NAT before routing, then the base chain,
// with block_dns where forward jumps to it, and the zone tail last.
func chainRank(chain string) int {
	switch chain {
	case "nat_prerouting":
		return 0
	case "input":
		return 1
	case BlockChain:
		return 2
	case "forward":
		return 3
	default:
		return 4
	}
}

// sys records a row that matches every zone.
func (r *renderer) sys(row SystemRule) {
	r.system = append(r.system, row)
}

// sysFor records a row scoped to the zones the interfaces belong to. An
// interface in no zone has no tab to show the row on, so a rule that
// matches only such interfaces is not recorded rather than shown everywhere.
func (r *renderer) sysFor(ifs []string, row SystemRule) {
	zones := r.zonesOf(ifs)
	if len(zones) == 0 {
		return
	}
	row.Zones = zones
	r.sys(row)
}

// zonesOf names the zones the interfaces belong to, in configuration order.
func (r *renderer) zonesOf(ifs []string) []string {
	want := map[string]bool{}
	for _, name := range ifs {
		if in, ok := r.cfg.Interface(name); ok && in.Zone != "" {
			want[in.Zone] = true
		}
	}
	var out []string
	for _, z := range r.cfg.Zones {
		if want[z.Name] {
			out = append(out, z.Name)
		}
	}
	return out
}

// firewallDest is the destination text of an input-chain rule: whatever it
// matches, the packet is addressed to this router.
func firewallDest(ports []string) string {
	if len(ports) == 0 {
		return "this firewall"
	}
	return "this firewall : " + strings.Join(ports, ", ")
}

// exemptSource is the source text of a DNS enforcement row: the exempt
// clients return before the rule, so to the reader they are excluded.
func (r *renderer) exemptSource() string {
	if len(r.exemptMatches("saddr")) == 0 {
		return "any"
	}
	return "not @" + r.cfg.Blocking.Enforce.ExemptAlias
}

// zoneLogsDrops reports whether a packet this firewall drops in the zone is
// logged: because the zone says so, or because every interface in it does.
//
// It answers for every drop in the zone, not only the one at the end of its
// chain. Ostiole's own drops — encrypted DNS, the flood and scan defence —
// are terminal and sit earlier, so a packet they take never reaches an end
// to fall off; asking this at each of them is what makes the setting mean
// "log the drops in this zone" rather than "log one particular rule".
func (r *renderer) zoneLogsDrops(z model.Zone) bool {
	if z.LogDrops {
		return true
	}
	ifs := r.cfg.ZoneInterfaces(z.Name)
	if len(ifs) == 0 {
		return false
	}
	def := r.cfg.System.Management.LogDefaultDrops
	for _, name := range ifs {
		if in, ok := r.cfg.Interface(name); !ok || !in.LogsDrops(def) {
			return false
		}
	}
	return true
}

// zoneNamedLogsDrops is zoneLogsDrops for a zone known only by name. A name
// that is not a zone logs nothing.
func (r *renderer) zoneNamedLogsDrops(name string) bool {
	z, ok := r.cfg.Zone(name)
	if !ok {
		return false
	}
	return r.zoneLogsDrops(*z)
}

// loggingInterfaces narrows ifs to those whose zone logs its drops, in the
// order given. A chain several zones share cannot decide this per rule, so
// the log statement carries the set and the drop beside it does not.
func (r *renderer) loggingInterfaces(ifs []string) []string {
	var out []string
	for _, name := range ifs {
		if in, ok := r.cfg.Interface(name); ok && r.zoneNamedLogsDrops(in.Zone) {
			out = append(out, name)
		}
	}
	return out
}

// SystemLogRate is how often one of Ostiole's own drops may log. Every
// packet is still dropped and still counted; only the recording is sampled.
//
// Without it a single source can fill the log: a scanner in the hold set
// has every packet dropped by one rule for the whole hold window, and at a
// few thousand packets a second it would evict everything else in the ring
// before an operator could read any of it. The counter on the rule remains
// the true figure.
const SystemLogRate = "10/second"

// systemLogLine is the log statement in front of one of Ostiole's own
// drops: sampled, non-terminal, so the drop below it still decides the
// packet and still counts every one. iifnames scopes it to the interfaces
// whose zone logs; empty means the rule is already in a chain that only
// those interfaces reach.
//
// A separate rule rather than a log statement inside the drop, because
// `limit` is a match: were it in the drop rule, a packet over the sampling
// rate would fail the match and not be dropped at all.
func systemLogLine(match, iifnames, kind string) string {
	var b strings.Builder
	if iifnames != "" {
		fmt.Fprintf(&b, "iifname %s ", iifnames)
	}
	if match != "" {
		b.WriteString(match + " ")
	}
	fmt.Fprintf(&b, `limit rate %s log prefix "ostiole:s:%s:drop: " group %d comment "log:%s"`,
		SystemLogRate, kind, LogGroup, kind)
	return b.String()
}

// forwardedZones lists the zones whose traffic can arrive already
// translated by a port forward or a 1:1 mapping, including the internal
// zones a reflected forward serves.
func (r *renderer) forwardedZones() []string {
	var ifs []string
	for _, pf := range r.cfg.NAT.PortForwards {
		if !pf.Enabled {
			continue
		}
		zone := r.cfg.ZoneInterfaces(pf.Zone)
		if len(zone) == 0 {
			continue
		}
		ifs = append(ifs, zone...)
		if pf.Reflection {
			ifs = append(ifs, r.internalInterfaces()...)
		}
	}
	for _, o := range r.cfg.NAT.OneToOne {
		if o.Enabled {
			ifs = append(ifs, r.cfg.ZoneInterfaces(o.Zone)...)
		}
	}
	return r.zonesOf(ifs)
}
