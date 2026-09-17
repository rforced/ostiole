package services

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // a v5 UUID is defined as SHA-1; see uuidV5
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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
	// UPnPLeaseFile is where miniupnpd records the mappings clients asked
	// for. It reads the file back at startup, which is what makes a
	// restart invisible to them.
	UPnPLeaseFile = "/var/lib/misc/ostiole-upnp.leases"
	// upnpHTTPPort serves the device description clients fetch after they
	// find this box over SSDP. It is pfSense's port, and the one the
	// firewall rules open.
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
	// Leases is the mapping file miniupnpd keeps; default UPnPLeaseFile.
	Leases string
	// UUID overrides the device id, which is otherwise derived from the
	// machine id. Tests set it; a box has no reason to.
	UUID string
	// Cmd runs systemctl; default execs it.
	Cmd network.Commander
}

var _ network.Backend = (*UPnP)(nil)

// NewUPnP returns a backend with production defaults.
func NewUPnP() *UPnP {
	return &UPnP{Dir: UPnPDir, Leases: UPnPLeaseFile, Cmd: execCommander{}}
}

func (u *UPnP) dir() string {
	if u.Dir == "" {
		return UPnPDir
	}
	return u.Dir
}

func (u *UPnP) leases() string {
	if u.Leases == "" {
		return UPnPLeaseFile
	}
	return u.Leases
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
	// Report this box's uptime rather than the daemon's: a client that
	// sees the gateway restart drops its mappings.
	b.WriteString("system_uptime=yes\n")
	// Sweep expired mappings every ten minutes, so a client that went away
	// without cleaning up does not leave its hole open until a reboot.
	b.WriteString("clean_ruleset_interval=600\n")
	fmt.Fprintf(&b, "lease_file=%s\n", u.leases())
	fmt.Fprintf(&b, "uuid=%s\n", u.uuid())
	fmt.Fprintf(&b, "friendly_name=%s\n", friendlyName(cfg))
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

// friendlyName is what clients show in their network list.
func friendlyName(cfg *model.Config) string {
	if h := strings.TrimSpace(cfg.System.Hostname); h != "" {
		return h + " (Ostiole)"
	}
	return "Ostiole"
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
// id rather than kept in a state file: the same box answers with the same
// id after an upgrade or a reinstall of Ostiole, and a golden test does
// not depend on which box ran it. A box with no machine id gets a fixed
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
		// A box that does not run this, and never has, gets no directory,
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
		return errors.New("UPnP is not set up on this box: run `ostiole services setup --with-upnp` once as root")
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

// Mapping is one hole a client opened for itself.
type Mapping struct {
	// Expires is when the mapping is taken away. It is absent when the
	// client asked for one with no lifetime, which a zero time.Time could
	// not say: it would reach the UI as the year 1.
	Expires      *time.Time `json:"expires,omitempty"`
	Protocol     string     `json:"protocol"`
	Internal     string     `json:"internal"`
	Description  string     `json:"description,omitempty"`
	ExternalPort int        `json:"externalPort"`
	InternalPort int        `json:"internalPort"`
}

// ReadMappings parses the lease file. A missing file is an empty list:
// miniupnpd writes it when the first mapping is made.
func (u *UPnP) ReadMappings() ([]Mapping, error) {
	f, err := os.Open(u.leases())
	if errors.Is(err, os.ErrNotExist) {
		return []Mapping{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseMappings(f)
}

// ParseMappings reads miniupnpd's lease file. A line is
// "PROTO:eport:iaddr:iport:expiry:description", or the same with the
// remote host between the external port and the internal address where
// the build was configured with SUPPORT_REMOTEHOST. Which one it is can be
// told from the line: in the short form the third field is an address and
// the fourth a number, and in the long form it is not.
func ParseMappings(r interface{ Read([]byte) (int, error) }) ([]Mapping, error) {
	out := []Mapping{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 5 {
			continue
		}
		off := 0
		if !isAddr(f[2]) || !isNumber(f[3]) {
			off = 1 // the remote host is in the way
		}
		if len(f) < 5+off {
			continue
		}
		eport, eok := port(f[1])
		iport, iok := port(f[3+off])
		if !eok || !iok || !isAddr(f[2+off]) {
			continue
		}
		m := Mapping{
			Protocol:     strings.ToUpper(f[0]),
			ExternalPort: eport,
			Internal:     f[2+off],
			InternalPort: iport,
		}
		// A description may hold colons of its own, so whatever is left of
		// the line after the timestamp is all of it.
		if len(f) > 5+off {
			m.Description = strings.Join(f[5+off:], ":")
		}
		if epoch, err := strconv.ParseInt(f[4+off], 10, 64); err == nil && epoch > 0 {
			exp := time.Unix(epoch, 0).UTC()
			m.Expires = &exp
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ExternalPort != out[j].ExternalPort {
			return out[i].ExternalPort < out[j].ExternalPort
		}
		return out[i].Protocol < out[j].Protocol
	})
	return out, nil
}

func isAddr(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func port(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}
