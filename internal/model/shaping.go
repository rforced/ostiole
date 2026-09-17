package model

import (
	"fmt"
	"hash/fnv"
)

// Shaping is how fast one interface's line really is. Given a figure, the
// router holds the queue on this side of the bottleneck instead of letting
// it build up in somebody else's modem, which is what turns a saturated
// line from unusable into merely busy.
//
// A direction left at zero is not shaped at all. On an interface facing the
// internet the two figures are the line's own speed; on an interface facing
// hosts they are a cap on what those hosts may use.
type Shaping struct {
	// Download and Upload are bit/s from the operator's point of view, so
	// they read the same on a WAN and on a LAN. The backend works out which
	// direction that is on the wire.
	Download int64 `json:"download,omitempty"`
	Upload   int64 `json:"upload,omitempty"`
	// Link is what the line is made of, which decides how much of every
	// packet the operator never sees. Empty means plain Ethernet.
	Link LinkType `json:"link,omitempty"`
}

// LinkType names the encapsulation under an interface. The shaper has to
// count the bytes the line adds to every packet, or it hands the modem
// more than the modem can send and the queue moves back out of reach.
type LinkType string

// Link types, in the order the UI offers them.
const (
	// LinkEthernet is plain Ethernet or fibre.
	LinkEthernet LinkType = "ethernet"
	// LinkDOCSIS is cable.
	LinkDOCSIS LinkType = "docsis"
	// LinkPPPoEPTM is VDSL2 with a dialled session, which is most VDSL.
	LinkPPPoEPTM LinkType = "pppoe-ptm"
	// LinkBridgedPTM is VDSL2 without one, where the modem dials instead.
	LinkBridgedPTM LinkType = "bridged-ptm"
	// LinkPPPoEVCMux is ADSL with a dialled session over ATM.
	LinkPPPoEVCMux LinkType = "pppoe-vcmux"
	// LinkConservative over-counts on purpose, for a line nobody can name.
	LinkConservative LinkType = "conservative"
)

// LinkTypes lists them in the order the UI offers them.
var LinkTypes = []LinkType{
	LinkEthernet, LinkDOCSIS, LinkPPPoEPTM, LinkBridgedPTM, LinkPPPoEVCMux, LinkConservative,
}

// Rate bounds. Below the floor the shaper cannot get a packet out in any
// sensible time; above the ceiling nobody is shaping anything.
const (
	MinRate = 64_000
	MaxRate = 100_000_000_000
)

// Tier is how much a flow matters when the line is full. It is set on the
// rule or the port forward that admits the flow and follows it both ways,
// so nothing has to be classified twice.
type Tier string

// Tiers, lowest first.
const (
	// TierBulk yields to everything else, for backups and updates.
	TierBulk Tier = "bulk"
	// TierNormal is where unmarked traffic ends up anyway; setting it
	// explicitly stops a device's own marking from promoting itself.
	TierNormal Tier = "normal"
	// TierHigh goes ahead of ordinary traffic, for video and for work.
	TierHigh Tier = "high"
	// TierRealtime goes first, for calls and for games: small packets that
	// are worthless late.
	TierRealtime Tier = "realtime"
)

// Tiers lists them lowest first, which is also the order of the marks.
var Tiers = []Tier{TierBulk, TierNormal, TierHigh, TierRealtime}

// Mark is the packet mark that puts a flow in this tier, and whether the
// tier is one Ostiole knows. The empty tier has no mark: traffic nobody
// classified is left for the shaper to judge on its own.
func (t Tier) Mark() (uint32, bool) {
	for i, x := range Tiers {
		if x == t {
			return uint32(i+1) << ShapeMarkShift, true
		}
	}
	return 0, false
}

// Valid reports whether the tier is one of the four, or empty.
func (t Tier) Valid() bool {
	if t == "" {
		return true
	}
	_, ok := t.Mark()
	return ok
}

// Active reports whether either direction is shaped.
func (s Shaping) Active() bool { return s.Download > 0 || s.Upload > 0 }

// LinkType is the encapsulation in force, filling in the default.
func (s Shaping) LinkType() LinkType {
	if s.Link == "" {
		return LinkEthernet
	}
	return s.Link
}

// ShapedInterfaces lists the enabled interfaces with a speed on them, in
// configuration order.
func (c *Config) ShapedInterfaces() []Interface {
	var out []Interface
	for _, in := range c.Interfaces {
		if in.Enabled && in.Shaping != nil && in.Shaping.Active() {
			out = append(out, in)
		}
	}
	return out
}

// ShapesTraffic reports whether anything is classified into a tier, which
// is what decides whether the ruleset needs the rules that carry a tier
// from one packet of a flow to the next.
func (c *Config) ShapesTraffic() bool {
	for _, r := range c.Rules {
		if r.Enabled && r.Priority != "" {
			return true
		}
	}
	for _, pf := range c.NAT.PortForwards {
		if pf.Enabled && pf.Priority != "" {
			return true
		}
	}
	return false
}

// IFBName is the intermediate device that carries an interface's shaped
// ingress. The kernel refuses a name over 15 characters, so a long
// interface name is cut to its first six and given four hex digits of its
// hash, which keeps the name stable and distinct without being readable.
//
// It lives here rather than beside the rest of the shaping because
// validation has to know when two interfaces would land on one device.
func IFBName(iface string) string {
	const prefix = "ifb-"
	if len(iface) <= 11 {
		return prefix + iface
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(iface))
	return fmt.Sprintf("%s%s-%04x", prefix, iface[:6], h.Sum32()&0xffff)
}
