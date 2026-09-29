package model

import (
	"bytes"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// WoL is Wake on LAN: the machines this router can switch on by sending a
// magic packet onto their network.
type WoL struct {
	Devices []WoLDevice `json:"devices,omitempty"`
}

// WoLDevice is one machine to wake.
type WoLDevice struct {
	ID string `json:"id"`
	// Interface is the network the machine is on. The packet goes out
	// there and nowhere else.
	Interface   string `json:"interface"`
	MAC         string `json:"mac"`
	Description string `json:"description,omitempty"`
}

// WoLDevice returns the device with the given id.
func (c *Config) WoLDevice(id string) (*WoLDevice, bool) {
	for i := range c.Services.WoL.Devices {
		if c.Services.WoL.Devices[i].ID == id {
			return &c.Services.WoL.Devices[i], true
		}
	}
	return nil, false
}

// ParseWakeMAC reads the hardware address of a machine to wake, written
// aa:bb:cc:dd:ee:ff. A group address names no one machine, and all zeros
// none at all.
func ParseWakeMAC(s string) (net.HardwareAddr, error) {
	if !macRe.MatchString(s) {
		return nil, fmt.Errorf("%q is not a MAC address like aa:bb:cc:dd:ee:ff", s)
	}
	hw, err := net.ParseMAC(s)
	if err != nil {
		return nil, err
	}
	switch {
	case hw[0]&1 != 0:
		return nil, fmt.Errorf("%s is a group address, not one machine's", s)
	case bytes.Equal(hw, make(net.HardwareAddr, len(hw))):
		return nil, fmt.Errorf("%s is no machine's address", s)
	}
	return hw, nil
}

// CheckWake says why a machine on the named interface cannot be woken, or
// nil when it can. The packet is an Ethernet frame, so a tunnel or a
// dialled session has nowhere to put it, and it goes to the inside only:
// on a WAN it would hand a machine's address to the provider's network.
// Whether the interface is on is left to the caller, since a device on
// one that is off stays saved for when it comes back.
func (c *Config) CheckWake(name string) error {
	in, ok := c.Interface(name)
	if !ok {
		return fmt.Errorf("unknown interface %q", name)
	}
	switch in.Kind() {
	case KindWireGuard, KindTailscale:
		return fmt.Errorf("%q is a tunnel, with no Ethernet to carry a wake", name)
	case KindPPPoE:
		return fmt.Errorf("%q is a dialled session, with no Ethernet to carry a wake", name)
	}
	if master, ok := c.MasterOf()[name]; ok {
		return fmt.Errorf("%q is part of %q, so wake on %q instead", name, master, master)
	}
	if session, ok := c.PPPoEParents()[name]; ok {
		return fmt.Errorf("%q carries the session %q, which faces the internet", name, session)
	}
	z, ok := c.Zone(in.Zone)
	switch {
	case !ok:
		return fmt.Errorf("interface %q has no zone, so it is not on the inside", name)
	case z.External:
		return fmt.Errorf("interface %q is in an external zone; a wake goes to the inside", name)
	}
	return nil
}

// WakeTarget checks a wake before it is sent: the machine's address, and
// an interface that can carry a wake and is on.
func (c *Config) WakeTarget(iface, mac string) (net.HardwareAddr, error) {
	hw, err := ParseWakeMAC(mac)
	if err != nil {
		return nil, err
	}
	if err := c.CheckWake(iface); err != nil {
		return nil, err
	}
	if in, _ := c.Interface(iface); !in.Enabled {
		return nil, fmt.Errorf("interface %q is off", iface)
	}
	return hw, nil
}

// InterfaceFor returns the enabled interface whose own IPv4 network holds
// addr. It is where a DHCP lease for addr was handed out, which dnsmasq's
// lease file does not record.
func (c *Config) InterfaceFor(addr netip.Addr) (string, bool) {
	for _, in := range c.Interfaces {
		if !in.Enabled || in.IPv4.Mode != AddrStatic {
			continue
		}
		p, err := netip.ParsePrefix(in.IPv4.Address)
		if err == nil && p.Masked().Contains(addr.Unmap()) {
			return in.Name, true
		}
	}
	return "", false
}

// wol checks the machines this router can wake. A device on an interface
// that is off is not an error, as a DHCP server on one is not: nothing is
// sent there until it is back.
func (v *validator) wol(c *Config) {
	ids := map[string]bool{}
	macs := map[string]bool{}
	for i, d := range c.Services.WoL.Devices {
		path := fmt.Sprintf("services.wol.devices[%d]", i)
		v.id(path+".id", d.ID, ids)
		if _, err := ParseWakeMAC(d.MAC); err != nil {
			v.add(path+".mac", "%v", err)
		} else if mac := strings.ToLower(d.MAC); macs[mac] {
			v.add(path+".mac", "%s is already listed", d.MAC)
		} else {
			macs[mac] = true
		}
		if err := c.CheckWake(d.Interface); err != nil {
			v.add(path+".interface", "%v", err)
		}
	}
}
