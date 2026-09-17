package services

import (
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner runs commands (package managers); swapped for a fake in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// SetupOptions tunes Setup.
type SetupOptions struct {
	// UnitDir is where the unit is written; default /etc/systemd/system.
	UnitDir string
	// PackageManager is dnf, apt-get, pacman, zypper, or apk; empty means
	// detect.
	PackageManager string
	// Run executes package manager and systemctl commands.
	Run Runner
	// Binary overrides the dnsmasq path (found on PATH otherwise).
	Binary string
	// Resolver also installs unbound and writes its unit, so the DNS
	// service can validate DNSSEC or speak DNS over TLS.
	Resolver bool
	// Unbound is the backend to set up when Resolver is set; nil means
	// production defaults.
	Unbound *Unbound
	// UnboundBinary overrides the unbound path (found on PATH otherwise).
	UnboundBinary string
	// PPPoE also installs pppd and writes the templated unit that dials a
	// session.
	PPPoE bool
	// PPPBinary overrides the pppd path (found on PATH otherwise).
	PPPBinary string
	// PPPoEBackend is set up when PPPoE is set; nil means production
	// defaults.
	PPPoEBackend *PPPoE
	// UPnP also installs miniupnpd and writes its unit, so clients can ask
	// for their own port mappings.
	UPnP bool
	// UPnPBinary overrides the miniupnpd path (found on PATH otherwise).
	UPnPBinary string
	// UPnPBackend is set up when UPnP is set; nil means production
	// defaults.
	UPnPBackend *UPnP
}

// Setup makes the host able to run the services: installs dnsmasq if
// missing, writes the ostiole-dnsmasq unit, masks the distro dnsmasq and
// systemd-resolved (both would fight over port 53), and turns
// /etc/resolv.conf into a regular file so Ostiole can manage it.
func Setup(ctx context.Context, d *Dnsmasq, o SetupOptions, log *slog.Logger) error {
	run := o.Run
	if run == nil {
		run = execRunner{}
	}
	unitDir := o.UnitDir
	if unitDir == "" {
		unitDir = "/etc/systemd/system"
	}
	bin := o.Binary
	if bin == "" {
		if p, err := exec.LookPath("dnsmasq"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		pm := o.PackageManager
		if pm == "" {
			pm = detectPackageManager()
		}
		if err := installPackage(ctx, run, pm, "dnsmasq", log); err != nil {
			return err
		}
		p, err := exec.LookPath("dnsmasq")
		if err != nil {
			return errors.New("dnsmasq still not found after installation")
		}
		bin = p
	}

	for _, unit := range []string{distroUnit, resolvedUnit} {
		if out, err := run.Run(ctx, "systemctl", "cat", unit); err != nil || len(out) == 0 {
			continue
		}
		_, _ = run.Run(ctx, "systemctl", "disable", "--now", unit)
		_, _ = run.Run(ctx, "systemctl", "mask", unit)
		log.Info("masked competing resolver", "unit", unit)
	}

	if d.Resolv != "" {
		if info, err := os.Lstat(d.Resolv); err == nil && info.Mode()&os.ModeSymlink != 0 {
			raw, _ := os.ReadFile(d.Resolv)
			_ = os.Remove(d.Resolv)
			if err := os.WriteFile(d.Resolv, raw, 0o644); err != nil { //nolint:gosec // world-readable by design
				return err
			}
			log.Info("replaced the resolv.conf symlink with a regular file")
		}
	}

	if err := os.MkdirAll(filepath.Dir(d.leases()), 0o755); err != nil { //nolint:gosec // dnsmasq writes leases here
		return err
	}
	if err := os.MkdirAll(d.dir(), 0o755); err != nil { //nolint:gosec // dnsmasq reads these unprivileged
		return err
	}
	unit := UnitContent(bin, filepath.Join(d.dir(), confName))
	if err := os.MkdirAll(unitDir, 0o755); err != nil { //nolint:gosec // systemd unit dir
		return err
	}
	if err := writeFile(filepath.Join(unitDir, Unit), unit); err != nil {
		return err
	}
	if o.Resolver {
		if err := setupResolver(ctx, run, o, unitDir, log); err != nil {
			return err
		}
	}
	if o.PPPoE {
		if err := setupPPPoE(ctx, run, o, unitDir, log); err != nil {
			return err
		}
	}
	if o.UPnP {
		if err := setupUPnP(ctx, run, o, unitDir, log); err != nil {
			return err
		}
	}
	if _, err := run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	log.Info("services ready", "unit", Unit, "dnsmasq", bin)
	return nil
}

// setupResolver installs unbound, bootstraps the DNSSEC trust anchor, and
// writes the ostiole-unbound unit. The distro's own unbound is masked: it
// would bind port 53 and fight dnsmasq.
func setupResolver(ctx context.Context, run Runner, o SetupOptions, unitDir string, log *slog.Logger) error {
	u := o.Unbound
	if u == nil {
		u = NewUnbound()
	}
	bin := o.UnboundBinary
	if bin == "" {
		bin = lookPath("unbound")
	}
	if bin == "" {
		pm := o.PackageManager
		if pm == "" {
			pm = detectPackageManager()
		}
		if err := installPackage(ctx, run, pm, "unbound", log); err != nil {
			return err
		}
		if bin = lookPath("unbound"); bin == "" {
			return errors.New("unbound still not found after installation")
		}
	}

	if out, err := run.Run(ctx, "systemctl", "cat", unboundDistroSvc); err == nil && len(out) > 0 {
		_, _ = run.Run(ctx, "systemctl", "disable", "--now", unboundDistroSvc)
		_, _ = run.Run(ctx, "systemctl", "mask", unboundDistroSvc)
		log.Info("masked competing resolver", "unit", unboundDistroSvc)
	}

	// The trust anchor lives outside our directories and belongs to
	// unbound, which updates it in place as the root keys roll over.
	anchorTool := lookPath("unbound-anchor")
	if err := os.MkdirAll(filepath.Dir(u.anchor()), 0o755); err != nil { //nolint:gosec // unbound reads and writes this
		return err
	}
	if anchorTool != "" {
		if _, err := run.Run(ctx, anchorTool, "-a", u.anchor()); err != nil {
			// Exit code 1 means "anchor written but not verified yet",
			// which is normal on a first run.
			log.Debug("unbound-anchor reported a problem", "err", err)
		}
		_, _ = run.Run(ctx, "chown", "unbound:unbound", u.anchor())
	}
	if err := os.MkdirAll(u.dir(), 0o755); err != nil { //nolint:gosec // unbound reads this unprivileged
		return err
	}
	unit := UnboundUnitContent(bin, lookPath("unbound-checkconf"), anchorTool, u.ConfPath(), u.anchor())
	if err := writeFile(filepath.Join(unitDir, UnboundUnit), unit); err != nil {
		return err
	}
	log.Info("validating resolver ready", "unit", UnboundUnit, "unbound", bin, "port", UnboundPort)
	return nil
}

func lookPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	// Package managers put daemons in sbin, which is not always on PATH
	// for a service.
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/local/sbin"} {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// UnitContent renders the ostiole-dnsmasq unit.
func UnitContent(binary, conf string) string {
	return fmt.Sprintf(`[Unit]
Description=Ostiole DHCP and DNS (dnsmasq)
Documentation=https://github.com/rforced/ostiole
After=network.target ostiole-firewall.service
Wants=ostiole-firewall.service

[Service]
Type=simple
ExecStartPre=%[1]s --test --conf-file=%[2]s
ExecStart=%[1]s --keep-in-foreground --conf-file=%[2]s
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=2
ProtectSystem=full
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`, binary, conf)
}

func detectPackageManager() string {
	for _, pm := range []string{"dnf", "apt-get", "pacman", "zypper", "apk"} {
		if _, err := exec.LookPath(pm); err == nil {
			return pm
		}
	}
	return ""
}

func installPackage(ctx context.Context, run Runner, pm, pkg string, log *slog.Logger) error {
	var args []string
	switch pm {
	case "dnf":
		args = []string{"dnf", "-y", "install", pkg}
	case "apt-get":
		args = []string{"apt-get", "install", "-y", "-q", pkg}
	case "pacman":
		args = []string{"pacman", "-S", "--noconfirm", pkg}
	case "zypper":
		args = []string{"zypper", "--non-interactive", "install", pkg}
	case "apk":
		args = []string{"apk", "add", pkg}
	default:
		return fmt.Errorf("%s is not installed and no supported package manager was found; install it and retry", pkg)
	}
	log.Info("installing package", "package", pkg, "with", pm)
	if out, err := run.Run(ctx, args[0], args[1:]...); err != nil {
		s := strings.TrimSpace(string(out))
		if len(s) > 400 {
			s = "…" + s[len(s)-400:]
		}
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, s)
	}
	return nil
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// setupPPPoE installs pppd and writes the templated unit. The rp-pppoe
// plugin ships with pppd on every distribution that packages it, so there
// is nothing else to fetch.
func setupPPPoE(ctx context.Context, run Runner, o SetupOptions, unitDir string, log *slog.Logger) error {
	p := o.PPPoEBackend
	if p == nil {
		p = NewPPPoE()
	}
	bin := o.PPPBinary
	if bin == "" {
		bin = lookPath("pppd")
	}
	if bin == "" {
		pm := o.PackageManager
		if pm == "" {
			pm = detectPackageManager()
		}
		for _, pkg := range pppPackages(pm) {
			if err := installPackage(ctx, run, pm, pkg, log); err != nil {
				return err
			}
		}
		if bin = lookPath("pppd"); bin == "" {
			return errors.New("pppd still not found after installation")
		}
	}
	// Peer files carry the provider password, so the directory is
	// root-only too.
	if err := os.MkdirAll(p.dir(), 0o700); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(unitDir, PPPoEUnit), PPPoEUnitContent(bin)); err != nil {
		return err
	}
	log.Info("PPPoE ready", "unit", PPPoEUnit, "pppd", bin)
	return nil
}

// pppPackages names the ppp daemon and, where it is packaged apart, the
// rp-pppoe plugin that dials over Ethernet.
func pppPackages(pm string) []string {
	if pm == "apk" {
		return []string{"ppp-daemon", "ppp-pppoe"}
	}
	return []string{"ppp"}
}

// upnpUnavailable is what a box gets when nobody packages the daemon for
// it. The Red Hat family is the case that matters, and the Fedora build
// runs there unchanged: the sonames it wants are the ones EL ships, and
// only the RPM's Fedora-only filesystem dependency stops a plain install.
// A binary already on the box is used wherever it is, so unpacking one is
// enough.
const upnpUnavailable = "miniupnpd is not packaged for this distribution, and there is no EPEL branch for it. " +
	"The Fedora build runs unchanged on Red Hat family boxes: unpack one with " +
	"`rpm2cpio miniupnpd-*.fc*.x86_64.rpm | cpio -idmv`, " +
	"install usr/sbin/miniupnpd into /usr/local/sbin, and run this again"

// setupUPnP installs miniupnpd, masks the distro's own unit, and writes
// ostiole-miniupnpd.service. Nothing here decides whether the service
// runs: that is the Enabled switch, applied like everything else.
func setupUPnP(ctx context.Context, run Runner, o SetupOptions, unitDir string, log *slog.Logger) error {
	u := o.UPnPBackend
	if u == nil {
		u = NewUPnP()
	}
	bin := o.UPnPBinary
	if bin == "" {
		bin = lookPath("miniupnpd")
	}
	if bin == "" {
		pm := o.PackageManager
		if pm == "" {
			pm = detectPackageManager()
		}
		pkg, ok := upnpPackage(pm)
		if !ok {
			return errors.New(upnpUnavailable)
		}
		if err := installPackage(ctx, run, pm, pkg, log); err != nil {
			return fmt.Errorf("%w\n\n%s", err, upnpUnavailable)
		}
		if bin = lookPath("miniupnpd"); bin == "" {
			return errors.New("miniupnpd still not found after installation")
		}
	}
	if err := nftablesBuild(bin); err != nil {
		return err
	}

	if out, err := run.Run(ctx, "systemctl", "cat", upnpDistroSvc); err == nil && len(out) > 0 {
		_, _ = run.Run(ctx, "systemctl", "disable", "--now", upnpDistroSvc)
		_, _ = run.Run(ctx, "systemctl", "mask", upnpDistroSvc)
		log.Info("masked the distribution's own mapping service", "unit", upnpDistroSvc)
	}

	if err := os.MkdirAll(filepath.Dir(u.leases()), 0o755); err != nil { //nolint:gosec // miniupnpd writes mappings here
		return err
	}
	if err := os.MkdirAll(u.dir(), 0o755); err != nil { //nolint:gosec // miniupnpd reads this
		return err
	}
	if err := writeFile(filepath.Join(unitDir, UPnPUnit), UPnPUnitContent(bin, u.ConfPath())); err != nil {
		return err
	}
	log.Info("mapping service ready", "unit", UPnPUnit, "miniupnpd", bin)
	return nil
}

// upnpPackage names the nftables build. Debian and Alpine ship both
// builds and choose between them by package name; Fedora and openSUSE
// package only the one. Arch has it in the AUR alone, which is not
// something to install on anyone's behalf.
func upnpPackage(pm string) (string, bool) {
	switch pm {
	case "apt-get", "apk":
		return "miniupnpd-nftables", true
	case "dnf", "zypper":
		return "miniupnpd", true
	default:
		return "", false
	}
}

// nftablesBuild checks that the binary is the one that writes nftables.
// miniupnpd picks its firewall at compile time, and the iptables build
// would make mappings in tables Ostiole's chains never see: every request
// would be answered and no packet would pass. The linked libraries say
// which build it is without running it.
func nftablesBuild(bin string) error {
	f, err := elf.Open(bin)
	if err != nil {
		// Not an ELF we can read, so take it on trust rather than refuse to
		// set up a box over a file format.
		return nil
	}
	defer func() { _ = f.Close() }()
	libs, err := f.ImportedLibraries()
	if err != nil || len(libs) == 0 {
		return nil
	}
	for _, lib := range libs {
		if strings.HasPrefix(lib, "libnftnl.so") {
			return nil
		}
	}
	return fmt.Errorf("%s is the iptables build of miniupnpd and cannot write Ostiole's chains; "+
		"install the nftables build (Debian and Alpine call it miniupnpd-nftables)", bin)
}

// UPnPUnitContent renders the ostiole-miniupnpd unit. -d is what keeps
// the daemon in the foreground, and it is the flag that does so whether
// or not the build was configured to fork at all.
func UPnPUnitContent(binary, conf string) string {
	return fmt.Sprintf(`[Unit]
Description=Ostiole UPnP IGD, PCP and NAT-PMP (miniupnpd)
Documentation=https://github.com/rforced/ostiole
After=network.target ostiole-firewall.service
Wants=ostiole-firewall.service

[Service]
Type=simple
ExecStart=%s -d -f %s
Restart=on-failure
RestartSec=2
ProtectSystem=full
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`, binary, conf)
}
