// Package sshd decides whether this router's sshd accepts a password.
//
// The setting is configuration — `system.management.sshPasswords` — and
// every apply writes it, so a router that is rebuilt from a backup lets
// people in the same way it did before.
//
// The reading is `sshd -T`, which prints the effective value of every
// keyword after every Include has been followed, because that is the only
// answer that survives what cloud images do to /etc/ssh: Vultr's
// cloud-init drops a 50-cloud-init.conf into sshd_config.d that turns
// password logins on, whatever the main file says. sshd takes the first
// value it reads for a keyword, and the Include sits at the top of the
// main file, so a drop-in that sorts before that one wins over both. That
// is what "00-ostiole.conf" is.
package sshd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Applier turns password logins over SSH on or off.
type Applier interface {
	Apply(ctx context.Context, allowPasswords bool) error
}

// Runner runs a command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// System is the router this process runs on.
type System struct {
	// Run executes sshd and systemctl; nil means the real ones.
	Run Runner
	// FS is the filesystem root, overridden in tests.
	FS string
}

// Where sshd keeps things. Every path is joined onto FS.
const (
	sshdConfig    = "/etc/ssh/sshd_config"
	sshdConfigDir = "/etc/ssh/sshd_config.d"
	// DropIn is Ostiole's drop-in. The 00 is the whole point: sshd takes
	// the first value it reads, and Include globs are read in name order.
	DropIn = sshdConfigDir + "/00-ostiole.conf"
	// sshdInclude is the line a main file needs for the drop-in to be read
	// at all. Every current distribution ships it; one that does not gets
	// it added.
	sshdInclude = "Include /etc/ssh/sshd_config.d/*.conf"
	// CloudInitDropIn stops cloud-init turning passwords back on: its
	// ssh_pwauth setting is what wrote the 50-cloud-init.conf in the first
	// place, and a re-run would write it again.
	CloudInitDropIn = "/etc/cloud/cloud.cfg.d/99-ostiole-ssh.cfg"
)

// dropInContent is what requiring keys writes.
const dropInContent = `# Written by ostiole. Password logins over SSH are off on this router;
# keys only. Allow passwords again from the General page to undo it.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
MaxAuthTries 3
LoginGraceTime 30
`

const cloudInitContent = "# Written by ostiole: SSH password logins are managed by Ostiole.\nssh_pwauth: false\n"

func (s System) path(p string) string {
	if s.FS == "" {
		return p
	}
	return filepath.Join(s.FS, p)
}

func (s System) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run == nil {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	return s.Run.Run(ctx, name, args...)
}

// binary is where sshd is, or "". Package managers put it in sbin, which
// is not always on a service's PATH. A test that names a filesystem root
// gets only what is under it.
func (s System) binary() string {
	if s.FS == "" {
		if p, err := exec.LookPath("sshd"); err == nil {
			return p
		}
	}
	for _, dir := range []string{"/usr/sbin", "/sbin", "/usr/local/sbin"} {
		p := s.path(filepath.Join(dir, "sshd"))
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// Apply writes or removes Ostiole's drop-in and the cloud-init pin beside
// it, checks the result with sshd itself, and reloads the running unit. A
// configuration sshd rejects is put back. Nothing checks whether anybody
// has a key: that is the operator's call, and the page says so.
//
// A router with no sshd has nothing to apply and is not an error.
func (s System) Apply(ctx context.Context, allowPasswords bool) error {
	bin := s.binary()
	if bin == "" {
		return nil
	}
	changed, err := s.write(allowPasswords)
	if err != nil {
		return err
	}
	if changed {
		if out, err := s.run(ctx, bin, "-t"); err != nil {
			// Whatever was there before is better than a configuration sshd
			// will not start with.
			_, _ = s.write(!allowPasswords)
			return fmt.Errorf("sshd rejected the configuration, so it was put back: %s", strings.TrimSpace(string(out)))
		}
		if unit := s.unit(ctx); unit != "" {
			if out, err := s.run(ctx, "systemctl", "reload", unit); err != nil {
				return fmt.Errorf("reload %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
			}
		}
	}
	// The drop-in wins only while nothing sshd reads first says otherwise,
	// and an upgrade or cloud-init can change that between applies, so
	// every apply asks sshd what it ended up with.
	if !allowPasswords {
		if passwords, readable := s.State(ctx); readable && passwords {
			return fmt.Errorf("sshd still accepts passwords: something it reads before %s turns them on, "+
				"in %s above its Include or in a drop-in that sorts first", filepath.Base(DropIn), sshdConfig)
		}
	}
	return nil
}

// write puts the drop-in and the cloud-init pin in place, or takes them
// away. It reports whether anything changed, so an apply that agrees with
// the router does not reload sshd every time.
func (s System) write(allow bool) (bool, error) {
	dropIn := s.path(DropIn)
	pin := s.path(CloudInitDropIn)
	if allow {
		changed := false
		for _, p := range []string{dropIn, pin} {
			err := os.Remove(p)
			switch {
			case err == nil:
				changed = true
			case !errors.Is(err, fs.ErrNotExist):
				return changed, err
			}
		}
		return changed, nil
	}
	// A package upgrade can put back a main file without the Include.
	added, err := s.ensureInclude()
	if err != nil {
		return false, err
	}
	if same(dropIn, dropInContent) {
		return added, nil
	}
	if err := os.MkdirAll(filepath.Dir(dropIn), 0o755); err != nil { //nolint:gosec // sshd's own directory
		return added, err
	}
	if err := writeAtomic(dropIn, dropInContent, 0o600); err != nil {
		return added, err
	}
	if _, err := os.Stat(filepath.Dir(pin)); err == nil {
		if err := writeAtomic(pin, cloudInitContent, 0o644); err != nil {
			return true, err
		}
	}
	return true, nil
}

// ensureInclude adds the Include line to a main file that lacks it, at the
// top, which is the only place it means "the drop-ins win". It reports
// whether it added one.
func (s System) ensureInclude() (bool, error) {
	path := s.path(sshdConfig)
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "Include") &&
			strings.Contains(fields[1], "sshd_config.d") {
			return false, nil
		}
	}
	err = writeAtomic(path, "# Added by ostiole so the drop-ins below are read.\n"+sshdInclude+"\n"+string(raw), 0o600)
	return err == nil, err
}

// unit is the sshd unit that is running: ssh.service on Debian and
// Ubuntu, sshd.service everywhere else. A socket-activated sshd has no
// running service between connections and needs no reload, since each
// connection reads the configuration afresh.
func (s System) unit(ctx context.Context) string {
	for _, unit := range []string{"ssh.service", "sshd.service"} {
		out, err := s.run(ctx, "systemctl", "is-active", unit)
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			return unit
		}
	}
	return ""
}

// State reports whether sshd accepts a password right now, and whether it
// could be read at all. Unreadable — no sshd, or not root — answers true,
// which is the value whose apply changes nothing.
func (s System) State(ctx context.Context) (passwords, readable bool) {
	bin := s.binary()
	if bin == "" {
		return true, false
	}
	out, err := s.run(ctx, bin, "-T")
	if err != nil {
		return true, false
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && strings.EqualFold(key, "passwordauthentication") {
			return strings.TrimSpace(value) == "yes", true
		}
	}
	return true, false
}

// same reports whether the file already holds exactly this content.
func same(path, content string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && string(raw) == content
}

// writeAtomic writes a file in one rename, so sshd never reads half of one.
func writeAtomic(path, content string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
