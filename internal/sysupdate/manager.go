package sysupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rforced/ostiole/internal/panics"
)

// ErrBusy means an update is already running.
var ErrBusy = errors.New("an update is already running on this router")

// ErrNotRoot means the daemon cannot drive a package manager from here.
var ErrNotRoot = errors.New("system updates need root; this daemon is not running as root")

// Status is the whole answer to "what about the operating system", as
// the page shows it. The mode and schedule are filled in by the caller
// from the configuration, because those are settings rather than facts
// about the router.
type Status struct {
	Snapshot
	// Manager is the package manager found, e.g. "dnf".
	Manager string `json:"manager,omitempty"`
	// Distro is what /etc/os-release calls this router.
	Distro string `json:"distro,omitempty"`
	// Available says whether updates can be driven from here at all.
	Available bool `json:"available"`
	// Unavailable explains why not, in the words the page shows.
	Unavailable string `json:"unavailable,omitempty"`
	// SecurityCapable is false where the distro has no security-only
	// channel, which is what greys out that choice.
	SecurityCapable bool `json:"securityCapable"`
	// ExcludeSupported says whether the never-upgrade list can be
	// honoured in the mode in force.
	ExcludeSupported bool `json:"excludeSupported"`
	// Mode and the two schedules come from the configuration: one for
	// asking what is waiting, one for installing it.
	Mode            string `json:"mode,omitempty"`
	CheckSchedule   string `json:"checkSchedule,omitempty"`
	InstallSchedule string `json:"installSchedule,omitempty"`
	// Running is true while a transaction is going.
	Running bool `json:"running"`
}

// Manager drives the package manager on this router for the API and the
// scheduled cron.
type Manager struct {
	// Driver is the package manager found, nil when there is none.
	Driver Driver
	// Unavailable explains a nil driver, or a router that cannot install.
	Unavailable string
	// Distro is the pretty name from /etc/os-release.
	Distro string
	// Run is how the package manager is run: outside the daemon's
	// sandbox where that is possible, because none of these tools can
	// work with a read-only /var.
	Run   Runner
	State *State
	Log   *slog.Logger

	// unit drives systemd itself — systemctl, journalctl, systemd-run —
	// which works perfectly well from inside the sandbox and must not be
	// wrapped in another transient unit.
	unit transient
	// direct skips the transient unit; see Options.Direct.
	direct bool

	mu      sync.Mutex
	running bool
}

// Options tune New.
type Options struct {
	// PackageManager names the manager instead of looking for one, for a
	// router with two and for tests.
	PackageManager string
	// StateDir is where the snapshot is kept.
	StateDir string
	// Run executes commands; nil uses the real ones.
	Run Runner
	// Root says whether this daemon can actually install. A router that is
	// not root still gets a page, and an explanation on it.
	Root bool
	// Direct runs every package manager command as a child of this
	// process instead of in a transient unit. A test sets it, so that its
	// fake runner sees the command and not systemd-run.
	Direct bool
	Log    *slog.Logger
}

// New finds the package manager and reads what is known about it.
func New(o Options) *Manager {
	run := o.Run
	if run == nil {
		run = ExecRunner{}
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		Run:    run,
		State:  NewState(o.StateDir),
		Log:    log,
		Distro: distroName(),
		unit:   transient{unit: TransientUnit, run: run},
		direct: o.Direct,
	}
	// Only root can raise a transient unit, and only a router with systemd
	// has one to raise; everywhere else the command runs as a child and
	// takes the sandbox with it.
	if o.Root && !o.Direct && m.unit.supported() {
		m.Run = hostRunner{inner: run, seq: &cmdSeq}
	}
	driver, err := Detect(o.PackageManager)
	switch {
	case err != nil:
		m.Unavailable = err.Error()
	case !o.Root:
		m.Driver = driver
		m.Unavailable = ErrNotRoot.Error()
	default:
		m.Driver = driver
	}
	// A driver that needs scratch space needs it where the daemon and a
	// transient unit both see the same directory: PrivateTmp means /tmp
	// is not that place.
	if scratch, ok := m.Driver.(scratchUser); ok && o.StateDir != "" {
		m.Driver = scratch.useScratch(o.StateDir)
	}
	return m
}

var cmdSeq atomic.Int64

// scratchUser is a driver that writes somewhere while it works.
type scratchUser interface {
	useScratch(dir string) Driver
}

// Available reports whether this router can be checked and updated.
func (m *Manager) Available() bool { return m.Driver != nil && m.Unavailable == "" }

