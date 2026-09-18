package services

import (
	"context"
	"debug/elf"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/tailscale"
)

// Runner runs commands (systemctl); swapped for a fake in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// SetupOptions tunes Setup.
type SetupOptions struct {
	// Dnsmasq sets up DHCP and DNS: masks the units that would fight
	// dnsmasq for port 53, and takes over resolv.conf. It is a choice of
	// its own because a router can want a dialled line or a shaped queue
	// and no name service at all, and masking systemd-resolved on that
	// router would be an act of vandalism.
	Dnsmasq bool
	// NoRestart leaves a running ostiole.service alone at the end. The
	// daemon's mount namespace is built once, at start, so it normally has
	// to be restarted to see the directories this creates; when the daemon
	// itself is driving Setup it restarts once it has answered, because a
	// restart from in here would kill the request that asked.
	NoRestart bool
	// UnitDir is where the unit is written; default /etc/systemd/system.
	UnitDir string
	// Run executes systemctl commands.
	Run Runner
	// Binary overrides the dnsmasq path (found on PATH otherwise).
	Binary string
	// Resolver writes unbound's unit, so the DNS service can validate
	// DNSSEC or speak DNS over TLS.
	Resolver bool
	// Unbound is the backend to set up when Resolver is set; nil means
	// production defaults.
	Unbound *Unbound
	// UnboundBinary overrides the unbound path (found on PATH otherwise).
	UnboundBinary string
	// PPPoE writes the templated unit that dials a session.
	PPPoE bool
	// PPPBinary overrides the pppd path (found on PATH otherwise).
	PPPBinary string
	// PPPoEBackend is set up when PPPoE is set; nil means production
	// defaults.
	PPPoEBackend *PPPoE
	// UPnP writes miniupnpd's unit, so clients can ask for their own port
	// mappings.
	UPnP bool
	// UPnPBinary overrides the miniupnpd path (found on PATH otherwise).
	UPnPBinary string
	// UPnPBackend is set up when UPnP is set; nil means production
	// defaults.
	UPnPBackend *UPnP
	// Tailscale writes tailscaled's unit, so the router can join a tailnet.
	Tailscale bool
	// TailscaleBinary overrides the tailscaled path (found on PATH
	// otherwise).
	TailscaleBinary string
	// ConfigDir is where the Tailscale and wireless backends' files go;
	// empty is the default configuration directory.
	ConfigDir string
	// Wireless writes the templated unit that serves a radio's networks.
	Wireless bool
	// HostapdBinary overrides the hostapd path (found on PATH otherwise).
	HostapdBinary string
}

