package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/iptables"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// The installer is opinionated: a router gets what a router needs and
// loses what it does not, in one pass, after the operator has seen the
// whole list and said yes. The list is worked out here from the same
// report the page shows, so the console and the page never disagree
// about what a router has.

// InstallPlan is what `ostiole install` proposes to do to this router.
type InstallPlan struct {
	// Install are the components to set up: their packages and the units
	// that point them at Ostiole.
	Install []ComponentState
	// Retire are the firewall services to stop and mask before anything
	// is removed, so the router is never briefly between firewalls with
	// the old one still deciding what gets in.
	Retire []CompetitorState
	// Remove is everything whose packages come off the router: retired
	// firewalls, and the extras a router has no use for.
	Remove []Removal
	// Mask are the units that are masked and kept, because their package
	// is the package manager itself or something Ostiole needs.
	Mask []string
	// Preview is the package manager's own account of the removal, which
	// is what the operator agrees to: what else comes away is its answer
	// and not Ostiole's.
	Preview string
	// Refused is set when the preview names something Ostiole needs.
	Refused error
	// Kept are the extras --keep exempted.
	Kept []string
}

// Removal is one thing whose packages come off the router.
type Removal struct {
	Label    string
	Units    []string
	Packages []string
}

// Empty reports whether there is nothing to do.
func (p InstallPlan) Empty() bool {
	return len(p.Install) == 0 && len(p.Retire) == 0 && len(p.Remove) == 0 && len(p.Mask) == 0
}

// packages is every package the plan removes.
func (p InstallPlan) packages() []string {
	var out []string
	for _, r := range p.Remove {
		out = append(out, r.Packages...)
	}
	return out
}

// units is every unit the plan stops and masks.
func (p InstallPlan) units() []string {
	var out []string
	for _, c := range p.Retire {
		out = append(out, c.Name)
	}
	for _, r := range p.Remove {
		out = append(out, r.Units...)
	}
	out = append(out, p.Mask...)
	return out
}

// installedComponents are what every router gets, whatever its
// configuration asks for yet: the firewall, its addresses, DHCP and DNS,
// the validating resolver and the queue. PPPoE and port mapping stay on
// demand, from the page, because most routers never dial a line or map
// a port.
var installedComponents = map[string]bool{"nft": true, "networkd": true, "dnsmasq": true, "unbound": true, "tc": true}

// PlanInstall works out what installing would do to this router. keep
// names extras to leave alone.
func PlanInstall(ctx context.Context, d Deps, keep []string) (InstallPlan, error) {
	if !d.Root {
		return InstallPlan{}, ErrNotRoot
	}
	rep := Status(ctx, d)
	var p InstallPlan
	for _, c := range rep.Components {
		// A component nobody packages here, or one that comes with systemd
		// and is missing anyway, is a fact about the distribution: the
		// page reports it, an installer cannot fix it.
		if installedComponents[c.Key] && !c.Ready && (c.Present || c.Availability == sysupdate.Installable) {
			p.Install = append(p.Install, c)
		}
	}
	for _, c := range rep.Competitors {
		if c.Kind != "firewall" {
			continue
		}
		if c.Conflicts {
			p.Retire = append(p.Retire, c)
		}
		if c.Name == "nftables" {
			continue
		}
		if c.Installed && len(c.Packages) > 0 {
			p.Remove = append(p.Remove, Removal{Label: c.Name, Packages: c.Packages})
		}
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[strings.ToLower(k)] = true
	}
	for _, e := range rep.Extras {
		if e.Removed {
			continue
		}
		if kept[strings.ToLower(e.Key)] {
			p.Kept = append(p.Kept, e.Label)
			continue
		}
		switch {
		case e.MaskOnly:
			// Already masked and stopped is already done.
			if e.Enabled != "masked" || e.Active {
				p.Mask = append(p.Mask, e.Units...)
			}
		case len(e.Packages) > 0:
			p.Remove = append(p.Remove, Removal{Label: e.Label, Units: e.Units, Packages: e.Packages})
		default:
			// Nothing to remove it with here; masking still stops it.
			p.Mask = append(p.Mask, e.Units...)
		}
	}
	if pkgs := p.packages(); len(pkgs) > 0 {
		if d.Packages == nil {
			return p, sysupdate.ErrNoManager
		}
		preview, err := d.Packages.Remove(ctx, pkgs, true)
		p.Preview = strings.TrimSpace(preview)
		if err != nil {
			return p, fmt.Errorf("ask the package manager what removing %s would take: %w", join(pkgs), err)
		}
		var names []string
		for _, r := range p.Remove {
			names = append(names, r.Label)
		}
		p.Refused = refuseRemoval(p.Preview, names, protectedPackages(rep))
	}
	return p, nil
}

