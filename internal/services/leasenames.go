package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/rforced/ostiole/internal/network"
)

// leasesName is the rewritten lease file on its way into place.
const leasesName = "ostiole.leases.new"

// leaseCopies keeps the transient units' names apart.
var leaseCopies atomic.Int64

// namePolicy is what a rendered configuration answers of the names devices
// send: the IPv4 networks whose servers register them, the interfaces whose
// IPv6 servers do, and the MACs a static lease gives a name of its own.
type namePolicy struct {
	nets4   []netip.Prefix
	ifaces6 []string
	named   map[string]bool
}

// policyOf reads the policy back out of the configuration dnsmasq is about
// to start with, so an apply and a revert follow the same rule. ok is false
// when the configuration serves no DHCP, and dnsmasq then reads no leases.
func policyOf(conf string) (p namePolicy, ok bool) {
	p.named = map[string]bool{}
	nets := map[string]netip.Prefix{}
	ignored := map[string]bool{}
	for _, line := range strings.Split(conf, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		parts := strings.Split(value, ",")
		switch key {
		case "dhcp-range":
			ok = true
			tag, tagged := strings.CutPrefix(parts[0], "set:")
			if !tagged {
				continue
			}
			if iface, v6 := constructorOf(parts); v6 {
				// renderV6 writes ra-names only for a server that registers.
				if slices.Contains(parts, "ra-names") {
					p.ifaces6 = append(p.ifaces6, iface)
				}
				continue
			}
			if len(parts) < 4 {
				continue
			}
			start, err := netip.ParseAddr(parts[1])
			mask, merr := netip.ParseAddr(parts[3])
			if err != nil || merr != nil || !start.Is4() || !mask.Is4() {
				continue
			}
			bits, _ := net.IPMask(mask.AsSlice()).Size()
			nets[tag] = netip.PrefixFrom(start, bits).Masked()
		case "dhcp-ignore-names":
			if tag, tagged := strings.CutPrefix(value, "tag:"); tagged {
				ignored[tag] = true
			}
		case "dhcp-host":
			// mac[,ip][,[ipv6]][,hostname]: the hostname is the one part
			// that is neither address.
			last := parts[len(parts)-1]
			if _, err := netip.ParseAddr(last); len(parts) > 1 && err != nil && !strings.HasPrefix(last, "[") {
				p.named[strings.ToLower(parts[0])] = true
			}
		}
	}
	for tag, prefix := range nets {
		if !ignored[tag] {
			p.nets4 = append(p.nets4, prefix)
		}
	}
	return p, ok
}

func constructorOf(parts []string) (string, bool) {
	for _, part := range parts {
		if iface, found := strings.CutPrefix(part, "constructor:"); found {
			return iface, true
		}
	}
	return "", false
}

// strip takes the name off every lease the policy does not register and
// leaves everything else as it was, the addresses above all. prefixes6 are
// the global prefixes on the interfaces in ifaces6. It reports whether it
// took any name off.
func (p namePolicy) strip(file string, prefixes6 []netip.Prefix) (string, bool) {
	lines := strings.SplitAfter(file, "\n")
	changed := false
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] == "*" || strings.HasPrefix(fields[0], "duid") {
			continue
		}
		ip, err := netip.ParseAddr(fields[2])
		if err != nil {
			continue
		}
		var keep bool
		if ip.Is4() || ip.Is4In6() {
			keep = inAny(p.nets4, ip.Unmap()) || p.named[strings.ToLower(fields[1])]
		} else {
			keep = inAny(prefixes6, ip)
		}
		if keep {
			continue
		}
		fields[3] = "*"
		lines[i] = strings.Join(fields, " ")
		if strings.HasSuffix(line, "\n") {
			lines[i] += "\n"
		}
		changed = true
	}
	return strings.Join(lines, ""), changed
}

func inAny(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// strippedLeases is the lease file with the names conf does not register
// taken off, or nil when there are none to take off.
func (d *Dnsmasq) strippedLeases(conf string) ([]byte, error) {
	p, ok := policyOf(conf)
	if !ok {
		return nil, nil
	}
	raw, err := os.ReadFile(d.leases())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var prefixes6 []netip.Prefix
	if len(p.ifaces6) > 0 {
		links, err := d.links()
		if err != nil {
			return nil, err
		}
		prefixes6 = globalPrefixes(links, p.ifaces6)
	}
	out, changed := p.strip(string(raw), prefixes6)
	if !changed {
		return nil, nil
	}
	return []byte(out), nil
}

// globalPrefixes are the IPv6 prefixes the kernel has on the named links:
// the configuration names none, as dnsmasq takes them from the interface.
func globalPrefixes(links []network.Link, names []string) []netip.Prefix {
	var out []netip.Prefix
	for _, l := range links {
		if !slices.Contains(names, l.Name) {
			continue
		}
		for _, a := range l.Addresses {
			p, err := netip.ParsePrefix(a)
			if err == nil && p.Addr().Is6() && !p.Addr().IsLinkLocalUnicast() {
				out = append(out, p.Masked())
			}
		}
	}
	return out
}

func (d *Dnsmasq) links() ([]network.Link, error) {
	if d.Links != nil {
		return d.Links()
	}
	return network.Discover()
}

// restart restarts dnsmasq on the configuration conf. dnsmasq loads its
// lease file as it starts and keeps every name on it, through renewals too,
// so a name the configuration no longer registers has to come off the file
// while dnsmasq is stopped. The daemon cannot write where the file lives: it
// writes the new one beside the configuration, and a transient unit, which
// has no sandbox, copies it over the old one. A copy keeps the file's inode,
// and with it the SELinux label dnsmasq needs to write it.
func (d *Dnsmasq) restart(ctx context.Context, conf string) error {
	if stripped, err := d.strippedLeases(conf); err != nil || stripped == nil {
		if err != nil {
			return err
		}
		return d.systemctl(ctx, "restart")
	}
	if err := d.systemctl(ctx, "stop"); err != nil {
		return err
	}
	// Read again now that nothing writes the file: a lease handed out since
	// the first read would be lost otherwise.
	stripped, copyErr := d.strippedLeases(conf)
	if copyErr == nil && stripped != nil {
		copyErr = d.replaceLeases(ctx, stripped)
	}
	// A router without DHCP and DNS is worse than one still answering a
	// name, so dnsmasq starts whatever the copy did.
	if err := d.systemctl(ctx, "start"); err != nil {
		return err
	}
	if copyErr != nil {
		return fmt.Errorf("take the names off the DHCP leases: %w", copyErr)
	}
	return nil
}

func (d *Dnsmasq) replaceLeases(ctx context.Context, content []byte) error {
	tmp := filepath.Join(d.dir(), leasesName)
	if err := writeFile(tmp, string(content)); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	unit := fmt.Sprintf("ostiole-leases-%d-%d", os.Getpid(), leaseCopies.Add(1))
	if out, err := d.cmd().Run(ctx, "systemd-run", "--unit="+unit, "--wait", "--collect", "--quiet",
		"--", "cp", "--", tmp, d.leases()); err != nil {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

func (d *Dnsmasq) systemctl(ctx context.Context, verb string) error {
	if out, err := d.cmd().Run(ctx, "systemctl", verb, Unit); err != nil {
		return fmt.Errorf("%s %s: %w: %s", verb, Unit, err, bytes.TrimSpace(out))
	}
	return nil
}
