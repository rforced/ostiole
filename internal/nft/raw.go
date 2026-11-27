package nft

import (
	"fmt"
	"strings"

	"ostiole/internal/model"
)

// RawChain is the first chain a packet meets, ahead of connection
// tracking, so what it drops never becomes a connection.
const RawChain = "raw_prerouting"

// System kinds of the raw chain's drops, as the log's "s" prefix names them.
const (
	kindIPOptions     = "block-ip-options"
	kindRoutingHeader = "block-routing-header"
	kindSpoofed       = "block-spoofed"
	kindImpossible    = "block-impossible"
	kindTCPFlags      = "block-tcp-flags"
	kindHopByHop      = "block-hop-by-hop"
)

// chainRaw drops what no connection should be built from: addresses and
// TCP flags that never cross a link honestly, TCP whose IPv4 header
// carries options and IPv6 with a routing header, unless the protection
// settings allow those two, and IPv6 whose source an inside link would not
// route back to. Nothing here matches by zone: it runs before the zone
// chains, on everything that arrives, forwarded or not.
func (r *renderer) chainRaw() {
	r.block("chain "+RawChain, func() {
		r.line("type filter hook prerouting priority raw; policy accept;")
		r.rawDrops(rawFixed, "")
		if !r.cfg.Protection.AllowIPOptions {
			r.rawDrops(rawOptions, "protection")
		}
		r.spoofedSources(r.sourceCheckedInterfaces())
	})
}

// rawDrop is one kind of packet the raw chain drops, by every match that
// finds it. The matches share a comment, so one counter sums them.
type rawDrop struct {
	kind, protocol, description string
	matches                     []string
}

// rawFixed are the drops no setting lifts. RFC 6092 names the addresses:
// the IPv6 loopback as a source from the wire, and an IPv4-mapped address
// at either end, which exists inside a host's socket layer and nowhere
// else. The flags are no flags at all, SYN with FIN and SYN with RST,
// which connection tracking would call invalid one lookup later.
var rawFixed = []rawDrop{
	{
		kindImpossible, string(model.ProtocolAny), "IPv6 loopback sources and IPv4-mapped addresses from the wire",
		[]string{notLoopback + "ip6 saddr ::1", "ip6 saddr ::ffff:0:0/96", "ip6 daddr ::ffff:0:0/96"},
	},
	{
		kindTCPFlags, string(model.ProtocolTCP), "TCP with flags no connection sends",
		[]string{
			"tcp flags & (fin|syn|rst|psh|ack|urg) == 0x0",
			"tcp flags & (syn|fin) == (syn|fin)",
			"tcp flags & (syn|rst) == (syn|rst)",
		},
	},
}

const notLoopback = `iifname != "lo" `

// rawOptions are the drops the IP options setting controls. The TCP match
// leaves IGMP's Router Alert alone, which is the one option a network
// does need. Every routing header goes, not type 0 alone: types 3 and 4
// steer a packet through hosts of the sender's choosing as type 0 did.
var rawOptions = []rawDrop{
	{kindIPOptions, string(model.ProtocolTCP), "TCP with IPv4 options", []string{"ip protocol tcp ip hdrlength != 5"}},
	{kindRoutingHeader, string(model.ProtocolAny), "IPv6 routing headers", []string{"exthdr rt exists"}},
}

func (r *renderer) rawDrops(drops []rawDrop, setting string) {
	loud, all := r.loggingInterfaces(r.enabledInterfaces())
	for _, d := range drops {
		for _, match := range d.matches {
			if len(loud) > 0 {
				// The set names real links, never lo.
				r.line(systemLogLine(strings.TrimPrefix(match, notLoopback), ifnameSet(loud), d.kind))
			}
			r.line(fmt.Sprintf("%s counter drop comment %q", match, d.kind))
		}
		r.sys(SystemRule{
			Chain: RawChain, Action: "drop", Protocol: d.protocol,
			Source: "any", Destination: "any", Description: d.description, Log: all,
			Keys:    []string{RawChain + "/" + d.kind},
			LogKeys: logKeys(len(loud) > 0, RawChain+"/log:"+d.kind),
			Setting: setting,
		})
	}
}

