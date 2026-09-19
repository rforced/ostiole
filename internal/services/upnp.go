package services

import (
	"context"
	"crypto/sha1" //nolint:gosec // a v5 UUID is defined as SHA-1; see uuidV5
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
)

// miniupnpd paths and names.
const (
	UPnPUnit = "ostiole-miniupnpd.service"
	// UPnPDir holds the generated configuration. It is the package's own
	// directory for the reason unbound taught us: SELinux labels what is
	// in there the way the daemon's policy expects.
	UPnPDir = "/etc/miniupnpd"
	upnpHTTPPort  = 2189
	upnpConfName  = "ostiole.conf"
	upnpDistroSvc = "miniupnpd.service"
	// machineIDPath seeds the device UUID; see uuid.
	machineIDPath = "/etc/machine-id"
)

// upnpNamespace is the UUID namespace the device id is derived under. Any
// fixed value keeps the id stable across restarts; one of our own keeps it
// from colliding with an id another tool derived from the same machine.
// The bytes spell "ostiole-upnp-igd".
var upnpNamespace = [16]byte{
	'o', 's', 't', 'i', 'o', 'l', 'e', '-', 'u', 'p', 'n', 'p', '-', 'i', 'g', 'd',
}

// UPnP runs miniupnpd, which answers UPnP IGD, NAT-PMP and PCP and opens
// the holes clients ask for (ADR-0007). It implements network.Backend so
// the engine applies and reverts it with everything else.
//
// The rules it writes go into chains Ostiole renders inside its own table:
// miniupnpd's nftables backend adds to chains it is given the names of and
// creates none, so nothing of ours ends up in a foreign table.
type UPnP struct {
	// Dir holds the generated configuration; default UPnPDir.
	Dir string
	// UUID overrides the device id, which is otherwise derived from the
	// machine id. Tests set it; a router has no reason to.
	UUID string
	// Cmd runs systemctl; default execs it.
	Cmd network.Commander
}

var _ network.Backend = (*UPnP)(nil)

// NewUPnP returns a backend with production defaults.
func NewUPnP() *UPnP {
	return &UPnP{Dir: UPnPDir, Cmd: execCommander{}}
}

func (u *UPnP) dir() string {
	if u.Dir == "" {
		return UPnPDir
	}
	return u.Dir
}

func (u *UPnP) cmd() network.Commander {
	if u.Cmd == nil {
		return execCommander{}
	}
	return u.Cmd
}

// Name implements network.Backend.
func (u *UPnP) Name() string { return "upnp" }

// ConfPath is the generated configuration file.
func (u *UPnP) ConfPath() string { return filepath.Join(u.dir(), upnpConfName) }

// Render implements network.Backend. With UPnP off it returns no files,
// which Apply turns into "unit stopped".
func (u *UPnP) Render(cfg *model.Config) (network.Files, error) {
	files := network.Files{}
	if !nft.UPnPEnabled(cfg) {
		return files, nil
	}
	files[upnpConfName] = u.render(cfg)
	return files, nil
}

func (u *UPnP) render(cfg *model.Config) string {
	up := cfg.Services.UPnP
	var b strings.Builder
	b.WriteString(fileHeader)
	fmt.Fprintf(&b, "ext_ifname=%s\n", up.ExternalInterface)
	// One line per interface clients may ask from. miniupnpd takes an
	// interface name here as well as an address, which is what lets this
	// follow a LAN that renumbers.
	for _, name := range nft.UPnPInterfaces(cfg) {
		fmt.Fprintf(&b, "listening_ip=%s\n", name)
	}
	fmt.Fprintf(&b, "http_port=%d\n", upnpHTTPPort)
	fmt.Fprintf(&b, "enable_upnp=%s\n", yesNo(up.IGD))
	// One daemon answers NAT-PMP and the PCP that replaced it, under the
	// older option name: it is in the option table of every miniupnpd from
	// 2.1 to 2.3, and the newer spelling is not.
	fmt.Fprintf(&b, "enable_natpmp=%s\n", yesNo(up.PCP))
	// A client may only map a port to its own address. Without this one
	// host on the LAN can open a hole to another, which is how a browser
	// tab ends up forwarding a port to the NAS.
	b.WriteString("secure_mode=yes\n")
	// IPv6 pinholes are not modelled yet, so they are not offered.
	b.WriteString("ipv6_disable=yes\n")
	// Report this router's uptime rather than the daemon's: a client that
	// sees the gateway restart drops its mappings.
	b.WriteString("system_uptime=yes\n")
	// Sweep expired mappings every ten minutes, so a client that went away
	// without cleaning up does not leave its hole open until a reboot.
	b.WriteString("clean_ruleset_interval=600\n")
	fmt.Fprintf(&b, "uuid=%s\n", u.uuid())
	// Nothing here may name an option that is not compiled into every
	// build. miniupnpd refuses its whole configuration over one option it
	// was not built with, so an extra line does not degrade the service, it
	// stops the daemon starting at all. Two were learned the hard way
	// against Fedora's build (ADR-0007):
	//
	//   lease_file     needs --leasefile. Without it an apply takes the
	//                  mappings with it, because the table is rebuilt and
	//                  there is nothing to reload them from. Clients ask
	//                  again on their own schedule.
	//   friendly_name  needs ENABLE_MANUFACTURER_INFO_CONFIGURATION. The
	//                  daemon falls back to its own name for this router in
	//                  a client's network list.
	// Where the rules go. Naming the table and chains is the whole reason
	// miniupnpd can live inside `table inet ostiole`; without
	// upnp_nftables_family_split it addresses the inet family, which is
	// the family our table is in.
	fmt.Fprintf(&b, "upnp_table_name=%s\n", upnpTableName)
	fmt.Fprintf(&b, "upnp_nat_table_name=%s\n", upnpTableName)
	fmt.Fprintf(&b, "upnp_forward_chain=%s\n", nft.UPnPForwardChain)
	fmt.Fprintf(&b, "upnp_nat_chain=%s\n", nft.UPnPPreroutingChain)
	fmt.Fprintf(&b, "upnp_nat_postrouting_chain=%s\n", nft.UPnPPostroutingChain)
	for _, r := range up.ACL {
		fmt.Fprintf(&b, "%s %s %s %s\n", r.Action,
			portRange(r.ExternalPorts), sourcePrefix(r.Source), portRange(r.InternalPorts))
	}
	if up.DefaultDeny {
		// Last, so the entries above are what gets through. miniupnpd
		// reads the list in order and stops at the first match.
		b.WriteString("deny 0-65535 0.0.0.0/0 0-65535\n")
	}
	return b.String()
}

