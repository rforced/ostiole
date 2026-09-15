// Package install puts Ostiole on a systemd host: binary, units, config
// directory, competitor detection, and the takeover that makes Ostiole the
// only firewall.
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
	"strings"
	"time"
)

// Layout says where things go. Tests point it at temp directories.
type Layout struct {
	BinDir    string
	UnitDir   string
	ConfigDir string
}

// DefaultLayout is the production layout.
func DefaultLayout() Layout {
	return Layout{BinDir: "/usr/local/bin", UnitDir: "/etc/systemd/system", ConfigDir: "/etc/ostiole"}
}

// Binary is the installed executable path.
func (l Layout) Binary() string { return filepath.Join(l.BinDir, "ostiole") }

// Unit names.
const (
	FirewallUnit = "ostiole-firewall.service"
	DaemonUnit   = "ostiole.service"
)

// NetworkdUnitDir is where Ostiole writes its networkd units.
const NetworkdUnitDir = "/etc/systemd/network"

// NetworkdUnit is the service Ostiole hands networking to.
const NetworkdUnit = "systemd-networkd.service"

// Systemctl runs systemctl; swapped for a fake in tests.
type Systemctl interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// ExecSystemctl runs the real systemctl.
type ExecSystemctl struct{}

// Run implements Systemctl.
func (ExecSystemctl) Run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Options tunes the installation.
type Options struct {
	// Source is the executable to install; empty means the running one.
	Source string
	// Listen is the daemon's listen address, e.g. ":443".
	Listen string
}

// Report describes what Install did and found.
type Report struct {
	Binary      string
	Units       []string
	Competitors []Service
}

