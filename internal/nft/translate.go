package nft

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// A tunnel's peers can show networks under other prefixes, for two sites
// numbered alike (WireGuardPeer.Theirs and Ours). One end does all of it:
//
//   - Theirs: this side reaches the peer's network at the shown prefix. A
//     connection to it is marked, its destination mapped back to the real
//     network, and the mark's table sends that into the tunnel, since the
//     main table would answer this side's own network. What the peer's
//     hosts send here comes from the shown prefix, and the router's own
//     services see it so too.
//   - Ours: the peer reaches this side's network at the shown prefix, and
//     what this side sends into the tunnel leaves from it.
//
// Every translation is conntrack's, so it holds for a whole connection
// both ways.

// translation is a tunnel whose peers show networks, as the firewall
// writes it.
type translation struct {
	iface string
	zone  string
	// mark routes the peers' real networks into the tunnel; zero when no
	// peer shows a network of its own, only this side's.
	mark  uint32
	peers []peerMaps
}

type peerMaps struct {
	name         string
	theirs, ours []model.PrefixMap
}

// translations lists the tunnels whose enabled peers show networks, in
// the order the configuration has them.
func (r *renderer) translations() []translation {
	marks := map[string]uint32{}
	for _, t := range r.cfg.TranslateTargets() {
		marks[t.Tunnel] = t.Mark
	}
	var out []translation
	for _, in := range r.cfg.Interfaces {
		if !in.Enabled || in.WireGuard == nil {
			continue
		}
		tr := translation{iface: in.Name, zone: in.Zone, mark: marks[in.Name]}
		for _, p := range in.WireGuard.Peers {
			if !p.Enabled {
				continue
			}
			pm := peerMaps{name: p.Name, theirs: model.ParseNetMaps(p.Theirs), ours: model.ParseNetMaps(p.Ours)}
			if len(pm.theirs)+len(pm.ours) > 0 {
				tr.peers = append(tr.peers, pm)
			}
		}
		if len(tr.peers) > 0 {
			out = append(out, tr)
		}
	}
	return out
}

// shown lists the prefixes one side's maps show, as nft writes a set.
func (tr translation) shown(ours bool) []string {
	var out []string
	for _, p := range tr.peers {
		maps := p.theirs
		if ours {
			maps = p.ours
		}
		for _, m := range maps {
			out = append(out, m.Shown.String())
		}
	}
	return out
}

// allTheirs is every tunnel's shown prefixes for the peers' networks.
func (r *renderer) allTheirs() []string {
	var out []string
	for _, tr := range r.translations() {
		out = append(out, tr.shown(false)...)
	}
	return out
}

// markLine marks what goes to a tunnel's shown networks, in a chain of
// prerouting or of output, so the tunnel's table routes it.
func (tr translation) markLine() string {
	return fmt.Sprintf(`ip daddr %s meta mark set meta mark & 0x%08x | 0x%x counter comment %q`,
		setOrSingle(tr.shown(false)), ^uint32(model.PolicyMarkMask), tr.mark, "translate:"+tr.iface)
}

// translateChains mark connections to the shown networks before the
// routing decision, for forwarded traffic and for the router's own
// answers, and keep anything but the tunnel from sending to this side's
// shown networks: a host here that did could pass itself off as an
// answer from the far end.
func (r *renderer) translateChains() {
	trs := r.translations()
	var marked, guarded []translation
	for _, tr := range trs {
		if tr.mark != 0 {
			marked = append(marked, tr)
		}
		if len(tr.shown(true)) > 0 {
			guarded = append(guarded, tr)
		}
	}
	if len(marked) > 0 {
		r.block("chain translate", func() {
			r.line("type filter hook prerouting priority mangle; policy accept;")
			for _, tr := range marked {
				r.line(tr.markLine())
			}
		})
		r.block("chain translate_output", func() {
			r.line("type route hook output priority mangle; policy accept;")
			for _, tr := range marked {
				r.line(tr.markLine())
			}
		})
	}
	if len(guarded) == 0 {
		return
	}
	r.block("chain translate_guard", func() {
		r.line("type filter hook prerouting priority raw; policy accept;")
		for _, tr := range guarded {
			key := "translate-guard:" + tr.iface
			r.line(fmt.Sprintf(`iifname != %q ip daddr %s counter drop comment %q`,
				tr.iface, setOrSingle(tr.shown(true)), key))
			r.sys(SystemRule{
				Chain: "translate_guard", Action: "drop", Protocol: string(model.ProtocolAny),
				Source: "anything but " + tr.iface, Destination: strings.Join(tr.shown(true), ", "),
				Description: "Keep all but " + tr.iface + " off the networks this side shows its peers",
				Keys:        []string{"translate_guard/" + key}, Setting: "wireguard",
			})
		}
	})
}

