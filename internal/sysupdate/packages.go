package sysupdate

// Ostiole drives the software a distribution already packages rather than
// reimplementing it: DHCP is dnsmasq, the validating resolver is unbound,
// the queue is tc. That is a good bargain right up to the moment somebody
// has to get those packages onto the router, which is what this file is
// for.
//
// A component is one such piece — what it is called, what stops working
// without it, how to tell whether it is there, and what the package is
// named on each manager. That last part is the whole reason this is a
// table and not a constant: the same tc command is iproute-tc on Fedora,
// iproute2 on Debian and iproute2-tc on Alpine, and a router told to
// install the wrong one is told something useless.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Availability is what can be done about a component this router does not
// have.
type Availability string

// The three answers, which are three different conversations with the
// operator: install it, install nothing because it arrives with something
// else, or give up because nobody packages it here.
const (
	// Installable means this manager has a package for it.
	Installable Availability = "installable"
	// Bundled means it arrives with something else — systemd, usually —
	// so a missing one is a fact about the distribution rather than
	// something an install can fix.
	Bundled Availability = "bundled"
	// Unpackaged means nobody ships it for this distribution.
	Unpackaged Availability = "unpackaged"
)

// Component is a piece of the operating system Ostiole drives.
type Component struct {
	// Key names it in the API and on the command line.
	Key string
	// Label is what a page calls it.
	Label string
	// Needs says what does not work until it is there, in the words shown
	// beside the row.
	Needs string
	// Binary is the command that proves it is present. Daemons live in
	// sbin, which is not always on a service's PATH, so Locate looks
	// there too.
	Binary string
	// Unit proves it instead, for a component that is part of systemd and
	// has no command of its own.
	Unit string
	// packages names it per manager, and says as much by what it leaves
	// out: a manager with no entry does not package it at all, and an
	// entry with no names ships it with something else.
	packages map[string][]string
	// Note is the caveat worth printing beside the row, for the one
	// component that is genuinely awkward on one family of distributions.
	Note string
}

// upnpNote is the Red Hat family problem in one sentence. There is no EPEL
// branch for miniupnpd, and the Fedora build runs on EL unchanged, so the
// answer is to unpack one rather than to go without.
const upnpNote = "The Red Hat family has no EPEL build of miniupnpd. The Fedora package runs " +
	"unchanged on EL: unpack one with `rpm2cpio miniupnpd-*.fc*.x86_64.rpm | cpio -idmv` and put " +
	"usr/sbin/miniupnpd in /usr/local/sbin."

// Components are the pieces Ostiole can put on a router, in the order a
// page lists them: the firewall first, because nothing works without it,
// then the services, then the things only some routers want.
func Components() []Component {
	return []Component{{
		Key:   "nft",
		Label: "nftables",
		Needs: "the firewall itself: Ostiole renders rules for nft and loads them with it",
		// nft lives in sbin on most distributions.
		Binary: "nft",
		packages: map[string][]string{
			"dnf": {"nftables"}, "apt-get": {"nftables"}, "pacman": {"nftables"},
			"zypper": {"nftables"}, "apk": {"nftables"},
		},
	}, {
		Key:    "dnsmasq",
		Label:  "DHCP and DNS",
		Needs:  "handing out addresses and answering names for the networks behind this router",
		Binary: "dnsmasq",
		packages: map[string][]string{
			"dnf": {"dnsmasq"}, "apt-get": {"dnsmasq"}, "pacman": {"dnsmasq"},
			"zypper": {"dnsmasq"}, "apk": {"dnsmasq"},
		},
	}, {
		Key:    "unbound",
		Label:  "Validating resolver",
		Needs:  "checking DNSSEC and speaking DNS over TLS instead of forwarding in the clear",
		Binary: "unbound",
		packages: map[string][]string{
			"dnf": {"unbound"}, "apt-get": {"unbound"}, "pacman": {"unbound"},
			"zypper": {"unbound"}, "apk": {"unbound"},
		},
	}, {
		Key:   "miniupnpd",
		Label: "Port mapping",
		Needs: "letting clients ask for their own port forwards over UPnP, NAT-PMP and PCP",
		// Debian and Alpine ship both builds and choose by package name;
		// the iptables build would write mappings into tables Ostiole's
		// chains never see. Arch has it in the AUR alone, which is not
		// something to install on anybody's behalf.
		Binary: "miniupnpd",
		packages: map[string][]string{
			"dnf": {"miniupnpd"}, "apt-get": {"miniupnpd-nftables"},
			"zypper": {"miniupnpd"}, "apk": {"miniupnpd-nftables"},
		},
		Note: upnpNote,
	}, {
		Key:    "pppd",
		Label:  "PPPoE",
		Needs:  "dialling a session over Ethernet, which is how many lines are delivered",
		Binary: "pppd",
		packages: map[string][]string{
			"dnf": {"ppp"}, "apt-get": {"ppp"}, "pacman": {"ppp"}, "zypper": {"ppp"},
			// Alpine packages the daemon and the Ethernet plugin apart.
			"apk": {"ppp-daemon", "ppp-pppoe"},
		},
	}, {
		Key:    "tc",
		Label:  "Traffic shaping",
		Needs:  "holding the queue on a busy line so a download cannot drown a call",
		Binary: "tc",
		packages: map[string][]string{
			"dnf": {"iproute-tc"}, "apt-get": {"iproute2"}, "pacman": {"iproute2"},
			"zypper": {"iproute2"}, "apk": {"iproute2-tc"},
		},
	}, {
		Key:   "networkd",
		Label: "Networking",
		Needs: "owning the addresses, routes and links on this router",
		Unit:  "systemd-networkd.service",
		packages: map[string][]string{
			// Only the Red Hat family packages it apart from systemd, and
			// there it comes from EPEL.
			"dnf": {"systemd-networkd"},
			// Everywhere else it is part of systemd, so there is nothing
			// to install and a missing unit means something else is wrong.
			"apt-get": {}, "pacman": {}, "zypper": {},
			// Alpine has no systemd at all, so it has no networkd either.
		},
	}}
}

