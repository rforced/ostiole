package nft

import (
	"fmt"
	"slices"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// tunnelWay is a tunnel that a tunnel gateway sends rule traffic into, and
// the marks that route there: the gateway's own and those of the groups it
// is in, for the targets something routes through.
type tunnelWay struct {
	iface string
	zone  string
	marks []uint32
	// via names the targets, for the NAT page.
	via []string
}

// tunnelWays lists the tunnels behind tunnel gateways, in the order the
// configuration has the tunnels.
func (r *renderer) tunnelWays() []tunnelWay {
	byIface := map[string]*tunnelWay{}
	for _, t := range r.cfg.PolicyTargets() {
		if !r.targetUsed(t.Name) {
			continue
		}
		members := []string{t.Name}
		if t.Group {
			members = members[:0]
			if g, ok := r.cfg.GatewayGroup(t.Name); ok {
				for _, m := range g.Members {
					members = append(members, m.Gateway)
				}
			}
		}
		for _, name := range members {
			gw, ok := r.cfg.Gateway(name)
			if !ok || !gw.Enabled || !r.cfg.TunnelGateway(*gw) {
				continue
			}
			w := byIface[gw.Interface]
			if w == nil {
				w = &tunnelWay{iface: gw.Interface}
				byIface[gw.Interface] = w
			}
			if !slices.Contains(w.marks, t.Mark) {
				w.marks = append(w.marks, t.Mark)
				w.via = append(w.via, t.Name)
			}
		}
	}
	var out []tunnelWay
	for _, in := range r.cfg.Interfaces {
		if w := byIface[in.Name]; w != nil && in.Enabled {
			w.zone = in.Zone
			out = append(out, *w)
		}
	}
	return out
}

// targetUsed reports whether anything puts a gateway's or group's mark on
// traffic: a rule, or the resolvers' lookups.
func (r *renderer) targetUsed(name string) bool {
	return len(r.gatewayZones(name)) > 0 || (r.cfg.Services.DNS.Via == name && len(r.resolverUIDs) > 0)
}

// markMatch compares a masked mark with one value or several.
func markMatch(marks []uint32) string {
	if len(marks) == 1 {
		return fmt.Sprintf("== 0x%x", marks[0])
	}
	vals := make([]string, len(marks))
	for i, m := range marks {
		vals[i] = fmt.Sprintf("0x%x", m)
	}
	return "== { " + strings.Join(vals, ", ") + " }"
}

// tunnelNAT puts what rules send into a tunnel gateway's tunnel behind the
// tunnel's own address, in both families: the far end takes only the
// addresses it assigned. It is the gateway's doing, so it holds in every
// outbound mode, and it comes before the outbound rules, which would
// otherwise decide first.
func (r *renderer) tunnelNAT() {
	for _, w := range r.tunnelWays() {
		key := "tunnel-nat:" + w.iface
		r.line(fmt.Sprintf("oifname %s ct mark & 0x%08x %s counter masquerade comment %q",
			ifnameSet([]string{w.iface}), model.PolicyMarkMask, markMatch(w.marks), key))
		r.nat = append(r.nat, SystemNAT{
			Zone: w.zone, Interfaces: []string{w.iface},
			Source: "routed through " + strings.Join(w.via, ", "), Destination: "anywhere",
			Keys: []string{"nat_postrouting/" + key},
		})
	}
}

// clampTunnels lowers the segment size a TCP handshake offers into a
// tunnel gateway's tunnel to what fits in it. The LAN's hosts assume a
// 1500-byte path, and a site whose path MTU discovery is broken stalls
// on every large answer otherwise. It comes before the connection state,
// as the PPPoE clamp does.
func (r *renderer) clampTunnels() {
	var ifs []string
	for _, w := range r.tunnelWays() {
		ifs = append(ifs, w.iface)
	}
	if len(ifs) == 0 {
		return
	}
	r.line(fmt.Sprintf(`oifname %s tcp flags syn tcp option maxseg size set rt mtu counter comment "tunnel-mss"`, ifnameSet(ifs)))
	r.sys(SystemRule{
		Chain: "forward", Action: "continue", Protocol: string(model.ProtocolTCP),
		Source: "any", Destination: "out " + strings.Join(ifs, ", "),
		Description: "Shrink TCP segments to fit the tunnel",
		Keys:        []string{"forward/tunnel-mss"}, Setting: "routing",
	})
}