// translateHold drops what is marked for a tunnel's shown networks but
// would leave by another interface: before the daemon's first pass, or
// while networkd has just flushed the tunnel's table, the mark routes
// nowhere and the real network is this side's own.
func (r *renderer) translateHold(chain string) {
	for _, tr := range r.translations() {
		if tr.mark == 0 {
			continue
		}
		key := "translate-hold:" + tr.iface
		r.line(fmt.Sprintf(`meta mark & 0x%08x == 0x%x oifname != %q counter drop comment %q`,
			model.PolicyMarkMask, tr.mark, tr.iface, key))
		r.sys(SystemRule{
			Chain: chain, Action: "drop", Protocol: string(model.ProtocolAny),
			Source: "any", Destination: strings.Join(tr.shown(false), ", ") + " out anything but " + tr.iface,
			Description: "Keep traffic for " + tr.iface + "'s shown networks in the tunnel",
			Keys:        []string{chain + "/" + key}, Setting: "wireguard",
		})
	}
}

// translateDNAT maps the shown networks back to the real ones as a
// connection starts: theirs from anywhere but the tunnel, ours from the
// tunnel only. An address of theirs that is also this router's own on
// the shared network is left alone: mapped back, the router would answer
// it itself, so it is not reachable here, and the far end is reached at
// its tunnel address instead.
func (r *renderer) translateDNAT() {
	own := r.routerAddresses()
	for _, tr := range r.translations() {
		for _, p := range tr.peers {
			key := "translate:" + model.PeerRef(tr.iface, p.name)
			for _, m := range p.theirs {
				except := ""
				if skip := shownOwn(m, own); len(skip) > 0 {
					except = "ip daddr != " + setOrSingle(skip) + " "
				}
				r.line(fmt.Sprintf(`iifname != %q ip daddr %s %scounter dnat ip prefix to %s comment %q`,
					tr.iface, m.Shown, except, m.Real, key))
			}
			for _, m := range p.ours {
				r.line(fmt.Sprintf(`iifname %q ip daddr %s counter dnat ip prefix to %s comment %q`,
					tr.iface, m.Shown, m.Real, key))
			}
			if len(p.theirs)+len(p.ours) > 0 {
				r.sys(SystemRule{
					Chain: "nat_prerouting", Action: "redirect", Protocol: string(model.ProtocolAny),
					Source: "any", Destination: mapsText(p),
					Description: "Map the networks " + model.PeerRef(tr.iface, p.name) + " shows back to the real ones",
					Keys:        []string{"nat_prerouting/" + key}, Setting: "wireguard",
				})
			}
		}
	}
}

// translateSNAT gives connections leaving into the tunnel from this
// side's network, and those arriving from the peer's, the shown source.
// It comes before the other outbound NAT, which would decide first.
func (r *renderer) translateSNAT() {
	for _, tr := range r.translations() {
		for _, p := range tr.peers {
			key := "translate:" + model.PeerRef(tr.iface, p.name)
			for _, m := range p.ours {
				r.line(fmt.Sprintf(`oifname %q ip saddr %s counter snat ip prefix to %s comment %q`,
					tr.iface, m.Real, m.Shown, key))
			}
			for _, m := range p.theirs {
				r.line(fmt.Sprintf(`iifname %q ip saddr %s counter snat ip prefix to %s comment %q`,
					tr.iface, m.Real, m.Shown, key))
			}
			if len(p.theirs)+len(p.ours) > 0 {
				r.nat = append(r.nat, SystemNAT{
					Zone: tr.zone, Interfaces: []string{tr.iface}, Source: mapsText(p), Destination: "anywhere",
					Keys: []string{"nat_postrouting/" + key},
				})
			}
		}
	}
}