// Status is what the API returns.
func (m *Manager) Status(security bool) Status {
	st := Status{
		Snapshot:    m.State.Snapshot(),
		Distro:      m.Distro,
		Available:   m.Available(),
		Unavailable: m.Unavailable,
	}
	if m.Driver != nil {
		st.Manager = m.Driver.Name()
		st.SecurityCapable = m.Driver.SecurityCapable()
		st.ExcludeSupported = m.Driver.ExcludeSupported(security)
	}
	m.mu.Lock()
	st.Running = m.running
	m.mu.Unlock()
	return st
}

// Check asks the package manager what is waiting and records it.
func (m *Manager) Check(ctx context.Context) (Pending, error) {
	if !m.Available() {
		return Pending{}, m.unavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	pending, err := m.Driver.Check(ctx, m.Run)
	now := time.Now()
	if err != nil {
		m.State.Update(func(s *Snapshot) {
			s.LastCheck, s.CheckError = now, err.Error()
		})
		return Pending{}, err
	}
	need, why := m.Driver.RebootRequired(ctx, m.Run)
	m.State.Update(func(s *Snapshot) {
		s.LastCheck, s.CheckError, s.Pending = now, "", pending
		s.RebootRequired, s.RebootReason = need, why
	})
	return pending, nil
}

// Mode is what a scheduled run may install. It mirrors the update mode
// in the configuration without this package having to know about the
// configuration.
type Mode string

// Modes.
const (
	ModeManual   Mode = "manual"
	ModeSecurity Mode = "security"
	ModeAll      Mode = "all"
	ModeDisabled Mode = "disabled"
)

// Apply installs what is waiting and returns what it did. It blocks
// until the transaction finishes; the API starts it in the background
// with Start instead.
func (m *Manager) Apply(ctx context.Context, security bool, exclude []string) (string, error) {
	if err := m.begin(security); err != nil {
		return "", err
	}
	defer m.end()
	return m.apply(ctx, security, exclude)
}

// apply is Apply for a caller that has already claimed the router.
func (m *Manager) apply(ctx context.Context, security bool, exclude []string) (string, error) {
	// The list is taken fresh: it decides what an apt or zypper run
	// names on its command line, and a stale list installs the wrong
	// things.
	pending, err := m.Check(ctx)
	if err != nil {
		return "", err
	}
	return m.install(ctx, security, exclude, pending)
}

// CheckScheduled is the scheduled check: ask what is waiting and record
// it, installing none of it. It claims the router the way an install
// does, because no package manager enjoys being asked a question while it
// is in the middle of a transaction.
func (m *Manager) CheckScheduled(ctx context.Context) (string, error) {
	if err := m.begin(false); err != nil {
		return "", err
	}
	defer m.end()

	pending, err := m.Check(ctx)
	if err != nil {
		return "", err
	}
	return waitingLine(pending), nil
}

// waitingLine is what a check found, in the words both scheduled runs
// report it in.
func waitingLine(p Pending) string {
	return fmt.Sprintf("%d update(s) waiting, %d of them security fixes", len(p.Packages), p.Security)
}

// RunScheduled is the scheduled cron: always check, then install only
// what the mode allows. Manual still checks, so the page can say what is
// waiting without the router changing under anyone.
func (m *Manager) RunScheduled(ctx context.Context, mode Mode, exclude []string) (string, error) {
	security := mode == ModeSecurity
	if err := m.begin(security); err != nil {
		return "", err
	}
	defer m.end()

	pending, err := m.Check(ctx)
	if err != nil {
		return "", err
	}
	waiting := waitingLine(pending)
	switch {
	case mode == ModeDisabled:
		return waiting + "; this router installs nothing on its own", nil
	case mode == ModeManual:
		return waiting + "; this router installs them by hand", nil
	case len(pending.Packages) == 0:
		return "nothing to install", nil
	case security && pending.Security == 0:
		return waiting + "; nothing installed", nil
	}
	return m.install(ctx, security, exclude, pending)
}

// install runs the upgrade and records how it went.
func (m *Manager) install(ctx context.Context, security bool, exclude []string, pending Pending) (string, error) {
	argv := m.Driver.UpgradeArgv(security, exclude, pending)
	mode := string(ModeAll)
	if security {
		mode = string(ModeSecurity)
	}
	if len(argv) == 0 {
		out := "nothing to install"
		m.record(mode, out, nil)
		return out, nil
	}
	if len(exclude) > 0 && !m.Driver.ExcludeSupported(security) {
		m.Log.Warn("the never-upgrade list cannot be honoured in this mode",
			"manager", m.Driver.Name(), "security", security)
	}

	out, err := m.run(ctx, argv)
	m.record(mode, out, err)
	// What is waiting and whether the router wants a reboot have both
	// changed, and the page should not have to be told twice.
	if _, checkErr := m.Check(context.WithoutCancel(ctx)); checkErr != nil {
		m.Log.Debug("could not re-check after an update", "err", checkErr)
	}
	return out, err
}

// begin claims the router for one update at a time.
func (m *Manager) begin(security bool) error {
	if !m.Available() {
		return m.unavailable()
	}
	if security && !m.Driver.SecurityCapable() {
		return errors.New(SecurityUnavailable(m.Driver))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return ErrBusy
	}
	m.running = true
	return nil
}

func (m *Manager) end() {
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
}

// Start begins an update in the background and returns once it is
// going, because the browser cannot wait an hour for a distro upgrade.
// The router is claimed before this returns, so the status the caller sends
// back already says an update is running.
func (m *Manager) Start(security bool, exclude []string) error {
	if err := m.begin(security); err != nil {
		return err
	}
	go func() {
		defer m.end()
		defer panics.Recover(m.Log, "system update")
		ctx, cancel := context.WithTimeout(context.Background(), UpgradeTimeout+2*CheckTimeout)
		defer cancel()
		if _, err := m.apply(ctx, security, exclude); err != nil {
			m.Log.Warn("a system update failed", "err", err)
		}
	}()
	return nil
}

// run puts the transaction in a unit of its own where it can outlive the
// daemon, falling back to a plain child where systemd cannot be reached.
func (m *Manager) run(ctx context.Context, argv []string) (string, error) {
	if !m.unit.supported() {
		return runDirect(ctx, m.Run, argv, UpgradeTimeout)
	}
	if err := m.unit.start(ctx, argv, UpgradeTimeout); err != nil {
		return "", err
	}
	m.Log.Info("system update started", "unit", m.unit.unit, "command", strings.Join(argv, " "))
	return m.collect(context.WithoutCancel(ctx))
}

// collect waits for the transient unit, reads its output and clears it.
func (m *Manager) collect(ctx context.Context) (string, error) {
	res, err := m.unit.wait(ctx)
	if err != nil {
		return "", err
	}
	out := m.unit.output(ctx, res.Invocation)
	m.unit.clear(ctx)
	return out, res.err()
}

// Reattach picks up a transaction that outlived the daemon, so an update
// that restarted Ostiole still reports how it ended. It returns without
// doing anything when no unit is running.
func (m *Manager) Reattach(ctx context.Context) {
	if !m.unit.supported() {
		return
	}
	st := m.unit.state(ctx)
	if !st.Known {
		return
	}
	if !st.Active {
		// It finished while we were away: keep the result and tidy up.
		out := m.unit.output(ctx, st.Invocation)
		m.unit.clear(ctx)
		m.record("", out, st.err())
		return
	}
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	m.Log.Info("a system update was still running; reattaching", "unit", m.unit.unit)
	go func() {
		// Held until the re-check is in, as Start does: the page stops
		// polling at the first status that is not running.
		defer m.end()
		defer panics.Recover(m.Log, "system update")
		out, err := m.collect(ctx)
		m.record("", out, err)
		if _, checkErr := m.Check(ctx); checkErr != nil {
			m.Log.Debug("could not re-check after an update", "err", checkErr)
		}
	}()
}

// Reboot restarts the router. It is the one button on this page that is
// worse to press by accident than to forget.
func (m *Manager) Reboot(ctx context.Context) error {
	if _, err := m.Run.Run(ctx, "systemctl", "reboot"); err != nil {
		return err
	}
	return nil
}

func (m *Manager) record(mode, output string, err error) {
	m.State.Update(func(s *Snapshot) {
		s.LastRun = time.Now()
		if mode != "" {
			s.LastMode = mode
		}
		s.LastOutput = output
		s.LastError = ""
		if err != nil {
			s.LastError = err.Error()
		}
	})
}

func (m *Manager) unavailable() error {
	if m.Unavailable != "" {
		return errors.New(m.Unavailable)
	}
	return ErrNoManager
}

// osRelease is where the distro names itself.
var osRelease = "/etc/os-release"

// distroName reads PRETTY_NAME, so the page can say "Rocky Linux 10.0"
// rather than only "dnf".
func distroName() string {
	raw, err := os.ReadFile(osRelease)
	if err != nil {
		return ""
	}
	fields := map[string]string{}
	for _, line := range lines(raw) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		fields[key] = strings.Trim(value, `"'`)
	}
	if name := fields["PRETTY_NAME"]; name != "" {
		return name
	}
	return strings.TrimSpace(fields["NAME"] + " " + fields["VERSION_ID"])
}
