package services

import (
	"context"
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
	if err := os.MkdirAll(d.dir(), 0o700); err != nil {
		return err
	}
	unit := UnitContent(bin, filepath.Join(d.dir(), confName))
	if err := os.MkdirAll(unitDir, 0o755); err != nil { //nolint:gosec // systemd unit dir
		return err
	}
	if err := writeFile(filepath.Join(unitDir, Unit), unit, 0o644); err != nil {
		return err
	}
	if _, err := run.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	log.Info("services ready", "unit", Unit, "dnsmasq", bin)
	return nil
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
