package server

import (
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/services"
)

// dnsName is a name the DNS service answers on its own account rather than
// from a host override: a static lease's hostname, a proxy site's host, or
// the name a device sent to a server that registers it. The names tab has
// the overrides in the draft already and lists these beside them.
type dnsName struct {
	// Name is the name in full, and Bare the label it answers alone as
	// well when it is one label under the local domain.
	Name string `json:"name"`
	Bare string `json:"bare,omitempty"`
	// Addresses are what it answers. A proxy site's are the router's own
	// on Interfaces, as the kernel has them now.
	Addresses []string `json:"addresses"`
	// Source is static, proxy or device.
	Source      string   `json:"source"`
	MAC         string   `json:"mac,omitempty"`
	Description string   `json:"description,omitempty"`
	Site        string   `json:"site,omitempty"`
	Interfaces  []string `json:"interfaces,omitempty"`
	// Expires is when a device's lease, and with it the name, runs out.
	Expires time.Time `json:"expires,omitzero"`
	// HeldBy is set when something else answers a device's name at
	// another address: the device keeps its address and loses the name.
	HeldBy *nameHolder `json:"heldBy,omitempty"`
}

// nameHolder is what answers a name instead: an override by its full
// name, a static lease by its MAC, or a proxy site by its id.
type nameHolder struct {
	Source string `json:"source"`
	Name   string `json:"name"`
}

// dnsNamesHandler reads the draft in the body, like systemRules, so the tab
// shows what the draft would answer.
func (a *api) dnsNamesHandler(w http.ResponseWriter, r *http.Request) error {
	var req configRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil {
		return &badRequest{errors.New("config is required")}
	}
	// Without the lease file the devices are missing and the rest still
	// stands; the leases tab is where a file that cannot be read shows.
	var file []services.Lease
	if a.services != nil {
		file, _ = a.services.ReadLeases()
	}
	links, _ := network.Discover()
	writeJSON(w, http.StatusOK, dnsNames(req.Config.config(), file, links))
	return nil
}

// dnsNames derives the rows: the static leases, the proxy sites, then the
// devices, each in the order it is kept.
func dnsNames(cfg *model.Config, file []services.Lease, links []network.Link) []dnsName {
	out := []dnsName{}
	local := model.NormalizeDomain(cfg.Services.DNS.Domain)
	holders := map[string]heldName{}
	hold := func(names []string, addr string, h nameHolder) {
		for _, n := range names {
			if n = strings.ToLower(n); n != "" {
				if _, taken := holders[n]; !taken {
					holders[n] = heldName{addr: addr, holder: h}
				}
			}
		}
	}
	for _, o := range cfg.Services.DNS.HostOverrides {
		hold(o.Names(local), o.IP, nameHolder{Source: "override", Name: o.FQDN(local)})
	}

	named := map[string]bool{}
	for _, sh := range services.SystemHosts(cfg) {
		named[sh.MAC] = true
		name, bare := qualify(sh.Hostname, local)
		out = append(out, dnsName{
			Name: name, Bare: bare, Addresses: []string{sh.IP}, Source: "static",
			MAC: sh.MAC, Description: sh.Description,
		})
		hold([]string{name, bare}, sh.IP, nameHolder{Source: "static", Name: sh.MAC})
	}

	for _, pn := range cfg.ProxyNames() {
		out = append(out, dnsName{
			Name: pn.Name, Bare: pn.Bare, Addresses: routerAddresses(links, pn.Interfaces),
			Source: "proxy", Site: pn.Site, Interfaces: pn.Interfaces,
		})
		hold(pn.Names(), "", nameHolder{Source: "proxy", Name: pn.Site})
	}

	registers4 := map[string]bool{}
	for _, sc := range cfg.ActiveDHCP() {
		registers4[sc.Interface] = sc.DNSRegistration
	}
	served6 := map[string]bool{}
	for _, sc := range cfg.ActiveDHCPv6() {
		if sc.DNSRegistration {
			served6[sc.Interface] = true
		}
	}
	prefixes6 := v6Prefixes(links, served6)
	for _, l := range file {
		// A device with a static lease that names it is listed above, and
		// the name on its lease is that one.
		if l.Hostname == "" || named[strings.ToLower(l.MAC)] {
			continue
		}
		ip, err := netip.ParseAddr(l.IP)
		if err != nil {
			continue
		}
		var registers bool
		if l.Family == 4 {
			iface, _ := cfg.InterfaceFor(ip)
			registers = registers4[iface]
		} else {
			registers = prefixOf(prefixes6, ip) != ""
		}
		if !registers {
			continue
		}
		name, bare := qualify(l.Hostname, local)
		row := dnsName{
			Name: name, Bare: bare, Addresses: []string{l.IP}, Source: "device",
			MAC: strings.ToLower(l.MAC), Expires: l.Expires,
		}
		for _, n := range []string{name, bare} {
			if h, ok := holders[strings.ToLower(n)]; ok && n != "" && h.addr != l.IP {
				row.HeldBy = &h.holder
				break
			}
		}
		out = append(out, row)
	}
	return out
}

// heldName is a name something answers with addr, empty for the router's
// own addresses.
type heldName struct {
	addr   string
	holder nameHolder
}

// qualify is the full name a hostname answers as, and the bare label when
// that is different: a name with a dot in it is never expanded.
func qualify(hostname, local string) (name, bare string) {
	if local == "" || strings.Contains(hostname, ".") {
		return hostname, ""
	}
	return hostname + "." + local, hostname
}

// routerAddresses are the global addresses the kernel has on the named
// links, IPv4 first.
func routerAddresses(links []network.Link, names []string) []string {
	v4, v6 := []string{}, []string{}
	for _, l := range links {
		if !slices.Contains(names, l.Name) {
			continue
		}
		for _, a := range l.Addresses {
			p, err := netip.ParsePrefix(a)
			switch {
			case err != nil || p.Addr().IsLinkLocalUnicast():
			case p.Addr().Is4():
				v4 = append(v4, p.Addr().String())
			default:
				v6 = append(v6, p.Addr().String())
			}
		}
	}
	return append(v4, v6...)
}