// Install copies the binary, writes the units, and enables them. It never
// touches competing firewalls; that is Takeover's job, after the admin has
// confirmed a working ruleset.
func Install(ctx context.Context, sc Systemctl, lay Layout, opts Options, log *slog.Logger) (*Report, error) {
	if opts.Listen == "" {
		opts.Listen = ":443"
	}
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
	if same, err := sameFile(src, lay.Binary()); err != nil {
		return nil, err
	} else if !same {
		if err := copyFile(src, lay.Binary(), 0o755); err != nil {
			return nil, fmt.Errorf("install binary: %w", err)
		}
		log.Info("installed binary", "path", lay.Binary())
	}

	if err := os.MkdirAll(lay.UnitDir, 0o755); err != nil { //nolint:gosec // systemd unit dir must be world-readable
		return nil, err
	}
	for name, content := range Units(lay, opts) {
		if err := writeFile(filepath.Join(lay.UnitDir, name), content, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
		rep.Units = append(rep.Units, name)
	}
	if _, err := sc.Run(ctx, "daemon-reload"); err != nil {
		return nil, fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if out, err := sc.Run(ctx, "enable", FirewallUnit); err != nil {
		return nil, fmt.Errorf("enable %s: %w: %s", FirewallUnit, err, out)
	}
	if out, err := sc.Run(ctx, "enable", "--now", DaemonUnit); err != nil {
		return nil, fmt.Errorf("enable %s: %w: %s", DaemonUnit, err, out)
	}
	log.Info("services enabled", "units", rep.Units)

	comp, err := Competitors(ctx, sc)
	if err != nil {
		return rep, err
	}
	rep.Competitors = comp
	return rep, nil
}

// Units renders the systemd units for the layout and options.
func Units(lay Layout, opts Options) map[string]string {
	bin := lay.Binary()
	cfg := lay.ConfigDir
	// The auto backend drives systemd-networkd once it is running and stays
	// out of the way before the network takeover.
	backend := "auto"
	rw := cfg + " " + NetworkdUnitDir
	firewall := fmt.Sprintf(`[Unit]
Description=Ostiole firewall ruleset (loaded before networking)
Documentation=https://github.com/rforced/ostiole
Wants=network-pre.target
Before=network-pre.target shutdown.target
Conflicts=shutdown.target
ConditionPathExists=%s/ruleset.nft

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
	NetworkServices  = []string{"NetworkManager", "netplan", "dhcpcd", "connman", "wicked", "ifupdown"}
)

// Competitors lists installed competing services and their state.
func Competitors(ctx context.Context, sc Systemctl) ([]Service, error) {
	var out []Service
	for _, group := range []struct {
		kind  string
		names []string
	}{{"firewall", FirewallServices}, {"network", NetworkServices}} {
		for _, name := range group.names {
			unit := name + ".service"
			enabled, _ := sc.Run(ctx, "is-enabled", unit)
			enabled = firstLine(enabled)
			if enabled == "" || enabled == "not-found" || strings.Contains(enabled, "No such file") || strings.Contains(enabled, "not found") {
				continue
			}
			active, _ := sc.Run(ctx, "is-active", unit)
			out = append(out, Service{Name: name, Kind: group.kind, Active: firstLine(active), Enabled: enabled})
		}
	}
	return out, nil
}

// Takeover stops, disables, and masks the named services.
func Takeover(ctx context.Context, sc Systemctl, names []string, log *slog.Logger) error {
	var errs []error
	for _, name := range names {
		unit := name + ".service"
		if out, err := sc.Run(ctx, "disable", "--now", unit); err != nil {
			errs = append(errs, fmt.Errorf("disable %s: %w: %s", unit, err, out))
			continue
		}
		if out, err := sc.Run(ctx, "mask", unit); err != nil {
			errs = append(errs, fmt.Errorf("mask %s: %w: %s", unit, err, out))
			continue
		}
		log.Info("service disabled and masked", "unit", unit)
	}
	return errors.Join(errs...)
}

// Uninstall stops and removes the units. With purge it also removes the
// configuration directory and the binary. It does not restore competitors.
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
	if owned, _ := filepath.Glob(filepath.Join(NetworkdUnitDir, "10-ostiole-*")); len(owned) > 0 {
		for _, f := range owned {
			_ = os.Remove(f)
		}
		log.Warn("removed Ostiole networkd units; networking now depends on whatever else manages it", "count", len(owned))
	}
	if purge {
		if err := os.RemoveAll(lay.ConfigDir); err != nil {
			return err
		}
		if err := os.Remove(lay.Binary()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		log.Info("configuration and binary removed")
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
	return writeFile(dst, string(data), mode)
}

// writeFile writes atomically so a running service never sees a torn file.
func writeFile(path, content string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// PackageManager identifies the host's package manager, or "".
func PackageManager() string {
	for _, pm := range []string{"dnf", "apt-get", "pacman", "zypper", "apk"} {
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

// EnsureNetworkd installs systemd-networkd when the unit is missing. Only
// RHEL-family hosts ship it separately (EPEL); elsewhere it comes with
// systemd, so a missing unit is reported rather than fixed.
func EnsureNetworkd(ctx context.Context, sc Systemctl, run Runner, pm string, log *slog.Logger) error {
	if HasNetworkd(ctx, sc) {
		return nil
	}
	switch pm {
	case "dnf":
		log.Info("installing systemd-networkd with dnf (EPEL)")
		if out, err := run.Run(ctx, "dnf", "-y", "install", "systemd-networkd"); err != nil {
			return fmt.Errorf("dnf install systemd-networkd failed (is EPEL enabled? `dnf install -y epel-release`): %w: %s", err, tail(out))
		}
	default:
		return errors.New("systemd-networkd is not installed and no supported package manager was found; install it and retry")
	}
	if _, err := sc.Run(ctx, "daemon-reload"); err != nil {
		return err
	}
	if !HasNetworkd(ctx, sc) {
		return errors.New("systemd-networkd still missing after installation")
	}
	return nil
}

// NetworkManagers are the services the network takeover disables.
var NetworkManagers = []string{"NetworkManager", "NetworkManager-wait-online", "netplan", "dhcpcd", "connman", "wicked", "ifupdown"}

// NetworkTakeover hands networking to systemd-networkd: stops, disables,
// and masks the listed managers, then enables and starts networkd. The
// caller must have written the networkd units first.
func NetworkTakeover(ctx context.Context, sc Systemctl, managers []string, log *slog.Logger) error {
	for _, name := range managers {
		unit := name + ".service"
		if out, err := sc.Run(ctx, "disable", "--now", unit); err != nil {
			return fmt.Errorf("disable %s: %w: %s", unit, err, out)
		}
		if out, err := sc.Run(ctx, "mask", unit); err != nil {
			return fmt.Errorf("mask %s: %w: %s", unit, err, out)
		}
		log.Info("network manager disabled and masked", "unit", unit)
	}
	if out, err := sc.Run(ctx, "enable", "--now", NetworkdUnit); err != nil {
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
	return writeFile(filepath.Join(dir, TakeoverRecordFile), string(raw)+"\n", 0o600)
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

// NetworkRevert undoes NetworkTakeover: stops networkd and brings the
// previous managers back. wait-online style units are only re-enabled,
// never started, because starting them blocks until the network is up.
func NetworkRevert(ctx context.Context, sc Systemctl, managers []string, log *slog.Logger) error {
	if out, err := sc.Run(ctx, "disable", "--now", NetworkdUnit); err != nil {
		log.Warn("stopping systemd-networkd failed", "err", err, "out", out)
	}
	var errs []error
	for _, name := range managers {
		unit := name + ".service"
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
	return errors.Join(errs...)
}

// ScheduleNetworkRevert arms a transient systemd timer that runs
// `ostiole takeover --network --revert` after window unless cancelled.
func ScheduleNetworkRevert(ctx context.Context, run Runner, binary, configDir string, window time.Duration) error {
	_, _ = run.Run(ctx, "systemctl", "stop", RevertTimerUnit+".timer")
	// Timers default to one-minute accuracy; the admin is counting seconds.
	out, err := run.Run(ctx, "systemd-run", "--quiet", "--unit="+RevertTimerUnit,
		"--on-active="+fmt.Sprint(int(window.Seconds())), "--timer-property=AccuracySec=1s",
		binary, "--config-dir", configDir, "takeover", "--network", "--revert")
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
