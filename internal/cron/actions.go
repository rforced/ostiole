package cron

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/atomicfile"
	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/model"
)

// DefaultCommandTimeout bounds a command cron that names no timeout of
// its own. A command that has not finished in five minutes is stuck.
const DefaultCommandTimeout = 5 * time.Minute

// Actions runs the scheduled work: one method per cron kind. Each
// dependency is optional, so a router without a feed refresher simply cannot
// schedule one, and says so instead of failing obscurely.
type Actions struct {
	// Config reads the configuration a backup is taken of.
	Config func() *model.Config
	// Users supplies accounts when a backup asks for them.
	Users func() []auth.User
	// Refresh fetches the address lists and country ranges.
	Refresh func(ctx context.Context) error
	// RefreshBlocklists fetches the DNS blocklists.
	RefreshBlocklists func(ctx context.Context) error
	// Restart restarts a service unit.
	Restart func(ctx context.Context, unit string) error
	// SystemUpdate checks the distro package manager and installs what
	// the mode allows. The mode and the never-upgrade list are read from
	// the configuration here, so there is one place to look.
	SystemUpdate func(ctx context.Context, mode string, exclude []string) (string, error)
	// SelfUpdate does the same for Ostiole's own releases.
	SelfUpdate func(ctx context.Context, mode, channel string) (string, error)
	// SystemCheck asks the package manager what is waiting and records it,
	// installing none of it.
	SystemCheck func(ctx context.Context) (string, error)
	// SelfCheck does the same for Ostiole's own releases.
	SelfCheck func(ctx context.Context, channel string) (string, error)
	// Remote copies an archive to the bucket the configuration names;
	// nil says this router cannot.
	Remote func(ctx context.Context, r model.RemoteBackup, hostname string, archive *backup.Archive) (string, error)
	// Certificates renews what is due and issues what is missing.
	Certificates func(ctx context.Context) (string, error)
	// Wake sends the magic packet for a machine onto one interface.
	Wake func(iface string, mac net.HardwareAddr) error
	// Version is recorded in the backups this takes.
	Version string
	// Exec runs a command; swapped for a fake in tests.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
}

var _ Executor = (*Actions)(nil)

// Run implements Executor.
func (a *Actions) Run(ctx context.Context, c model.Cron) (string, error) {
	switch c.Kind {
	case model.CronBackup:
		return a.backup(c)
	case model.CronRefreshAliases:
		if a.Refresh == nil {
			return "", errors.New("nothing on this router refreshes aliases")
		}
		return "refreshed", a.Refresh(ctx)
	case model.CronRefreshBlocklists:
		if a.RefreshBlocklists == nil {
			return "", errors.New("nothing on this router refreshes blocklists")
		}
		return "refreshed", a.RefreshBlocklists(ctx)
	case model.CronRestartService:
		return a.restart(ctx, c)
	case model.CronWake:
		return a.wake(c)
	case model.CronCommand:
		return a.command(ctx, c)
	case model.CronSystemUpdate:
		return a.systemUpdate(ctx)
	case model.CronOstioleUpdate:
		return a.selfUpdate(ctx)
	case model.CronSystemUpdateCheck:
		return a.systemCheck(ctx)
	case model.CronOstioleUpdateCheck:
		return a.selfCheck(ctx)
	case model.CronRemoteBackup:
		return a.remoteBackup(ctx)
	case model.CronCertificates:
		if a.Certificates == nil {
			return "", errors.New("nothing on this router issues certificates")
		}
		return a.Certificates(ctx)
	}
	return "", fmt.Errorf("unknown cron kind %q", c.Kind)
}

// systemCheck asks the package manager what is waiting. It runs whatever
// the mode is, and changes nothing, so the page has a fresh answer on a
// router that installs by hand.
func (a *Actions) systemCheck(ctx context.Context) (string, error) {
	if a.SystemCheck == nil {
		return "", errors.New("this router cannot drive a package manager from here")
	}
	return a.SystemCheck(ctx)
}

// selfCheck asks whether a newer Ostiole release has been published on
// the channel the configuration names.
func (a *Actions) selfCheck(ctx context.Context) (string, error) {
	if a.SelfCheck == nil {
		return "", errors.New("this router cannot check for Ostiole releases from here")
	}
	updates, err := a.updates()
	if err != nil {
		return "", err
	}
	return a.SelfCheck(ctx, updates.OstioleChannel())
}

// systemUpdate patches the Linux underneath Ostiole. The mode decides
// whether it installs anything at all; the check happens either way, so
// the page can say what is waiting.
func (a *Actions) systemUpdate(ctx context.Context) (string, error) {
	if a.SystemUpdate == nil {
		return "", errors.New("this router cannot drive a package manager from here")
	}
	updates, err := a.updates()
	if err != nil {
		return "", err
	}
	return a.SystemUpdate(ctx, string(updates.SystemMode()), updates.System.Exclude)
}

// selfUpdate keeps Ostiole itself current.
func (a *Actions) selfUpdate(ctx context.Context) (string, error) {
	if a.SelfUpdate == nil {
		return "", errors.New("this router cannot update Ostiole from here; run `ostiole update`")
	}
	updates, err := a.updates()
	if err != nil {
		return "", err
	}
	return a.SelfUpdate(ctx, string(updates.OstioleMode()), updates.OstioleChannel())
}