// Print writes the plan the way a console reads it, one verb per line.
func (p InstallPlan) Print(w io.Writer) {
	if len(p.Install) > 0 {
		var labels []string
		for _, c := range p.Install {
			label := c.Label
			if len(c.Packages) > 0 {
				label += " (" + strings.Join(c.Packages, " ") + ")"
			}
			labels = append(labels, label)
		}
		fmt.Fprintf(w, "  install and set up:   %s\n", strings.Join(labels, ", "))
	}
	if len(p.Retire) > 0 {
		var names []string
		for _, c := range p.Retire {
			names = append(names, c.Name)
		}
		fmt.Fprintf(w, "  stop and mask:        %s\n", strings.Join(names, ", "))
	}
	if len(p.Mask) > 0 {
		fmt.Fprintf(w, "  mask and keep:        %s\n", strings.Join(p.Mask, ", "))
	}
	if len(p.Remove) > 0 {
		var names []string
		for _, r := range p.Remove {
			names = append(names, r.Label+" ("+strings.Join(r.Packages, " ")+")")
		}
		fmt.Fprintf(w, "  remove:               %s\n", strings.Join(names, ", "))
	}
	if len(p.Kept) > 0 {
		fmt.Fprintf(w, "  kept (--keep):        %s\n", strings.Join(p.Kept, ", "))
	}
	if p.Preview != "" {
		fmt.Fprintf(w, "\nthe package manager says removing them would do this:\n")
		for _, line := range strings.Split(p.Preview, "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	if p.Refused != nil {
		fmt.Fprintf(w, "\n%v\n", p.Refused)
	}
}

// InstallComponents is the first half of a plan: the packages and units
// a router needs. It runs before anything is taken away, so that what is
// taken away can never be a dependency of what is needed, and while the
// router still resolves names the way its image set it up to.
func InstallComponents(ctx context.Context, d Deps, p InstallPlan) (string, error) {
	if len(p.Install) == 0 {
		return "", nil
	}
	var keys []string
	for _, c := range p.Install {
		keys = append(keys, c.Key)
	}
	return SetUp(ctx, d, keys, false)
}

// RetireAndRemove is the second half: the old firewall is stopped and
// masked, what it left in the kernel is cleared while the commands that
// can clear it are still installed, and then the packages go. It refuses
// a plan the preview refused.
func RetireAndRemove(ctx context.Context, d Deps, p InstallPlan) (string, error) {
	if p.Refused != nil {
		return "", p.Refused
	}
	var said []string
	if units := p.units(); len(units) > 0 {
		if err := install.Takeover(ctx, d.Units, units, d.log()); err != nil {
			return strings.Join(said, "\n"), err
		}
		said = append(said, "masked and stopped: "+strings.Join(units, ", "))
	}
	if out, err := FlushLegacy(ctx, d, nil); err == nil {
		said = append(said, out)
	} else if !errors.Is(err, iptables.ErrNothingToFlush) {
		return strings.Join(said, "\n"), err
	}
	if pkgs := p.packages(); len(pkgs) > 0 {
		out, err := d.Packages.Remove(ctx, pkgs, false)
		if err != nil {
			return strings.Join(said, "\n"), fmt.Errorf("remove %s: %w: %s", join(pkgs), err, tail([]byte(out)))
		}
		said = append(said, "removed: "+strings.Join(pkgs, ", "))
		// A firewall's package removal runs its own stop script, and
		// ufw's leaves behind the empty filter tables iptables-nft
		// creates on the way: swept again now that nothing owns them.
		if out, err := FlushLegacy(ctx, d, nil); err == nil {
			said = append(said, out)
		} else if !errors.Is(err, iptables.ErrNothingToFlush) {
			return strings.Join(said, "\n"), err
		}
	}
	return strings.Join(said, "\n"), nil
}