// hopByHop drops forwarded IPv6 that carries hop-by-hop options, under the
// IP options setting (RFC 9098): every router on the path has to read
// them, and nothing a network forwards needs them. It is in forward alone:
// MLD on the router's own links carries Router Alert there.
func (r *renderer) hopByHop() {
	if r.cfg.Protection.AllowIPOptions {
		return
	}
	const match = "exthdr hbh exists"
	loud, all := r.loggingInterfaces(r.enabledInterfaces())
	if len(loud) > 0 {
		r.line(systemLogLine(match, ifnameSet(loud), kindHopByHop))
	}
	r.line(fmt.Sprintf("%s counter drop comment %q", match, kindHopByHop))
	r.sys(SystemRule{
		Chain: "forward", Action: "drop", Protocol: string(model.ProtocolAny),
		Source: "any", Destination: "any", Description: "Forwarded IPv6 with hop-by-hop options", Log: all,
		Keys:    []string{"forward/" + kindHopByHop},
		LogKeys: logKeys(len(loud) > 0, "forward/log:"+kindHopByHop),
		Setting: "protection",
	})
}

// spoofedSources holds IPv6 arriving on an inside link to the routes back:
// a source the router would not send out the same link did not come from
// there (BCP 38). IPv6 has no rp_filter. IPv4 is left to rp_filter's loose
// mode, which refuses only sources with no route at all, as strict mode
// drops multi-WAN and policy-routed replies. The unspecified address is
// left alone: duplicate address detection and router solicitations come
// from it.
func (r *renderer) spoofedSources(ifs []string) {
	if len(ifs) == 0 {
		return
	}
	match := "ip6 saddr != :: fib saddr . iif oif missing"
	loud, all := r.loggingInterfaces(ifs)
	if len(loud) > 0 {
		r.line(systemLogLine(match, ifnameSet(loud), kindSpoofed))
	}
	r.line(fmt.Sprintf("iifname %s %s counter drop comment %q", ifnameSet(ifs), match, kindSpoofed))
	r.sysFor(ifs, SystemRule{
		Chain: RawChain, Action: "drop", Protocol: string(model.ProtocolAny),
		Source: "IPv6 not routed back here", Destination: "any",
		Description: "IPv6 sources the link they came in on does not lead back to", Log: all,
		Keys:    []string{RawChain + "/" + kindSpoofed},
		LogKeys: logKeys(len(loud) > 0, RawChain+"/log:"+kindSpoofed),
		Setting: "interface",
	})
}

// sourceCheckedInterfaces are the inside links whose IPv6 sources are held
// to their routes: enabled, in a zone that is not external, numbered by
// the router itself (static or from a delegated prefix). A link that takes
// its address from SLAAC or DHCPv6 has another router on it, whose
// networks the routes here may not know. Tunnels, PPPoE and a link a
// gateway sends through are left out, since sources from anywhere come
// back in by them. An interface can opt out with AllowUnroutedIPv6.
func (r *renderer) sourceCheckedInterfaces() []string {
	gateway := map[string]bool{}
	for _, g := range r.cfg.Gateways {
		gateway[g.Interface] = true
	}
	var out []string
	for _, in := range r.cfg.Interfaces {
		if !in.Enabled || (in.IPv6.Mode != model.AddrStatic && in.IPv6.Mode != model.AddrDelegated) {
			continue
		}
		if z, ok := r.cfg.Zone(in.Zone); !ok || z.External {
			continue
		}
		if in.AllowUnroutedIPv6 || in.Tunnel() || in.Tailscale != nil || in.PPPoE != nil || gateway[in.Name] {
			continue
		}
		out = append(out, in.Name)
	}
	return out
}

// enabledInterfaces lists every enabled interface.
func (r *renderer) enabledInterfaces() []string {
	var out []string
	for _, in := range r.cfg.Interfaces {
		if in.Enabled {
			out = append(out, in.Name)
		}
	}
	return out
}

// loggingInterfaces picks out of ifs those that log their drops, and says
// whether that is all of them.
func (r *renderer) loggingInterfaces(ifs []string) (loud []string, all bool) {
	def := r.cfg.System.Management.LogDefaultDrops
	for _, name := range ifs {
		if in, ok := r.cfg.Interface(name); ok && in.LogsDrops(def) {
			loud = append(loud, name)
		}
	}
	return loud, len(ifs) > 0 && len(loud) == len(ifs)
}