// chainNATOutput maps the router's own connections to a shown network
// back to the real one, as the prerouting map does for forwarded ones.
// The shown prefix's route takes them into the tunnel first; the mark
// then routes the real network there too.
func (r *renderer) chainNATOutput() {
	own := r.routerAddresses()
	var lines []string
	for _, tr := range r.translations() {
		for _, p := range tr.peers {
			for _, m := range p.theirs {
				except := ""
				if skip := shownOwn(m, own); len(skip) > 0 {
					except = "ip daddr != " + setOrSingle(skip) + " "
				}
				lines = append(lines, fmt.Sprintf(`ip daddr %s %scounter dnat ip prefix to %s comment %q`,
					m.Shown, except, m.Real, "translate:"+model.PeerRef(tr.iface, p.name)))
			}
		}
	}
	if len(lines) == 0 {
		return
	}
	r.block("chain nat_output", func() {
		r.line("type nat hook output priority dstnat; policy accept;")
		for _, l := range lines {
			r.line(l)
		}
	})
}

// shownOwn lists the shown addresses of the router's own addresses on a
// map's real network, which the maps leave alone.
func shownOwn(m model.PrefixMap, own []netip.Addr) []string {
	var out []string
	for _, a := range own {
		if m.Real.Contains(a) {
			out = append(out, shownAddr(m, a).String())
		}
	}
	return out
}

// chainNATInput shows the peers' hosts to the router's own services from
// the shown networks too, so an answer goes back through the tunnel
// rather than to a host here with the same number.
func (r *renderer) chainNATInput() {
	var lines []string
	for _, tr := range r.translations() {
		for _, p := range tr.peers {
			for _, m := range p.theirs {
				lines = append(lines, fmt.Sprintf(`iifname %q ip saddr %s counter snat ip prefix to %s comment %q`,
					tr.iface, m.Real, m.Shown, "translate:"+model.PeerRef(tr.iface, p.name)))
			}
		}
	}
	if len(lines) == 0 {
		return
	}
	r.block("chain nat_input", func() {
		r.line("type nat hook input priority srcnat; policy accept;")
		for _, l := range lines {
			r.line(l)
		}
	})
}

// mapsText says what a peer's maps show, for the rules and NAT pages.
func mapsText(p peerMaps) string {
	var parts []string
	for _, m := range p.theirs {
		parts = append(parts, m.Real.String()+" as "+m.Shown.String())
	}
	for _, m := range p.ours {
		parts = append(parts, "ours "+m.Real.String()+" as "+m.Shown.String())
	}
	return strings.Join(parts, ", ")
}

// shownAddr is the address a real one goes by under a map.
func shownAddr(m model.PrefixMap, a netip.Addr) netip.Addr {
	base := binary.BigEndian.Uint32(m.Real.Addr().AsSlice())
	host := binary.BigEndian.Uint32(a.AsSlice()) - base
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], binary.BigEndian.Uint32(m.Shown.Addr().AsSlice())+host)
	return netip.AddrFrom4(out)
}

// routerAddresses are the router's own static IPv4 addresses.
func (r *renderer) routerAddresses() []netip.Addr {
	var out []netip.Addr
	for _, in := range r.cfg.Interfaces {
		if !in.Enabled || in.IPv4.Mode != model.AddrStatic {
			continue
		}
		if p, err := netip.ParsePrefix(in.IPv4.Address); err == nil && p.Addr().Is4() {
			out = append(out, p.Addr())
		}
	}
	return out
}