// upnpTableName is the table the rules go in, without its family: the
// option takes a name, and the family comes from the daemon's build.
const upnpTableName = "ostiole"

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// portRange and sourcePrefix canonicalise what the access list was written
// with. Both are validated before they get here, so a value that will not
// parse is passed through rather than dropped: a line miniupnpd rejects is
// easier to explain than one that silently went missing.
func portRange(s string) string {
	pr, err := model.ParsePortRange(s)
	if err != nil {
		return strings.TrimSpace(s)
	}
	return pr.String()
}

func sourcePrefix(s string) string {
	p, err := model.ParseAddress(s)
	if err != nil {
		return strings.TrimSpace(s)
	}
	return p.String()
}

// uuid identifies this gateway to clients. It is derived from the machine
// id rather than kept in a state file: the same router answers with the same
// id after an upgrade or a reinstall of Ostiole, and a golden test does
// not depend on which router ran it. A router with no machine id gets a fixed
// one, which is worse for a client that meets two of them and better than
// a new id at every restart.
func (u *UPnP) uuid() string {
	if u.UUID != "" {
		return u.UUID
	}
	seed := "ostiole"
	if raw, err := os.ReadFile(machineIDPath); err == nil {
		if id := strings.TrimSpace(string(raw)); id != "" {
			seed = id
		}
	}
	return uuidV5(upnpNamespace, seed)
}

// uuidV5 is RFC 4122's name-based UUID. The hash is SHA-1 because that is
// what the format is defined as; nothing here is a security claim.
func uuidV5(namespace [16]byte, name string) string {
	h := sha1.New() //nolint:gosec // the UUID format, not cryptography
	h.Write(namespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)
	sum[6] = sum[6]&0x0f | 0x50 // version 5
	sum[8] = sum[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// Snapshot implements network.Backend.
func (u *UPnP) Snapshot() (network.Files, error) {
	files := network.Files{}
	raw, err := os.ReadFile(u.ConfPath())
	switch {
	case err == nil:
		files[upnpConfName] = string(raw)
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	return files, nil
}

// Apply implements network.Backend: write the configuration and start,
// restart, or stop the unit to match.
func (u *UPnP) Apply(ctx context.Context, files network.Files) error {
	conf, wanted := files[upnpConfName]
	current, err := u.Snapshot()
	if err != nil {
		return err
	}
	if !wanted {
		// A router that does not run this, and never has, gets no directory,
		// no file and no systemctl. Every apply runs every backend, and
		// the daemon's sandbox is narrow: work nobody asked for is how an
		// unrelated apply fails on a path it should never have touched.
		if len(current) == 0 {
			return nil
		}
		if _, err := u.cmd().Run(ctx, "systemctl", "disable", "--now", UPnPUnit); err != nil {
			return fmt.Errorf("stop %s: %w", UPnPUnit, err)
		}
		return os.Remove(u.ConfPath())
	}
	if !u.Installed(ctx) {
		return errors.New("UPnP is not set up on this router: run `ostiole repair` once as root")
	}
	if current[upnpConfName] != conf {
		if err := os.MkdirAll(u.dir(), 0o755); err != nil { //nolint:gosec // miniupnpd reads this
			return err
		}
		if err := writeFile(u.ConfPath(), conf); err != nil {
			return err
		}
	}
	if out, err := u.cmd().Run(ctx, "systemctl", "enable", "--now", UPnPUnit); err != nil {
		return fmt.Errorf("enable %s: %w: %s", UPnPUnit, err, strings.TrimSpace(string(out)))
	}
	// Restart even when the configuration has not changed. The apply that
	// got here deleted and recreated `table inet ostiole`, so every rule
	// miniupnpd had inserted went with it; it rebuilds them from the lease
	// file at startup, so the mappings themselves survive.
	if out, err := u.cmd().Run(ctx, "systemctl", "restart", UPnPUnit); err != nil {
		return fmt.Errorf("restart %s: %w: %s", UPnPUnit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Installed reports whether the ostiole-miniupnpd unit exists.
func (u *UPnP) Installed(ctx context.Context) bool {
	_, err := u.cmd().Run(ctx, "systemctl", "cat", UPnPUnit)
	return err == nil
}

// Active reports whether the unit is running.
func (u *UPnP) Active(ctx context.Context) bool {
	out, err := u.cmd().Run(ctx, "systemctl", "is-active", UPnPUnit)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// Mappings are read from the ruleset rather than from here: see
// nft.ParseMappings, and ADR-0007 for why there is no lease file.
