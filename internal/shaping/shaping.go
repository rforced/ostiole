// Package shaping keeps a busy line's queue on this router instead of in
// the modem at the far end of it, which is what makes a saturated line
// merely busy rather than unusable.
//
// One queue discipline does the whole job (ADR-0008): given how fast the
// line really is, CAKE holds the backlog here, shares it out between the
// hosts behind the router, and sorts it into four tiers. The tier comes
// from the firewall rule that admitted the flow, carried in the packet
// mark, so nothing is classified twice and nothing has to be described in
// two places.
package shaping

import (
	"fmt"

	"github.com/rforced/ostiole/internal/model"
)

// Handle is the major number of the queue Ostiole installs, written the
// way tc writes handles: in hexadecimal, so the batch says 571: and the
// kernel stores 0x0571. The kernel hands out handles from 0x8001 upwards
// when nobody asks for one, so a root carrying this number is one of ours
// and nothing else's, which is how a reconcile knows what it may remove.
const Handle = 0x571

// HandleString is Handle as tc prints it, which is how a queue is
// recognised in tc's own JSON.
func HandleString() string { return fmt.Sprintf("%x:", Handle) }

// Ownership markers for the ingress side. The filter is installed at a
// fixed preference and handle so reinstalling it replaces the same entry
// instead of stacking another one beside it.
const (
	FilterPref   = 1
	FilterHandle = 1
)

// Direction is one of the two ways traffic moves, named as the operator
// sees it rather than as the wire does. Which one is which on a given
// interface depends on where the interface faces, and that is the
// backend's problem rather than the operator's.
type Direction string

// The two directions.
const (
	Download Direction = "download"
	Upload   Direction = "upload"
)

// Queue is one CAKE instance: where it goes, what it carries, and what it
// is told.
type Queue struct {
	// Device is the interface itself for the direction the kernel can
	// shape in place, and the helper device for the other one.
	Device string
	// Direction is what the operator calls this traffic.
	Direction Direction
	// Rate is the ceiling in bit/s.
	Rate int64
	// Options are the CAKE keywords that follow the bandwidth, in order.
	Options []string
}

// Plan is everything one interface needs in the kernel.
type Plan struct {
	// Interface is the link the operator gave a speed to.
	Interface string
	// IFB is the helper device that carries the direction the kernel
	// cannot queue in place. It is only created when that direction is
	// shaped.
	IFB string
	// External says this interface faces the internet, which decides which
	// figure applies to which direction and whether the shaper has to look
	// through NAT to find the host a packet belongs to.
	External bool
	// Egress hangs off the interface's own root. Ingress is redirected
	// onto the helper and hangs off that. Either may be nil.
	Egress  *Queue
	Ingress *Queue
}

// Wants reports whether the plan asks for anything at all.
func (p Plan) Wants() bool { return p.Egress != nil || p.Ingress != nil }

// Plans is what the configuration asks for, in configuration order.
func Plans(cfg *model.Config) []Plan {
	tagged := taggedInterfaces(cfg)
	var out []Plan
	for _, in := range cfg.ShapedInterfaces() {
		s := *in.Shaping
		p := Plan{Interface: in.Name, IFB: model.IFBName(in.Name)}
		if z, ok := cfg.Zone(in.Zone); ok && z.External {
			p.External = true
		}
		over := overheadFor(s.LinkType(), tagged[in.Name])

		// The kernel queues on the way out of a device, so the direction
		// that leaves the interface hangs off the interface and the one
		// arriving is redirected onto a helper that it can leave instead.
		leaving, arriving := Download, Upload
		leavingRate, arrivingRate := s.Download, s.Upload
		if p.External {
			leaving, arriving = Upload, Download
			leavingRate, arrivingRate = s.Upload, s.Download
		}
		if leavingRate > 0 {
			p.Egress = &Queue{
				Device: in.Name, Direction: leaving, Rate: leavingRate,
				Options: cakeOptions(p.External, false, over),
			}
		}
		if arrivingRate > 0 {
			p.Ingress = &Queue{
				Device: p.IFB, Direction: arriving, Rate: arrivingRate,
				Options: cakeOptions(p.External, true, over),
			}
		}
		if p.Wants() {
			out = append(out, p)
		}
	}
	return out
}

// cakeOptions are the keywords after the bandwidth, in the order tc takes
// them.
//
// Flow isolation follows where the hosts are. Leaving an interface that
// faces the internet they are the sources, arriving they are the
// destinations; on an interface facing hosts it is the other way round.
// Getting it backwards would share the line out between the far ends
// rather than between the people on this network, which is the whole
// point of the exercise.
func cakeOptions(external, arriving bool, over overhead) []string {
	var opts []string
	if external && arriving {
		// The bytes already crossed the bottleneck, so the queue can only
		// work by dropping enough to make the senders slow down.
		opts = append(opts, "ingress")
	}
	opts = append(opts, "diffserv4")
	if external == arriving {
		opts = append(opts, "dual-dsthost")
	} else {
		opts = append(opts, "dual-srchost")
	}
	if external {
		// Behind an external interface every host wears the router's
		// address, so the shaper has to ask conntrack who a packet is
		// really for before it can share the line out fairly.
		opts = append(opts, "nat")
	}
	return append(opts, over.keywords()...)
}

// overhead is what a line adds to every packet that the operator never
// sees. Getting it wrong by a few bytes on small packets is the
// difference between holding the queue here and handing it to the modem.
type overhead struct {
	bytes int
	// mpu is the smallest a frame can be on this kind of line; zero means
	// the line has no floor worth naming.
	mpu int
	// framing is how the line cuts packets up: ATM cells, PTM, or neither.
	framing string
}

func (o overhead) keywords() []string {
	out := []string{"overhead", fmt.Sprint(o.bytes)}
	if o.mpu > 0 {
		out = append(out, "mpu", fmt.Sprint(o.mpu))
	}
	return append(out, o.framing)
}

// overheads are CAKE's own presets, written out as numbers so a rendered
// batch says what it means and a golden can pin it.
var overheads = map[model.LinkType]overhead{
	model.LinkEthernet:     {bytes: 38, mpu: 84, framing: "noatm"},
	model.LinkDOCSIS:       {bytes: 18, mpu: 64, framing: "noatm"},
	model.LinkPPPoEPTM:     {bytes: 30, framing: "ptm"},
	model.LinkBridgedPTM:   {bytes: 22, framing: "ptm"},
	model.LinkPPPoEVCMux:   {bytes: 32, framing: "atm"},
	model.LinkConservative: {bytes: 48, framing: "atm"},
}

// VLANOverhead is the tag an 802.1Q frame carries, which the operator does
// not see and the line still has to send.
const VLANOverhead = 4

func overheadFor(link model.LinkType, tagged bool) overhead {
	o, ok := overheads[link]
	if !ok {
		o = overheads[model.LinkEthernet]
	}
	if tagged {
		o.bytes += VLANOverhead
	}
	return o
}

// taggedInterfaces names the interfaces whose frames carry a VLAN tag: the
// VLANs themselves, and a dialled session running over one, where the tag
// is on the wire under the session.
func taggedInterfaces(cfg *model.Config) map[string]bool {
	vlan := map[string]bool{}
	for _, in := range cfg.Interfaces {
		if in.VLAN != nil {
			vlan[in.Name] = true
		}
	}
	out := map[string]bool{}
	for _, in := range cfg.Interfaces {
		switch {
		case vlan[in.Name]:
			out[in.Name] = true
		case in.PPPoE != nil && vlan[in.PPPoE.Parent]:
			out[in.Name] = true
		}
	}
	return out
}
