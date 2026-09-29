// Package install puts Ostiole on a systemd host: binary, units, config
// directory, competitor detection, the takeover that makes Ostiole the
// only firewall, and the handover that gives systemd-networkd the
// addresses. It also carries the shell installer the documentation tells
// people to pipe into sh.
package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/atomicfile"
	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/logging"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/sysctl"
	"github.com/rforced/ostiole/internal/timezone"
)

// LogDropInDirs are the unit drop-in directories the level cap is
// written into, in the form ReadWritePaths= takes: the leading dash means
// "only if it exists", though `ostiole install` creates every one of
// them.
func LogDropInDirs() []string {
	out := make([]string, 0, len(logging.Units))
	for _, u := range logging.Units {
		out = append(out, "-"+u.Dir(""))
	}
	return out
}

// Layout says where things go. Tests point it at temp directories.
type Layout struct {
	BinDir    string
	UnitDir   string
	ConfigDir string
	// BackupDir is where backup crons write; empty makes none.
	BackupDir string
	// LogDir holds the log files, which the unit's LogsDirectory makes.
	LogDir string
}

// DefaultLayout is the production layout.
func DefaultLayout() Layout {
	return Layout{BinDir: "/usr/local/bin", UnitDir: "/etc/systemd/system", ConfigDir: "/etc/ostiole",
		BackupDir: backup.Dir, LogDir: logfile.Dir}
}

// Binary is the installed executable path.
func (l Layout) Binary() string { return filepath.Join(l.BinDir, "ostiole") }

// DefaultListen is where the web UI listens unless told otherwise.
var DefaultListen = ":" + strconv.Itoa(model.DefaultWebPort)

// Listen reads the address the installed daemon unit serves the UI on, or
// "" when there is no unit or it names none. An install that is not told
// where to listen keeps this, so an update or a repair never moves the UI.
func Listen(lay Layout) string {
	raw, err := os.ReadFile(filepath.Join(lay.UnitDir, DaemonUnit))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "--listen" && i+1 < len(fields) {
				return fields[i+1]
			}
			if v, ok := strings.CutPrefix(f, "--listen="); ok {
				return v
			}
		}
	}
	return ""
}

// Installed reports whether the daemon's unit is on disk.
func Installed(lay Layout) bool {
	_, err := os.Stat(filepath.Join(lay.UnitDir, DaemonUnit))
	return err == nil
}

// Unit names.
const (
	FirewallUnit = "ostiole-firewall.service"
	DaemonUnit   = "ostiole.service"
)

// NetworkdUnitDir is where Ostiole writes its networkd units.
const NetworkdUnitDir = "/etc/systemd/network"

// NetworkdUnit is the service Ostiole hands networking to; NetworkdSocket
// activates it on demand.
const (
	NetworkdUnit   = "systemd-networkd.service"
	NetworkdSocket = "systemd-networkd.socket"
)

// Systemctl runs systemctl; swapped for a fake in tests.
type Systemctl interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// ExecSystemctl runs the real systemctl.
type ExecSystemctl struct{}