// ComponentByKey finds a component by key.
func ComponentByKey(key string) (Component, bool) {
	for _, c := range Components() {
		if c.Key == key {
			return c, true
		}
	}
	return Component{}, false
}

// Packages names what to install on a router with this manager, and says
// whether installing anything is the right idea at all.
func (c Component) Packages(manager string) ([]string, Availability) {
	pkgs, known := c.packages[manager]
	switch {
	case !known:
		return nil, Unpackaged
	case len(pkgs) == 0:
		return nil, Bundled
	}
	return pkgs, Installable
}

// Present reports whether the component is on this router. Both ways of
// asking are passed in: a component known by a command is looked for on
// the filesystem, and one known by a unit is asked about through
// systemctl. A nil find falls back to Locate; a nil known answers "no
// unit here".
func (c Component) Present(find func(name string) string, known func(unit string) bool) bool {
	if find == nil {
		find = Locate
	}
	if c.Binary != "" {
		return find(c.Binary) != ""
	}
	if c.Unit != "" && known != nil {
		return known(c.Unit)
	}
	return false
}

// sbinDirs are where package managers put daemons. A service's PATH does
// not always include them, so a binary that is plainly installed can look
// missing to the daemon that needs it.
var sbinDirs = []string{"/usr/sbin", "/sbin", "/usr/local/sbin"}

// Locate finds a command on PATH or in the sbin directories, and returns
// where it is or "".
func Locate(name string) string {
	if p, err := lookPath(name); err == nil {
		return p
	}
	for _, dir := range sbinDirs {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// InstallTimeout bounds an install or a removal. Fetching one package over
// a slow line is normal; ten minutes of it is not.
const InstallTimeout = 10 * time.Minute

// ErrNothingToDo means every package asked for is already where it should
// be, which is a success with nothing to report.
var ErrNothingToDo = errors.New("nothing to install")

// Remove takes packages off the router. With preview it changes nothing
// and returns what the manager says it would do, which is the only honest
// way to ask somebody to agree to a removal: the manager, not Ostiole,
// decides what else goes with it.
func (m *Manager) Remove(ctx context.Context, pkgs []string, preview bool) (string, error) {
	if len(pkgs) == 0 {
		return "", ErrNothingToDo
	}
	if err := m.begin(false); err != nil {
		return "", err
	}
	defer m.end()
	argv := m.Driver.RemoveArgv(pkgs, preview)
	if len(argv) == 0 {
		return "", fmt.Errorf("%s cannot remove packages", m.Driver.Name())
	}
	if preview {
		out, err := m.runFor(ctx, argv, InstallTimeout)
		if previewRefused(err) {
			// The manager printed its transaction and then declined to run
			// it, which is exactly what a preview asked it to do.
			return out, nil
		}
		return out, err
	}
	m.Log.Warn("removing packages", "manager", m.Driver.Name(), "packages", strings.Join(pkgs, " "))
	return m.runFor(ctx, argv, InstallTimeout)
}

// Installed reports whether a package is in the router's package
// database, which is a different question from whether its command is on
// PATH: a competitor can be installed, masked and idle all at once.
func (m *Manager) Installed(ctx context.Context, pkg string) (bool, error) {
	if !m.Available() {
		return false, m.unavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	return m.Driver.Installed(ctx, m.Run, pkg)
}

// runFor is run with a timeout of its own, for the operations that are
// not a distro upgrade and should not be given an hour.
func (m *Manager) runFor(ctx context.Context, argv []string, timeout time.Duration) (string, error) {
	if m.direct || !m.unit.supported() {
		return runDirect(ctx, m.Run, argv, timeout)
	}
	if err := m.unit.start(ctx, argv, timeout); err != nil {
		return "", err
	}
	return m.collect(context.WithoutCancel(ctx))
}

// previewRefused reports whether an error is a manager saying it changed
// nothing. dnf's --assumeno prints the transaction and exits 1; every
// other manager's dry run exits 0, so anything worse than 1 is real.
func previewRefused(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, errUnitStart) || errors.Is(err, ErrBusy) {
		// The manager never ran, so it declined nothing.
		return false
	}
	return exitCode(err) == 1
}
