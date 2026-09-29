package fwlog

import (
	"sync/atomic"

	"github.com/rforced/ostiole/internal/model"
)

// Zones names the zone each interface is in, as the configuration the
// router runs has it. The drops Ostiole makes on its own account log a
// prefix with no room for a zone, so the listener looks theirs up here as
// the packet arrives; a later change of zones does not rename old entries.
type Zones struct {
	m atomic.Pointer[map[string]string]
}

// Set takes the zones from a configuration.
func (z *Zones) Set(cfg *model.Config) {
	m := map[string]string{}
	if cfg != nil {
		for _, in := range cfg.Interfaces {
			if in.Enabled && in.Zone != "" {
				m[in.Name] = in.Zone
			}
		}
	}
	z.m.Store(&m)
}

// Of is the zone of an interface, or "" for one in none, or before Set.
func (z *Zones) Of(iface string) string {
	if z == nil {
		return ""
	}
	if m := z.m.Load(); m != nil {
		return (*m)[iface]
	}
	return ""
}