// RemoteBackupTimeout bounds a copy to the bucket. A backup is a few
// tens of kilobytes; five minutes covers a slow line and a retry.
const RemoteBackupTimeout = 5 * time.Minute

// remoteBackup copies the configuration to the bucket. The accounts go
// with it, because a disaster-recovery copy nobody can sign into is half
// a backup, and it is always encrypted.
func (a *Actions) remoteBackup(ctx context.Context) (string, error) {
	if a.Config == nil {
		return "", errors.New("no configuration to back up")
	}
	cfg := a.Config()
	if cfg == nil {
		return "", errors.New("nothing is configured yet")
	}
	r := cfg.Backup.Remote
	if !r.Enabled {
		return "", errors.New("remote backup is off")
	}
	if a.Remote == nil {
		return "", errors.New("this router cannot reach a bucket from here")
	}
	opts := backup.Options{
		Ostiole:    a.Version,
		Note:       "scheduled backup " + model.CronIDRemoteBackup,
		Passphrase: r.Passphrase,
	}
	if a.Users != nil {
		opts.Users = a.Users()
	}
	archive, err := backup.Create(cfg, opts)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, RemoteBackupTimeout)
	defer cancel()
	return a.Remote(ctx, r, cfg.System.Hostname, archive)
}

// wake sends the magic packet for the device a wake cron names, read from
// the configuration in force, so a device that was edited is woken where
// it is now.
func (a *Actions) wake(c model.Cron) (string, error) {
	if a.Wake == nil {
		return "", errors.New("this router cannot send a wake from here")
	}
	if a.Config == nil {
		return "", errors.New("nothing is configured yet")
	}
	cfg := a.Config()
	if cfg == nil {
		return "", errors.New("nothing is configured yet")
	}
	d, ok := cfg.WoLDevice(c.Device)
	if !ok {
		return "", fmt.Errorf("no Wake on LAN device has the id %q", c.Device)
	}
	mac, err := cfg.WakeTarget(d.Interface, d.MAC)
	if err != nil {
		return "", err
	}
	if err := a.Wake(d.Interface, mac); err != nil {
		return "", err
	}
	return fmt.Sprintf("sent a wake packet to %s on %s", mac, d.Interface), nil
}

func (a *Actions) updates() (model.Updates, error) {
	if a.Config == nil {
		return model.Updates{}, errors.New("nothing is configured yet")
	}
	cfg := a.Config()
	if cfg == nil {
		return model.Updates{}, errors.New("nothing is configured yet")
	}
	return cfg.Updates, nil
}

// backup writes a configuration backup and prunes the old ones, so a
// directory of them does not grow without limit on a small disk.
func (a *Actions) backup(c model.Cron) (string, error) {
	if a.Config == nil {
		return "", errors.New("no configuration to back up")
	}
	cfg := a.Config()
	if cfg == nil {
		return "", errors.New("nothing is configured yet")
	}
	opts := backup.Options{
		Ostiole:    a.Version,
		Note:       "scheduled backup " + c.ID,
		Passphrase: c.Passphrase,
	}
	if c.WithUsers && a.Users != nil {
		opts.Users = a.Users()
	}
	archive, err := backup.Create(cfg, opts)
	if err != nil {
		return "", err
	}
	raw, err := archive.Bytes()
	if err != nil {
		return "", err
	}
	// 0700 and 0600: a backup holds tunnel keys, and possibly password
	// hashes.
	if err := os.MkdirAll(c.Directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(c.Directory, archive.Filename())
	if err := atomicfile.Write(path, raw, 0o600); err != nil {
		return "", err
	}
	removed, err := prune(c.Directory, keepOf(c))
	if err != nil {
		return fmt.Sprintf("wrote %s (%d bytes)", path, len(raw)), err
	}
	out := fmt.Sprintf("wrote %s (%d bytes)", path, len(raw))
	if removed > 0 {
		out += fmt.Sprintf("; removed %d older backup(s)", removed)
	}
	return out, nil
}

func keepOf(c model.Cron) int {
	if c.Keep <= 0 {
		return model.DefaultBackupsKept
	}
	return c.Keep
}

// prune removes all but the newest keep backups from a directory. Only
// files a backup cron could have written are considered.
func prune(dir string, keep int) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, e := range entries {
		// Both shapes a backup cron writes: plain JSON and the encrypted
		// form, which is the same name with the age suffix on the end.
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".json"+backup.Suffix) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return 0, nil
	}
	// The names carry a timestamp, so sorting them sorts by age.
	sort.Strings(names)
	removed := 0
	for _, name := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (a *Actions) restart(ctx context.Context, c model.Cron) (string, error) {
	if a.Restart == nil {
		return "", errors.New("this router cannot restart services from here")
	}
	if err := a.Restart(ctx, c.Service); err != nil {
		return "", err
	}
	return "restarted " + c.Service, nil
}

// command runs a command without a shell, so there is nothing to quote
// and nothing to inject. It is bounded by a timeout, because a scheduled
// command that hangs is one that never runs again.
func (a *Actions) command(ctx context.Context, c model.Cron) (string, error) {
	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := a.Exec
	if run == nil {
		run = execRun
	}
	out, err := run(ctx, c.Command, c.Args...)
	text := strings.TrimSpace(string(out))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return text, fmt.Errorf("gave up after %s", timeout)
	}
	return text, err
}

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
