package model

import (
	"fmt"
	"regexp"
	"strings"
)

// Discovery relays mDNS and SSDP between the router's inside networks, so
// a device on one can be found from another.
type Discovery struct {
	Enabled    bool                 `json:"enabled"`
	MDNS       bool                 `json:"mdns"`
	SSDP       bool                 `json:"ssdp"`
	Interfaces []DiscoveryInterface `json:"interfaces,omitempty"`
	Services   []string             `json:"services,omitempty"`
	Log        LogKeep              `json:"log,omitzero"`
}

// DiscoveryInterface is one network in the relay and its roles.
type DiscoveryInterface struct {
	Interface string `json:"interface"`
	Asks      bool   `json:"asks"`
	Answers   bool   `json:"answers"`
}

// Discovery log defaults and bounds, and the reply ports the relay binds.
// An entry costs DiscoveryLogBytes in memory.
const (
	DefaultDiscoveryLogEntries = 10_000
	MaxDiscoveryLogEntries     = 1_000_000
	DiscoveryLogBytes          = 200
	DiscoveryReplyPortFirst    = 61900
	DiscoveryReplyPortLast     = 61999
)

var serviceTypeRe = regexp.MustCompile(`^_[A-Za-z0-9_-]{1,62}\._(tcp|udp)$`)

// CheckDiscoveryInterface says why the named interface cannot be in the
// relay, or nil when it can.
func (c *Config) CheckDiscoveryInterface(name string) error {
	return c.insideLink(name, linkWords{carry: "discovery", member: "add", inside: "discovery stays on the inside"})
}

// DiscoveryLinks are the enabled interfaces in the relay, in config order;
// nil when the relay is off or relays neither protocol.
func (c *Config) DiscoveryLinks() []DiscoveryInterface {
	d := c.Services.Discovery
	if !d.Enabled || !d.MDNS && !d.SSDP {
		return nil
	}
	var out []DiscoveryInterface
	for _, l := range d.Interfaces {
		if in, ok := c.Interface(l.Interface); ok && in.Enabled {
			out = append(out, l)
		}
	}
	return out
}

// DiscoveryActive reports whether the relay has work: on, with a protocol,
// and an enabled interface that asks and one that answers.
func (c *Config) DiscoveryActive() bool {
	var asks, answers bool
	for _, l := range c.DiscoveryLinks() {
		asks = asks || l.Asks
		answers = answers || l.Answers
	}
	return asks && answers
}

// discovery checks the relay. Names are checked whether or not it is on,
// as for Wake on LAN.
func (v *validator) discovery(c *Config, ifaces map[string]bool) {
	d := c.Services.Discovery
	seen := map[string]bool{}
	var asks, answers bool
	for i, l := range d.Interfaces {
		path := fmt.Sprintf("services.discovery.interfaces[%d].interface", i)
		switch err := c.CheckDiscoveryInterface(l.Interface); {
		case !ifaces[l.Interface]:
			v.add(path, "unknown interface %q", l.Interface)
		case err != nil:
			v.add(path, "%v", err)
		case seen[l.Interface]:
			v.add(path, "%q is already listed", l.Interface)
		case !l.Asks && !l.Answers:
			v.add(path, "%q must ask, answer or both", l.Interface)
		}
		seen[l.Interface] = true
		asks = asks || l.Asks
		answers = answers || l.Answers
	}
	if d.Enabled {
		switch {
		case len(d.Interfaces) < 2:
			v.add("services.discovery.interfaces", "the relay needs at least two interfaces")
		case !asks:
			v.add("services.discovery.interfaces", "at least one interface must ask")
		case !answers:
			v.add("services.discovery.interfaces", "at least one interface must answer")
		}
		if !d.MDNS && !d.SSDP {
			v.add("services.discovery.mdns", "turn on mDNS, SSDP or both")
		}
	}
	types := map[string]bool{}
	for i, s := range d.Services {
		path := fmt.Sprintf("services.discovery.services[%d]", i)
		switch t := strings.ToLower(s); {
		case !serviceTypeRe.MatchString(s):
			v.add(path, "%q is not a service type like _googlecast._tcp", s)
		case types[t]:
			v.add(path, "%q is already listed", s)
		default:
			types[t] = true
		}
	}
	v.logKeep("services.discovery.log", d.Log, MaxDiscoveryLogEntries, DefaultDiscoveryLogEntries)
}