// Run implements Systemctl.
//
// SYSTEMCTL_SKIP_SYSV stops systemctl syncing a unit that also has an
// init.d script with update-rc.d or chkconfig. That sync runs in the
// caller's own mount namespace, and the daemon's is read-only under
// ProtectSystem=strict, so disabling ufw on Ubuntu failed with
// "update-rc.d: error: Read-only file system" before anything was
// disabled at all. The rc symlinks it would have removed do nothing on a
// systemd router: a native unit exists for every one of them, and the
// ones Ostiole touches are masked as well as disabled.
func (ExecSystemctl) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Env = append(os.Environ(), "SYSTEMCTL_SKIP_SYSV=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Options tunes the installation.
type Options struct {
	// Source is the executable to install; empty means the running one.
	Source string
	// Listen is the daemon's listen address, e.g. ":9443". Empty keeps
	// what the installed unit has, else DefaultListen.
	Listen string
	// Run executes helper commands (timedatectl); nil means real ones.
	Run Runner
	// SysctlFile is where router sysctls are persisted; empty means the
	// default, "-" skips it (tests).
	SysctlFile string
	// Timezone is the zone the router is set to; empty means UTC, "-" leaves
	// the clock alone (tests).
	Timezone string
	// BluetoothFile is where the modprobe drop-in that blocks Bluetooth is
	// written; empty means the default, "-" skips it (tests).
	BluetoothFile string
}

// Report describes what Install did.
type Report struct {
	Binary string
	Units  []string
	// Sysctl is the persisted sysctl file, or "".
	Sysctl string
	// Timezone is the zone the clock was set to, or "".
	Timezone string
	// Bluetooth is the drop-in that blocks it, or "".
	Bluetooth string
}

// WriteUnits writes the firewall's and the daemon's units and the
// directories the daemon's unit opens, then reloads systemd. It enables,
// starts and restarts nothing: a self-update runs it before restarting
// into the new binary, so the new version starts under its own units.
func WriteUnits(ctx context.Context, sc Systemctl, lay Layout, opts Options) ([]string, error) {
	if opts.Listen == "" {
		opts.Listen = Listen(lay)
	}
	if opts.Listen == "" {
		opts.Listen = DefaultListen
	}
	if err := os.MkdirAll(lay.UnitDir, 0o755); err != nil { //nolint:gosec // systemd unit dir must be world-readable
		return nil, err
	}
	// A directory that appears after the daemon started is not in its
	// mount namespace, so it would stay read-only until a restart. The
	// level caps go in each unit's drop-in directory.
	for _, u := range logging.Units {
		dir := filepath.Join(lay.UnitDir, u.Name+".d")
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // systemd reads these
			return nil, err
		}
	}
	// 0700: a backup holds tunnel keys, and possibly password hashes.
	if lay.BackupDir != "" {
		if err := os.MkdirAll(lay.BackupDir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", lay.BackupDir, err)
		}
		if err := os.Chmod(lay.BackupDir, 0o700); err != nil { //nolint:gosec // a directory needs its search bit
			return nil, err
		}
	}
	var names []string
	for name, content := range Units(lay, opts) {
		if err := atomicfile.Write(filepath.Join(lay.UnitDir, name), []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	if _, err := sc.Run(ctx, "daemon-reload"); err != nil {
		return nil, fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	return names, nil
}

// Install copies the binary, writes the units, and enables them. It never
// touches competing firewalls; that is Takeover's job, once a ruleset is
// in the kernel.
func Install(ctx context.Context, sc Systemctl, lay Layout, opts Options, log *slog.Logger) (*Report, error) {
	src := opts.Source
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		src = exe
	}
	rep := &Report{Binary: lay.Binary()}

	if err := os.MkdirAll(lay.ConfigDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", lay.ConfigDir, err)
	}
	if err := os.MkdirAll(lay.BinDir, 0o755); err != nil { //nolint:gosec // system bin dir must be world-readable
		return nil, err
	}
	// A binary already in a system bin directory — where the install
	// script puts it before running this — is used where it is; anything
	// else (a downloaded copy in $HOME, say) is copied into place so
	// systemd and SELinux accept it.
	if InSystemBinDir(src) {
		lay.BinDir = filepath.Dir(src)
		rep.Binary = lay.Binary()
		log.Info("using the binary where it is", "path", src)
	} else if same, err := sameFile(src, lay.Binary()); err != nil {
		return nil, err
	} else if !same {
		if err := copyFile(src, lay.Binary(), 0o755); err != nil {
			return nil, fmt.Errorf("install binary: %w", err)
		}
		log.Info("installed binary", "path", lay.Binary())
	}

	run := opts.Run
	if run == nil {
		run = ExecRunner{}
	}
	units, err := WriteUnits(ctx, sc, lay, opts)
	if err != nil {
		return nil, err
	}
	rep.Units = units
	if opts.SysctlFile != "-" {
		if err := sysctl.Persist(opts.SysctlFile); err != nil {
			log.Warn("could not persist router sysctls", "err", err)
		} else {
			rep.Sysctl = opts.SysctlFile
			if rep.Sysctl == "" {
				rep.Sysctl = sysctl.ConfFile
			}
		}
		// Take effect now as well, not only after the next boot or apply.
		// Only the constants: an install has no configuration yet, so the
		// connection ceiling is whatever the kernel sized for itself until
		// the first apply says otherwise.
		if err := (sysctl.Proc{}).Apply(sysctl.Settings{}); err != nil {
			log.Warn("could not apply router sysctls now", "err", err)
		}
	}
	// A router has no use for Bluetooth, and a wifi card usually carries a
	// controller for it on the same chip.
	if opts.BluetoothFile != "-" {
		if err := BlockBluetooth(ctx, run, opts.BluetoothFile); err != nil {
			log.Warn("could not block Bluetooth", "err", err)
		} else {
			rep.Bluetooth = opts.BluetoothFile
			if rep.Bluetooth == "" {
				rep.Bluetooth = BluetoothConfFile
			}
			log.Info("Bluetooth blocked", "file", rep.Bluetooth)
		}
	}
	// A firewall's output is timestamps, and they are read next to other
	// machines' timestamps, so Ostiole takes the clock to UTC along with
	// everything else it takes over. The configuration can move it later.
	if opts.Timezone != "-" {
		zone := opts.Timezone
		if zone == "" {
			zone = timezone.Default
		}
		if err := (timezone.System{Run: run}).Apply(ctx, zone); err != nil {
			log.Warn("could not set the timezone; the clock reads as it did before", "zone", zone, "err", err)
		} else {
			rep.Timezone = zone
			log.Info("timezone set", "zone", zone)
		}
	}
	if out, err := sc.Run(ctx, "enable", FirewallUnit); err != nil {
		return nil, fmt.Errorf("enable %s: %w: %s", FirewallUnit, err, out)
	}
	if out, err := sc.Run(ctx, "enable", "--now", DaemonUnit); err != nil {
		return nil, fmt.Errorf("enable %s: %w: %s", DaemonUnit, err, out)
	}
	log.Info("services enabled", "units", rep.Units)
	return rep, nil
}

// Units renders the systemd units for the layout and options.
func Units(lay Layout, opts Options) map[string]string {
	bin := lay.Binary()
	cfg := lay.ConfigDir
	// The auto backend drives systemd-networkd once it is running and stays
	// out of the way before the network takeover.
	backend := "auto"
	// The daemon needs to write its own binary directory for self-updates.
	// -/etc/dnsmasq.d takes the DHCP and DNS configuration once dnsmasq is
	// set up, -/etc/unbound the validating resolver's, -/etc/miniupnpd the
	// mapping service's, -/etc/chrony the time service's, and
	// -/etc/resolv.conf is managed by the DNS service. The leading dash means "only if it exists": a router that never
	// sets up dnsmasq or PPPoE still starts.
	// resolv.conf is a file, not a directory, so systemd mounts that one
	// file read-write and leaves /etc around it read-only; it can be
	// rewritten but never replaced, which services.writeMode handles.
	// -/etc/ssh/sshd_config.d takes the drop-in that turns password logins
	// off, -/etc/cloud/cloud.cfg.d the pin that stops cloud-init turning
	// them back on, and -/etc/systemd/journald.conf.d the journal's
	// ceiling; all three are written from the page as well as the console.
	// Each unit's own .d directory takes the level cap, written from the
	// page too, and the backup directory takes what backup crons write.
	rw := cfg + " " + NetworkdUnitDir + " " + lay.BinDir +
		" -/etc/dnsmasq.d -/etc/unbound -/etc/resolv.conf -/etc/ppp -/etc/miniupnpd -/etc/chrony" +
		" -/etc/ssh/sshd_config.d -/etc/cloud/cloud.cfg.d -/etc/systemd/journald.conf.d" +
		" " + strings.Join(LogDropInDirs(), " ")
	if lay.BackupDir != "" {
		rw += " -" + lay.BackupDir
	}
	// The firewall unit runs outside the default dependencies, the way
	// Debian's nftables.service does. With them it would wait for
	// sysinit.target, and cloud-init's network stage orders itself before
	// sysinit.target and then waits, from inside, for
	// systemd-networkd-wait-online, which waits for networkd, which waits
	// for network-pre.target, which waits for this unit: a cycle systemd
	// cannot see because one edge is a runtime "systemctl start" with no
	// timeout. It hung an Ubuntu 26.04 router at boot with nothing
	// listening. What the unit actually needs is the root filesystem.
	// It runs whether or not a ruleset is saved: with none, `load` puts
	// the fallback in, and a router never boots without a firewall.
	firewall := fmt.Sprintf(`[Unit]
Description=Ostiole firewall ruleset (loaded before networking)
Documentation=https://github.com/rforced/ostiole
DefaultDependencies=no
RequiresMountsFor=%s
After=local-fs.target systemd-sysctl.service
Wants=network-pre.target
Before=network-pre.target shutdown.target
Conflicts=shutdown.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s --config-dir %s load
ExecReload=%s --config-dir %s load

[Install]
WantedBy=multi-user.target
`, cfg, bin, cfg, bin, cfg)

	daemon := fmt.Sprintf(`[Unit]
Description=Ostiole firewall management UI and API
Documentation=https://github.com/rforced/ostiole
After=network.target %s
Wants=%s

[Service]
Type=simple
ExecStart=%s --config-dir %s --network-backend %s serve --tls --listen %s
Restart=on-failure
RestartSec=2

# Runs as root because it programs nftables and networkd; limit everything else.
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=%s
# The log files, written only while System, General says so.
LogsDirectory=ostiole
LogsDirectoryMode=0700
PrivateTmp=yes
ProtectControlGroups=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
NoNewPrivileges=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`, FirewallUnit, FirewallUnit, bin, cfg, backend, opts.Listen, rw)

	return map[string]string{FirewallUnit: firewall, DaemonUnit: daemon}
}

// Service is the state of a systemd unit that competes with Ostiole.
type Service struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // firewall or network
	Active  string `json:"active"`
	Enabled string `json:"enabled"`
}

// Conflicts reports whether the service is running or would start at boot.
func (s Service) Conflicts() bool {
	return s.Active == "active" || s.Active == "activating" || s.Enabled == "enabled"
}

// Known competitors. Network managers are reported only; Ostiole does not
// disable them until its own network backend is in place on the host.
var (
	FirewallServices = []string{"firewalld", "ufw", "nftables", "iptables", "ip6tables", "netfilter-persistent", "shorewall", "shorewall6"}
	NetworkServices  = []string{"NetworkManager", "netplan", "dhcpcd", "connman", "ifupdown"}
)

// CompetitorUnits are the units Competitors asks about.
func CompetitorUnits() []string {
	var out []string
	for _, name := range slices.Concat(FirewallServices, NetworkServices) {
		out = append(out, UnitName(name))
	}
	return out
}

// Competitors lists installed competing services and their state.
func Competitors(ctx context.Context, sc Systemctl) ([]Service, error) {
	return CompetitorsIn(ReadUnits(ctx, sc, CompetitorUnits()...)), nil
}

// CompetitorsIn lists the competing services in states ReadUnits was asked
// for along with other units.
func CompetitorsIn(u UnitStates) []Service {
	var out []Service
	for _, group := range []struct {
		kind  string
		names []string
	}{{"firewall", FirewallServices}, {"network", NetworkServices}} {
		for _, name := range group.names {
			unit := UnitName(name)
			if enabled, ok := u.File(unit); ok {
				out = append(out, Service{Name: name, Kind: group.kind, Active: u.Active(unit), Enabled: enabled})
			}
		}
	}
	return out
}

// UnitStates is what systemd says about a set of units.
type UnitStates struct {
	read   bool
	files  map[string]string
	active map[string]string
}

// ReadUnits asks systemctl about units in two calls however many there
// are: a call per unit cost the dashboard about a quarter of a second on a
// router. is-enabled takes many units too, but systemd 252 stops at the
// first it has no file for, where list-unit-files leaves it out. is-active
// refuses the whole call over a template (name@.service), so a template
// is only looked for on disk.
func ReadUnits(ctx context.Context, sc Systemctl, units ...string) UnitStates {
	u := UnitStates{files: map[string]string{}, active: map[string]string{}}
	var names, running []string
	for _, n := range units {
		if n == "" || slices.Contains(names, n) {
			continue
		}
		names = append(names, n)
		if !strings.Contains(n, "@.") {
			running = append(running, n)
		}
	}
	if len(names) == 0 {
		return u
	}
	out, err := sc.Run(ctx, append([]string{"list-unit-files", "--no-legend", "--plain"}, names...)...)
	for line := range strings.SplitSeq(out, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && slices.Contains(names, f[0]) {
			u.files[f[0]] = f[1]
		}
	}
	// It exits 1 with nothing to say when none of the units has a file.
	u.read = err == nil || out == "" || len(u.files) > 0
	if len(running) == 0 {
		return u
	}
	out, _ = sc.Run(ctx, append([]string{"is-active"}, running...)...)
	states := strings.Split(strings.TrimSpace(out), "\n")
	if len(states) == len(running) && !slices.ContainsFunc(states, notAState) {
		for i, n := range running {
			u.active[n] = states[i]
		}
		return u
	}
	// Something besides the answers came back: ask each unit on its own.
	for _, n := range running {
		s, _ := sc.Run(ctx, "is-active", n)
		u.active[n] = firstLine(s)
	}
	return u
}

// notAState reports whether a line of is-active's answer is anything but
// a state such as active, inactive or deactivating.
func notAState(s string) bool {
	return s == "" || strings.ContainsFunc(s, func(r rune) bool { return (r < 'a' || r > 'z') && r != '-' })
}

// Read reports whether systemctl answered. When it did not, a unit with no
// file may still be there.
func (u UnitStates) Read() bool { return u.read }

// File is the state of the unit's file (enabled, disabled, static,
// masked…), and false when it has none.
func (u UnitStates) File(unit string) (string, bool) {
	s, ok := u.files[unit]
	return s, ok
}

// Active is what is-active said about the unit, "" when it was not asked.
func (u UnitStates) Active(unit string) string { return u.active[unit] }

// Running reports whether the unit runs, a reload in progress included.
func (u UnitStates) Running(unit string) bool {
	switch u.active[unit] {
	case "active", "reloading":
		return true
	}
	return false
}

// Takeover stops, masks, and disables the named units. A name with no
// suffix is a service.
//
// The mask comes first, and with --now, because it is the half that
// matters and the half that always works: stopping and masking go
// through systemd itself, so they succeed from inside the daemon's
// sandbox and leave a unit that cannot start whatever else happens.
// Disabling only tidies the wants links, and it is the step that can fail
// on a unit with an init script (see ExecSystemctl), so a failure there
// is logged rather than returned once the unit is masked.
func Takeover(ctx context.Context, sc Systemctl, names []string, log *slog.Logger) error {
	var errs []error
	for _, name := range names {
		unit := UnitName(name)
		if out, err := sc.Run(ctx, "mask", "--now", unit); err != nil {
			errs = append(errs, fmt.Errorf("mask %s: %w: %s", unit, err, out))
			continue
		}
		if out, err := sc.Run(ctx, "disable", unit); err != nil {
			log.Warn("unit is masked and stopped but could not be disabled", "unit", unit, "err", err, "out", out)
		}
		log.Info("service masked and stopped", "unit", unit)
	}
	return errors.Join(errs...)
}

// UnitName is name as a systemd unit: a bare name is a service, and a
// name with a suffix (snapd.socket, apt-daily.timer) is left alone.
func UnitName(name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	return name + ".service"
}

// Uninstall stops and removes the units. With purge it also removes the
// configuration directory, the log files and the binary; the backups
// stay. It does not restore competitors.
func Uninstall(ctx context.Context, sc Systemctl, lay Layout, purge bool, log *slog.Logger) error {
	for _, unit := range []string{DaemonUnit, FirewallUnit} {
		_, _ = sc.Run(ctx, "disable", "--now", unit)
		path := filepath.Join(lay.UnitDir, unit)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := sc.Run(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	log.Info("units removed")
	if owned, _ := filepath.Glob(filepath.Join(NetworkdUnitDir, network.NetworkdPrefix+"*")); len(owned) > 0 {
		for _, f := range owned {
			_ = os.Remove(f)
		}
		log.Info("removed Ostiole networkd units", "count", len(owned))
	}
	if purge {
		if err := os.RemoveAll(lay.ConfigDir); err != nil {
			return err
		}
		if lay.LogDir != "" {
			if err := os.RemoveAll(lay.LogDir); err != nil {
				return err
			}
		}
		if err := os.Remove(lay.Binary()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		log.Info("configuration, log files and binary removed")
	}
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

func sameFile(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(ia, ib), nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	return atomicfile.Write(dst, data, mode)
}

// PackageManager identifies the host's package manager, or "".
func PackageManager() string {
	for _, pm := range []string{"dnf", "apt-get", "pacman"} {
		if _, err := exec.LookPath(pm); err == nil {
			return pm
		}
	}
	return ""
}

// Runner runs an arbitrary command; swapped for a fake in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// HasNetworkd reports whether the systemd-networkd unit exists.
func HasNetworkd(ctx context.Context, sc Systemctl) bool {
	_, err := sc.Run(ctx, "cat", NetworkdUnit)
	return err == nil
}

// NetworkManagers are the units the network takeover disables.
// cockpit.socket is not a network manager, but it answers on the same
// router and offers to reconfigure the network from a browser of its own.
var NetworkManagers = []string{"NetworkManager", "NetworkManager-wait-online", "netplan", "dhcpcd", "connman", "ifupdown", "cockpit.socket"}

// NetworkTakeoverTargets are the managers on this router worth retiring:
// the ones that are running or would start at boot. A manager that is
// installed and disabled is left alone, so a revert puts back what was
// actually there.
func NetworkTakeoverTargets(ctx context.Context, sc Systemctl) []string {
	var units []string
	for _, name := range NetworkManagers {
		units = append(units, UnitName(name))
	}
	u := ReadUnits(ctx, sc, units...)
	var out []string
	for _, name := range NetworkManagers {
		unit := UnitName(name)
		if enabled, ok := u.File(unit); ok && (Service{Active: u.Active(unit), Enabled: enabled}).Conflicts() {
			out = append(out, name)
		}
	}
	return out
}

// NetworkTakeover hands networking to systemd-networkd: stops, disables,
// and masks the listed managers, then enables and starts networkd. The
// caller must have written the networkd units first.
func NetworkTakeover(ctx context.Context, sc Systemctl, managers []string, log *slog.Logger) error {
	for _, name := range managers {
		unit := UnitName(name)
		if out, err := sc.Run(ctx, "mask", "--now", unit); err != nil {
			return fmt.Errorf("mask %s: %w: %s", unit, err, out)
		}
		if out, err := sc.Run(ctx, "disable", unit); err != nil {
			log.Warn("network manager is masked and stopped but could not be disabled", "unit", unit, "err", err, "out", out)
		}
		log.Info("network manager masked and stopped", "unit", unit)
	}
	if out, err := sc.Run(ctx, "enable", "--now", NetworkdSocket, NetworkdUnit); err != nil {
		return fmt.Errorf("enable %s: %w: %s", NetworkdUnit, err, out)
	}
	log.Info("systemd-networkd enabled and started")
	return nil
}

func tail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 400 {
		s = "…" + s[len(s)-400:]
	}
	return s
}

// RevertTimerUnit is the transient timer that undoes a network takeover
// unless it is confirmed in time.
const RevertTimerUnit = "ostiole-network-revert"

// TakeoverRecord remembers what the network takeover replaced so it can
// be undone. It lives in the config directory.
type TakeoverRecord struct {
	Managers []string  `json:"managers"`
	At       time.Time `json:"at"`
}

// TakeoverRecordFile is the record's name inside the config directory.
const TakeoverRecordFile = "network-takeover.json"

// SaveTakeoverRecord writes the record.
func SaveTakeoverRecord(dir string, rec TakeoverRecord) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, TakeoverRecordFile), append(raw, '\n'), 0o600)
}

// LoadTakeoverRecord reads the record, or returns nil when none exists.
func LoadTakeoverRecord(dir string) (*TakeoverRecord, error) {
	raw, err := os.ReadFile(filepath.Join(dir, TakeoverRecordFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec TakeoverRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// RouteSweeper reads the kernel's default routes and takes away the ones
// a stopped systemd-networkd left behind. network.KernelRoutes is the
// implementation; tests pass one that touches no routing table.
type RouteSweeper interface {
	Defaults() ([]network.DefaultRoute, error)
	SweepStale(ctx context.Context, before []network.DefaultRoute, wait time.Duration) ([]network.DefaultRoute, error)
}

// Restorable is the subset of managers that still have a unit on this
// router. The install script removes the old manager once the handover is
// confirmed, so a revert that runs after that would stop networkd and bring
// back nothing: a router with no network at all.
func Restorable(ctx context.Context, sc Systemctl, managers []string) []string {
	var out []string
	for _, name := range managers {
		if _, err := sc.Run(ctx, "cat", UnitName(name)); err == nil {
			out = append(out, name)
		}
	}
	return out
}

// NetworkRevert undoes NetworkTakeover: stops networkd and brings the
// previous managers back. wait-online style units are only re-enabled,
// never started, because starting them blocks until the network is up.
//
// Stopping networkd does not always take its routes with it, so the
// routes it had are noted first and swept once the old manager has
// installed its own. A sweep that finds no replacement removes nothing.
func NetworkRevert(ctx context.Context, sc Systemctl, routes RouteSweeper, managers []string, log *slog.Logger) error {
	managers = Restorable(ctx, sc, managers)
	if len(managers) == 0 {
		log.Warn("no previous network manager is left on this router; systemd-networkd stays in charge")
		return nil
	}
	// Taken while networkd still owns the addressing, so every default
	// route here is one it put in the kernel.
	before, err := routes.Defaults()
	if err != nil {
		log.Warn("could not read the default routes before reverting", "err", err)
	}
	if err := RestoreCloudInitNetwork(); err != nil {
		log.Warn("could not remove the cloud-init drop-in", "err", err)
	}
	// The socket unit would re-activate networkd on the next client
	// connection, so it has to go down with the service.
	if out, err := sc.Run(ctx, "disable", "--now", NetworkdSocket, NetworkdUnit); err != nil {
		log.Warn("stopping systemd-networkd failed", "err", err, "out", out)
	}
	var errs []error
	for _, name := range managers {
		unit := UnitName(name)
		if out, err := sc.Run(ctx, "unmask", unit); err != nil {
			errs = append(errs, fmt.Errorf("unmask %s: %w: %s", unit, err, out))
			continue
		}
		args := []string{"enable", "--now", unit}
		if strings.Contains(name, "wait-online") {
			args = []string{"enable", unit}
		}
		if out, err := sc.Run(ctx, args...); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, out))
			continue
		}
		log.Info("network manager restored", "unit", unit)
	}
	removed, err := routes.SweepStale(ctx, before, network.StaleRouteWait)
	if err != nil {
		log.Warn("could not sweep the routes systemd-networkd left behind", "err", err)
	}
	for _, r := range removed {
		log.Info("removed a default route systemd-networkd left behind",
			"interface", r.Interface, "gateway", r.Gateway, "metric", r.Metric, "protocol", r.Protocol)
	}
	return errors.Join(errs...)
}

// ScheduleNetworkRevert arms a transient systemd timer that runs
// `ostiole takeover --network --revert` after window unless cancelled.
func ScheduleNetworkRevert(ctx context.Context, run Runner, binary, configDir string, window time.Duration) error {
	_, _ = run.Run(ctx, "systemctl", "stop", RevertTimerUnit+".timer")
	// Timers default to one-minute accuracy; the admin is counting seconds.
	out, err := run.Run(ctx, "systemd-run", "--quiet", "--unit="+RevertTimerUnit,
		"--on-active="+fmt.Sprint(int(window.Seconds())), "--timer-property=AccuracySec=1s",
		binary, "--config-dir", configDir, "takeover", "--network", "--revert", "--in-unit")
	if err != nil {
		return fmt.Errorf("arm revert timer: %w: %s", err, tail(out))
	}
	return nil
}

// CancelNetworkRevert disarms the timer. It reports whether one was armed.
func CancelNetworkRevert(ctx context.Context, run Runner) bool {
	out, err := run.Run(ctx, "systemctl", "is-active", RevertTimerUnit+".timer")
	armed := err == nil && strings.TrimSpace(string(out)) == "active"
	_, _ = run.Run(ctx, "systemctl", "stop", RevertTimerUnit+".timer")
	return armed
}

// CloudInitDropIn stops cloud-init from rendering network configuration
// on later boots, which would otherwise compete with Ostiole's units.
const CloudInitDropIn = "/etc/cloud/cloud.cfg.d/99-ostiole-network.cfg"

// DisableCloudInitNetwork writes the drop-in when cloud-init is present.
// It reports whether it did.
func DisableCloudInitNetwork(log *slog.Logger) (bool, error) {
	if _, err := os.Stat(filepath.Dir(CloudInitDropIn)); err != nil {
		return false, nil
	}
	content := "# Written by ostiole: networking is managed through systemd-networkd by Ostiole.\nnetwork: {config: disabled}\n"
	if err := atomicfile.Write(CloudInitDropIn, []byte(content), 0o644); err != nil {
		return false, err
	}
	log.Info("disabled cloud-init network rendering", "file", CloudInitDropIn)
	return true, nil
}

// RestoreCloudInitNetwork removes the drop-in.
func RestoreCloudInitNetwork() error {
	err := os.Remove(CloudInitDropIn)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Detached runs argv as a transient systemd unit and waits for it. The
// unit outlives the caller, so an operation that drops the caller's own
// SSH session (a network manager switch) still runs to completion. Output
// goes to the journal under the unit's name.
func Detached(ctx context.Context, run Runner, unit string, argv ...string) error {
	_, _ = run.Run(ctx, "systemctl", "reset-failed", unit+".service")
	args := append([]string{"--unit=" + unit, "--wait", "--collect", "--quiet", "--"}, argv...)
	out, err := run.Run(ctx, "systemd-run", args...)
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s (see journalctl -u %s)", unit, err, tail(out), unit)
	}
	return nil
}

// SystemBinDirs are the places the install script puts the binary.
var SystemBinDirs = []string{"/usr/local/bin", "/usr/bin", "/usr/local/sbin", "/usr/sbin"}

// InSystemBinDir reports whether path lives in a system bin directory.
func InSystemBinDir(path string) bool {
	dir := filepath.Dir(filepath.Clean(path))
	return slices.Contains(SystemBinDirs, dir)
}

// ServiceBinary returns the executable that units, timers, and the updater
// should use: the installed copy when one exists (systemd and SELinux will
// not run a binary sitting in a home directory), else the running one.
func ServiceBinary(lay Layout) (string, error) {
	candidates := []string{lay.Binary()}
	for _, d := range SystemBinDirs {
		candidates = append(candidates, filepath.Join(d, "ostiole"))
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.Mode().IsRegular() {
			return c, nil
		}
	}
	return os.Executable()
}