// Setup makes the host able to run the services it is asked for. Each
// piece is a flag, and each writes a unit of Ostiole's that points a
// distribution's daemon at a generated configuration. With Dnsmasq it
// masks the distro dnsmasq and systemd-resolved (both would fight over
// port 53) and turns /etc/resolv.conf into a regular file Ostiole
// manages.
//
// Nothing here installs anything. The install script puts the daemons on
// the router; a piece whose binary is not there is skipped with a line in
// the log, because a router that never dials a line is not broken for
// having no pppd.
func Setup(ctx context.Context, d *Dnsmasq, o SetupOptions, log *slog.Logger) error {
	run := o.Run
	if run == nil {
		run = execRunner{}
	}
	unitDir := o.UnitDir
	if unitDir == "" {
		unitDir = "/etc/systemd/system"
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil { //nolint:gosec // systemd unit dir
		return err
	}
	bin := ""
	if o.Dnsmasq {
		var err error
		if bin, err = setupDnsmasq(ctx, d, o, run, unitDir, log); err != nil {
			return err
		}
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
	if o.Tailscale {
		if err := setupTailscale(ctx, run, o, unitDir, log); err != nil {
			return err
		}
	}
	if o.Wireless {
		if err := setupWireless(ctx, run, o, unitDir, log); err != nil {
			return err
		}
	}
	if _, err := run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if !o.NoRestart {
		restartDaemon(ctx, run, log)
	}
	if bin != "" {
		log.Info("services ready", "unit", Unit, "dnsmasq", bin)
	} else {
		log.Info("services ready")
	}
	return nil
}

// resolvedUpstreams is where systemd-resolved writes the resolvers it
// actually forwards to, as opposed to the stub it points resolv.conf at.
const resolvedUpstreams = "/run/systemd/resolve/resolv.conf"

// setupDnsmasq is the DHCP and DNS part: the units that have to stop
// fighting it for port 53, resolv.conf, and our own unit. It returns
// where dnsmasq is, or "" when this router has none.
func setupDnsmasq(ctx context.Context, d *Dnsmasq, o SetupOptions, run Runner, unitDir string, log *slog.Logger) (string, error) {
	bin := o.Binary
	if bin == "" {
		bin = lookPath("dnsmasq")
	}
	if bin == "" {
		log.Info("no dnsmasq on this router; DHCP and DNS are not set up")
		return "", nil
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
		if err := freezeResolv(d.Resolv, log); err != nil {
			return "", err
		}
	}

	if err := os.MkdirAll(filepath.Dir(d.leases()), 0o755); err != nil { //nolint:gosec // dnsmasq writes leases here
		return "", err
	}
	if err := os.MkdirAll(d.dir(), 0o755); err != nil { //nolint:gosec // dnsmasq reads these unprivileged
		return "", err
	}
	if err := writeFile(filepath.Join(unitDir, Unit), UnitContent(bin, filepath.Join(d.dir(), confName))); err != nil {
		return "", err
	}
	return bin, nil
}

// freezeResolv turns /etc/resolv.conf into a regular file holding the
// resolvers the router is using now. Both managers that own the path
// point it at something under /run that goes away with them: resolved at
// its 127.0.0.53 stub, NetworkManager at a file in its own runtime
// directory. In a container the path is a bind-mounted regular file
// already, and is left as it is.
func freezeResolv(path string, log *slog.Logger) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	raw, _ := os.ReadFile(path)
	target, _ := os.Readlink(path)
	// The stub is the one target whose contents are no use once resolved
	// is masked: "nameserver 127.0.0.53" would leave the router unable to
	// resolve anything until the DNS service is applied. resolved keeps
	// the real upstreams in a file of its own.
	if strings.Contains(string(raw), "127.0.0.53") {
		if upstream, err := os.ReadFile(resolvedUpstreams); err == nil && strings.Contains(string(upstream), "nameserver") {
			raw = upstream
			log.Info("kept systemd-resolved's upstream resolvers in resolv.conf", "from", resolvedUpstreams)
		}
	}
	_ = os.Remove(path)
	if err := writeFile(path, string(raw)); err != nil {
		return err
	}
	log.Info("replaced the resolv.conf symlink with a regular file", "was", target)
	return nil
}

// setupResolver bootstraps the DNSSEC trust anchor and writes the
// ostiole-unbound unit. The distro's own unbound is masked: it would bind
// port 53 and fight dnsmasq.
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
		log.Info("no unbound on this router; the validating resolver is not set up")
		return nil
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

// restartDaemon rebuilds the running daemon's mount namespace.
//
// systemd builds that namespace once, when the unit starts, and a
// ReadWritePaths entry written with a leading dash is skipped while its
// path is missing. Setting a service up is what creates the directory its
// daemon reads, so a daemon that was already running goes on seeing that
// directory under a read-only /etc however its unit reads now, and the
// next apply fails with "read-only file system" on a path the unit plainly
// lists. Restarting is the only thing that rebuilds it, and it costs
// nothing visible: sessions are kept on disk, so nobody is signed out.
func restartDaemon(ctx context.Context, run Runner, log *slog.Logger) {
	out, err := run.Run(ctx, "systemctl", "is-active", install.DaemonUnit)
	if err != nil || strings.TrimSpace(string(out)) != "active" {
		// Not running, so whenever it next starts it builds the namespace
		// it needs.
		return
	}
	if out, err := run.Run(ctx, "systemctl", "restart", install.DaemonUnit); err != nil {
		log.Warn("could not restart the daemon: restart it before applying",
			"unit", install.DaemonUnit, "err", err, "output", strings.TrimSpace(string(out)))
		return
	}
	log.Info("restarted the daemon so it can write the directories this added", "unit", install.DaemonUnit)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// setupPPPoE writes the templated unit that dials a session. The rp-pppoe
// plugin ships with pppd on every distribution that packages it, so
// finding pppd is enough.
func setupPPPoE(_ context.Context, _ Runner, o SetupOptions, unitDir string, log *slog.Logger) error {
	p := o.PPPoEBackend
	if p == nil {
		p = NewPPPoE()
	}
	bin := o.PPPBinary
	if bin == "" {
		bin = lookPath("pppd")
	}
	if bin == "" {
		log.Info("no pppd on this router; PPPoE is not set up")
		return nil
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

// setupUPnP masks the distro's own unit and writes
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
		log.Info("no miniupnpd on this router; port mapping is not set up")
		return nil
	}
	if err := nftablesBuild(bin); err != nil {
		return err
	}

	if out, err := run.Run(ctx, "systemctl", "cat", upnpDistroSvc); err == nil && len(out) > 0 {
		_, _ = run.Run(ctx, "systemctl", "disable", "--now", upnpDistroSvc)
		_, _ = run.Run(ctx, "systemctl", "mask", upnpDistroSvc)
		log.Info("masked the distribution's own mapping service", "unit", upnpDistroSvc)
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

// setupTailscale masks the distribution's own unit — the package enables
// it — and writes ostiole-tailscaled.service. Nothing here joins a
// tailnet: that is the interface, applied like everything else.
func setupTailscale(ctx context.Context, run Runner, o SetupOptions, unitDir string, log *slog.Logger) error {
	bin := o.TailscaleBinary
	if bin == "" {
		bin = lookPath("tailscaled")
	}
	if bin == "" {
		log.Info("no tailscaled on this router; Tailscale is not set up")
		return nil
	}

	if out, err := run.Run(ctx, "systemctl", "cat", tailscaleDistroSvc); err == nil && len(out) > 0 {
		_, _ = run.Run(ctx, "systemctl", "disable", "--now", tailscaleDistroSvc)
		_, _ = run.Run(ctx, "systemctl", "mask", tailscaleDistroSvc)
		log.Info("masked the distribution's own Tailscale unit", "unit", tailscaleDistroSvc)
	}

	dir := TailscaleDir(o.ConfigDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(unitDir, TailscaleUnit), TailscaleUnitContent(bin, dir)); err != nil {
		return err
	}
	log.Info("Tailscale ready", "unit", TailscaleUnit, "tailscaled", bin)
	return nil
}

// setupWireless masks the distribution's own hostapd unit and writes the
// templated one, one instance of which serves each radio. Nothing here
// brings a network up: that is an interface, applied like everything else.
func setupWireless(ctx context.Context, run Runner, o SetupOptions, unitDir string, log *slog.Logger) error {
	bin := o.HostapdBinary
	if bin == "" {
		bin = lookPath("hostapd")
	}
	iw, ipCmd := lookPath("iw"), lookPath("ip")
	if bin == "" || iw == "" || ipCmd == "" {
		log.Info("no hostapd on this router; wireless is not set up")
		return nil
	}

	if out, err := run.Run(ctx, "systemctl", "cat", hostapdDistroSvc); err == nil && len(out) > 0 {
		_, _ = run.Run(ctx, "systemctl", "disable", "--now", hostapdDistroSvc)
		_, _ = run.Run(ctx, "systemctl", "mask", hostapdDistroSvc)
		log.Info("masked the distribution's own hostapd unit", "unit", hostapdDistroSvc)
	}

	dir := WirelessDir(o.ConfigDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(unitDir, WirelessUnit), WirelessUnitContent(bin, iw, ipCmd, dir)); err != nil {
		return err
	}
	log.Info("wireless ready", "unit", WirelessUnit, "hostapd", bin)
	return nil
}

// WirelessUnitContent renders the templated hostapd unit. The instance
// name is the radio, which is also the name of both its files, so one
// template covers however many cards a router has.
func WirelessUnitContent(binary, iw, ipCmd, dir string) string {
	return fmt.Sprintf(`[Unit]
Description=Ostiole wireless networks on %%i (hostapd)
Documentation=https://github.com/rforced/ostiole
After=network-pre.target ostiole-firewall.service sys-subsystem-net-devices-%%i.device
Wants=network-pre.target ostiole-firewall.service
BindsTo=sys-subsystem-net-devices-%%i.device

[Service]
Type=exec
EnvironmentFile=%[4]s/%%i.env
ExecStartPre=%[3]s link set %%i up
# Some radios learn their country from the networks around them and forget
# it when the firmware restarts. A scan first is what lets them use 5 GHz.
ExecStartPre=-%[2]s dev %%i scan
ExecStartPre=-%[2]s phy ${PHY} set txpower $TXPOWER
ExecStartPre=-%[2]s dev %%i interface add ${AP} type __ap
ExecStart=%[1]s %[4]s/%%i.conf
ExecStopPost=-%[2]s dev ${AP} del
Restart=on-failure
RestartSec=5
RuntimeDirectory=hostapd/%%i

[Install]
WantedBy=multi-user.target
`, binary, iw, ipCmd, dir)
}

// TailscaleUnitContent renders the ostiole-tailscaled unit. The daemon
// starts before the firewall's rules would matter and cleans its own
// interface up on the way out; the port and the log flag come from the
// environment file an apply writes.
func TailscaleUnitContent(binary, dir string) string {
	return fmt.Sprintf(`[Unit]
Description=Ostiole Tailscale node (tailscaled)
Documentation=https://github.com/rforced/ostiole
After=network-pre.target ostiole-firewall.service
Wants=network-pre.target ostiole-firewall.service

[Service]
Type=notify
Environment=PORT=%[3]d
EnvironmentFile=-%[2]s/env
ExecStartPre=%[1]s --cleanup
ExecStart=%[1]s --state=/var/lib/tailscale/tailscaled.state --socket=/run/tailscale/tailscaled.sock --tun=%[4]s --port=${PORT} $FLAGS
ExecStopPost=%[1]s --cleanup
Restart=on-failure
RestartSec=2
RuntimeDirectory=tailscale
RuntimeDirectoryMode=0755
StateDirectory=tailscale
StateDirectoryMode=0700
CacheDirectory=tailscale
CacheDirectoryMode=0750

[Install]
WantedBy=multi-user.target
`, binary, dir, tailscale.DefaultPort, model.TailscaleDevice)
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
		// set up a router over a file format.
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
		"install the nftables build (Debian calls it miniupnpd-nftables)", bin)
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
