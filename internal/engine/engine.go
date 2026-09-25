// Package engine applies configuration to the kernel with a commit-confirmed
// safety net: an apply is provisional until confirmed, and reverts to the
// previous ruleset automatically when the confirmation window expires.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/logging"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/notify"
	"github.com/rforced/ostiole/internal/sshd"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysctl"
	"github.com/rforced/ostiole/internal/timezone"
)

// Errors returned by the engine.
var (
	ErrPending        = errors.New("an apply is awaiting confirmation; confirm or revert it first")
	ErrNothingPending = errors.New("no apply is awaiting confirmation")
	ErrNoRuleset      = errors.New("no confirmed ruleset to load")
	// ErrUnknownInterface says the name is not in the running configuration.
	ErrUnknownInterface = errors.New("interface is not in the configuration")
	// ErrNotDynamic says the interface holds no lease to renew.
	ErrNotDynamic = errors.New("interface has no dynamic addressing")
	// ErrNoNetwork says no backend manages the interfaces here.
	ErrNoNetwork = errors.New("network management is not enabled")
)

// applyTimeout bounds the part of an apply that changes the router. It is
// not the caller's to cut short, so something has to.
const applyTimeout = 5 * time.Minute

// Engine coordinates the store and the nft runner. One Engine per process
// holds the pending state in memory; the record in the store keeps another
// process from applying over it and lets the next start undo it.
type Engine struct {
	store  *store.Store
	nft    nft.Runner
	net    network.Backend  // nil when network management is disabled
	svc    network.Backend  // dnsmasq services; nil when not managed
	shape  network.Backend  // traffic shaping; nil when not managed
	sysctl sysctl.Applier   // nil in tests without a kernel
	clock  timezone.Applier // sets the router's zone; nil leaves it alone
	// journal bounds the system journal; nil leaves it alone.
	journal journald.Applier
	// logging caps what each daemon writes; nil leaves them alone.
	logging logging.Applier
	// ssh decides whether sshd takes a password; nil leaves it alone.
	ssh sshd.Applier
	// feeds supplies the contents of aliases fetched from a URL or a
	// country list; nil renders them empty.
	feeds  FeedSource
	log    *slog.Logger
	revert time.Duration // time budget for putting the previous state back
	// defaultPorts are what the fallback ruleset opens when there is no
	// configuration to read the management ports from.
	defaultPorts []uint16
	// alive reports whether a PID is a running Ostiole process.
	alive func(pid int) bool
	// notifier hears of an apply undone because nobody confirmed it; nil
	// tells nobody.
	notifier Notifier

	mu      sync.Mutex
	pending *pendingApply
	// recovered is set when Recover undid an apply, until the next commit.
	recovered *Recovered
	// sshErr is why sshd does not do what the last apply or revert asked of
	// it, for the dashboard.
	sshErr string
	// retimed is the offset Retime last loaded the ruleset at, until the
	// next load: were the offset not to be noted, it would load again
	// every hour.
	retimed atomic.Pointer[int]
	// kernelErr is why the kernel last refused the offset, so that it is
	// logged once rather than at every look.
	kernelErr atomic.Pointer[string]
}

type pendingApply struct {
	id            string
	cfg           *model.Config
	ruleset       string
	previous      string
	previousNet   network.Files
	previousSvc   network.Files
	previousShape network.Files
	// previousKernel is the conntrack ceiling before the apply, when it
	// could be read.
	previousKernel *sysctl.Settings
	since          time.Time
	deadline       time.Time
	timer          *time.Timer
}

// New returns an engine over st and runner. net may be nil to leave
// network configuration alone (firewall only).
func New(st *store.Store, runner nft.Runner, net network.Backend, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	// A minute to put things back: restoring services restarts daemons,
	// and the proxy alone may take fifteen seconds to settle.
	return &Engine{
		store: st, nft: runner, net: net, log: log, revert: time.Minute,
		defaultPorts: []uint16{model.DefaultWebPort, 22}, alive: ostioleRunning,
	}
}

// Notifier is told what the engine did that nobody asked it to.
type Notifier interface {
	Notify(notify.Event)
}

// WithNotifier has n told when an apply's window runs out.
func (e *Engine) WithNotifier(n Notifier) *Engine {
	e.notifier = n
	return e
}

// WithDefaultPorts sets the management ports the fallback ruleset opens
// when no configuration names them: where the daemon really listens.
func (e *Engine) WithDefaultPorts(ports ...uint16) *Engine {
	e.defaultPorts = ports
	return e
}

// WithServices adds the DHCP/DNS backend, applied after the network and
// reverted with it.
func (e *Engine) WithServices(b network.Backend) *Engine {
	e.svc = b
	return e
}

// WithShaping adds the traffic shaping backend. It goes last, because the
// queues hang off links that the network and the dialled sessions have to
// have brought up first, and it is inside the confirmation window like
// everything else: a line told it runs at 8 kbit/s is a line nobody can
// reach the UI over.
func (e *Engine) WithShaping(b network.Backend) *Engine {
	e.shape = b
	return e
}

// Preflighter is a backend that can refuse a plan before anything has
// been applied. Shaping uses it to say that the command it drives is not
// installed, and Tailscale that its daemon is not on the router, which is
// better learned now than after the configuration has been saved and the
// work has quietly not happened.
type Preflighter interface {
	Preflight(ctx context.Context, files network.Files) error
}

// FeedSource supplies the entries of aliases that are fetched rather than
// written out.
type FeedSource interface {
	Entries() map[string][]string
}

// WithFeeds supplies fetched alias contents to every render.
func (e *Engine) WithFeeds(f FeedSource) *Engine {
	e.feeds = f
	return e
}

// FeedEntries is what the renderer is given for fetched aliases.
func (e *Engine) FeedEntries() map[string][]string {
	if e.feeds == nil {
		return nil
	}
	return e.feeds.Entries()
}

// WithSysctl makes every apply and load also turn on router kernel
// settings (IP forwarding and friends).
func (e *Engine) WithSysctl(a sysctl.Applier) *Engine {
	e.sysctl = a
	return e
}

// applySysctl turns on the router settings and, where the configuration
// asks for one, the ceiling on tracked connections. It runs after the
// ruleset has been loaded: the ruleset is what pulls in nf_conntrack, so
// that is the first moment the ceiling can be written at all. before is
// what the kernel had ahead of an apply being undone: a ceiling the kernel
// was left to choose goes back to what it chose, since nothing else would
// lower one the apply raised.
func (e *Engine) applySysctl(cfg *model.Config, before *sysctl.Settings) {
	if e.sysctl == nil {
		return
	}
	var s sysctl.Settings
	if cfg != nil {
		s.ConntrackMax = cfg.System.ConntrackMax
	}
	if s.ConntrackMax == 0 && before != nil {
		s = *before
	}
	if err := e.sysctl.Apply(s); err != nil {
		e.log.Warn("could not set router sysctls; forwarding may not work", "err", err)
	}
}

// system is the part of a configuration the router's own settings come
// from. With nothing confirmed yet they are the defaults.
func system(cfg *model.Config) model.System {
	if cfg == nil {
		return model.System{}
	}
	return cfg.System
}

// applyHost sets the router's own settings from cfg: the kernel's, the
// clock's zone, the journal, the log levels and sshd. before is the
// conntrack ceiling to put back where cfg leaves it to the kernel.
func (e *Engine) applyHost(ctx context.Context, cfg *model.Config, before *sysctl.Settings) {
	e.applySysctl(cfg, before)
	e.applyTimezone(ctx, cfg)
	e.applyJournal(ctx, cfg)
	e.applyLogging(ctx, cfg)
	e.applySSH(ctx, cfg)
}

// WithTimezone makes every apply set the router's clock to the zone in the
// configuration.
func (e *Engine) WithTimezone(a timezone.Applier) *Engine {
	e.clock = a
	return e
}

// WithJournal makes every apply bound the router's system journal to the
// ceiling in the configuration.
func (e *Engine) WithJournal(a journald.Applier) *Engine {
	e.journal = a
	return e
}

// WithLogging makes every apply cap what the daemons Ostiole runs write
// to the journal.
func (e *Engine) WithLogging(a logging.Applier) *Engine {
	e.logging = a
	return e
}

// applyLogging writes the per-unit level caps.
func (e *Engine) applyLogging(ctx context.Context, cfg *model.Config) {
	if e.logging == nil {
		return
	}
	level := system(cfg).Logging.EffectiveLevel()
	if err := e.logging.Apply(ctx, level); err != nil {
		e.log.Warn("could not cap what the daemons log; they write as much as they did before",
			"level", level, "err", err)
	}
}

// WithSSH makes every apply set whether sshd accepts a password.
func (e *Engine) WithSSH(a sshd.Applier) *Engine {
	e.ssh = a
	return e
}

// applySSH writes sshd's drop-in. With nothing confirmed yet there is no
// setting to hold sshd to, so the drop-in goes and sshd takes its own.
func (e *Engine) applySSH(ctx context.Context, cfg *model.Config) {
	if e.ssh == nil {
		return
	}
	passwords := cfg == nil || cfg.System.Management.SSHPasswords
	e.sshErr = ""
	if err := e.ssh.Apply(ctx, passwords); err != nil {
		e.sshErr = err.Error()
		e.log.Warn("could not set how sshd lets people in",
			"passwords", passwords, "err", err)
	}
}

// applyJournal writes the journal ceiling.
func (e *Engine) applyJournal(ctx context.Context, cfg *model.Config) {
	if e.journal == nil {
		return
	}
	log := system(cfg).Logging
	if err := e.journal.Apply(ctx, log.MaxUse(), log.Retention()); err != nil {
		e.log.Warn("could not bound the system journal; it keeps what journald's own defaults allow",
			"gb", log.MaxUse(), "days", log.Retention(), "err", err)
	}
}

// applyTimezone puts the router in the configured zone. A new zone moves
// the offset nft read the schedules of the ruleset in at, which retime
// sees to.
func (e *Engine) applyTimezone(ctx context.Context, cfg *model.Config) {
	if e.clock == nil {
		return
	}
	zone := system(cfg).Zone()
	if err := e.clock.Apply(ctx, zone); err != nil {
		e.log.Warn("could not set the router's timezone; its clock still reads in the old one",
			"zone", zone, "err", err)
	}
}

// Plan is everything rendered from a configuration.
type Plan struct {
	Ruleset  string        `json:"ruleset"`
	Network  network.Files `json:"network,omitempty"`
	Services network.Files `json:"services,omitempty"`
	Shaping  network.Files `json:"shaping,omitempty"`
}

// Store exposes the underlying store for read-only callers.
func (e *Engine) Store() *store.Store { return e.store }

// Check validates cfg, renders the ruleset and network units, and has nft
// dry-run the ruleset.
func (e *Engine) Check(ctx context.Context, cfg *model.Config) (*Plan, error) {
	ruleset, err := nft.RenderWithFeeds(cfg, e.FeedEntries())
	if err != nil {
		return nil, err
	}
	plan := &Plan{Ruleset: ruleset}
	if e.net != nil {
		files, err := e.net.Render(cfg)
		if err != nil {
			return nil, fmt.Errorf("network: %w", err)
		}
		plan.Network = files
	}
	if e.svc != nil {
		files, err := e.svc.Render(cfg)
		if err != nil {
			return nil, fmt.Errorf("services: %w", err)
		}
		plan.Services = files
		if p, ok := e.svc.(Preflighter); ok {
			if err := p.Preflight(ctx, files); err != nil {
				return nil, fmt.Errorf("services: %w", err)
			}
		}
	}
	if e.shape != nil {
		files, err := e.shape.Render(cfg)
		if err != nil {
			return nil, fmt.Errorf("shaping: %w", err)
		}
		plan.Shaping = files
		if p, ok := e.shape.(Preflighter); ok {
			if err := p.Preflight(ctx, files); err != nil {
				return nil, fmt.Errorf("shaping: %w", err)
			}
		}
	}
	if err := e.nft.Check(ctx, ruleset); err != nil {
		return nil, err
	}
	return plan, nil
}

// ApplyOptions tunes Apply.
type ApplyOptions struct {
	// ConfirmTimeout is how long to wait for Confirm before reverting.
	// Zero commits immediately with no safety window.
	ConfirmTimeout time.Duration
}

// ApplyResult describes what Apply did.
type ApplyResult struct {
	Plan
	Pending  bool            `json:"pending"`
	Deadline time.Time       `json:"deadline,omitzero"`
	Archived *store.Revision `json:"archived,omitempty"`
}

// Apply loads cfg into the kernel and, when a network backend is present,
// installs the network units. With a confirm timeout the change stays
// provisional until Confirm; otherwise it is committed to the store at once.
//
// The caller's context bounds the check. Once the router is being changed,
// the apply finishes or goes back to where it started whatever the caller
// does: a browser tab closed halfway must not leave half a configuration.
func (e *Engine) Apply(ctx context.Context, cfg *model.Config, opts ApplyOptions) (*ApplyResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil {
		return nil, ErrPending
	}

	plan, err := e.Check(ctx, cfg)
	if err != nil {
		return nil, err
	}
	previous, err := e.previousRuleset()
	if err != nil {
		return nil, fmt.Errorf("load previous ruleset: %w", err)
	}
	rec, base, err := e.begin(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applyTimeout)
	defer cancel()

	if err := e.load(ctx, plan.Ruleset); err != nil {
		// One transaction, so nothing changed.
		e.keep(base)
		return nil, err
	}
	e.applyHost(ctx, cfg, nil)
	e.retime(ctx, plan.Ruleset)
	rollback := &pendingApply{
		previous: previous, previousNet: rec.Network,
		previousSvc: rec.Services, previousShape: rec.Shaping, previousKernel: rec.Kernel,
	}
	if e.net != nil {
		// The units were written and networkd told before the failure,
		// so the previous ones go back too, or the router would run a
		// network nobody confirmed.
		if err := e.net.Apply(ctx, plan.Network); err != nil {
			if rerr := e.undo(ctx, rollback); rerr != nil {
				e.log.Error("network apply failed and rollback failed too", "networkErr", err, "err", rerr)
				return nil, fmt.Errorf("network apply failed (%w) and rollback failed (%w)", err, rerr)
			}
			return nil, fmt.Errorf("network apply failed, firewall and network reverted: %w", err)
		}
	}
	if e.svc != nil {
		if err := e.svc.Apply(ctx, plan.Services); err != nil {
			if rerr := e.undo(ctx, rollback); rerr != nil {
				e.log.Error("services apply failed and rollback failed too", "servicesErr", err, "err", rerr)
				return nil, fmt.Errorf("services apply failed (%w) and rollback failed (%w)", err, rerr)
			}
			return nil, fmt.Errorf("services apply failed, firewall and network reverted: %w", err)
		}
	}
	// Shaping comes last: its queues hang off links that the network and
	// the dialled sessions have only just brought up.
	if e.shape != nil {
		if err := e.shape.Apply(ctx, plan.Shaping); err != nil {
			if rerr := e.undo(ctx, rollback); rerr != nil {
				e.log.Error("traffic shaping failed and rollback failed too", "shapingErr", err, "err", rerr)
				return nil, fmt.Errorf("traffic shaping failed (%w) and rollback failed (%w)", err, rerr)
			}
			return nil, fmt.Errorf("traffic shaping failed, everything else reverted: %w", err)
		}
	}
	e.log.Info("configuration applied", "rules", len(cfg.Rules), "networkUnits", len(plan.Network), "confirmTimeout", opts.ConfirmTimeout)

	if opts.ConfirmTimeout <= 0 {
		archived, err := e.commit(cfg, plan.Ruleset)
		if err != nil {
			// The record stays, so the next start puts the rest back to
			// match the saved configuration, as a reboot does the firewall.
			return nil, err
		}
		e.clearRecord()
		return &ApplyResult{Plan: *plan, Archived: archived}, nil
	}

	now := time.Now()
	p := &pendingApply{
		id:             rec.ID,
		cfg:            cfg,
		ruleset:        plan.Ruleset,
		previous:       previous,
		previousNet:    rec.Network,
		previousSvc:    rec.Services,
		previousShape:  rec.Shaping,
		previousKernel: rec.Kernel,
		since:          now,
		deadline:       now.Add(opts.ConfirmTimeout),
	}
	id := p.id
	p.timer = time.AfterFunc(opts.ConfirmTimeout, func() { e.expire(id) })
	e.pending = p
	return &ApplyResult{Plan: *plan, Pending: true, Deadline: p.deadline}, nil
}

// begin writes the record of an apply before it changes anything. It takes
// the store lock so that two processes cannot both find no record and both
// apply. It returns the record, and the one an unfinished earlier apply
// left, whose "before" this apply inherits.
func (e *Engine) begin(cfg *model.Config) (rec, base *record, err error) {
	unlock, err := e.store.Lock()
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	if base, err = e.leftover(); err != nil {
		return nil, nil, err
	}
	rec = &record{ID: newID(), PID: os.Getpid(), Boot: bootID(), Since: time.Now(), Config: digest(cfg)}
	if base != nil {
		rec.Network, rec.Services, rec.Shaping, rec.Kernel = base.Network, base.Services, base.Shaping, base.Kernel
	}
	if err := e.snapshot(rec); err != nil {
		return nil, nil, err
	}
	if err := e.writeRecord(rec); err != nil {
		return nil, nil, fmt.Errorf("record the apply before making it: %w", err)
	}
	return rec, base, nil
}

// Confirm commits the pending apply.
func (e *Engine) Confirm(_ context.Context) (*store.Revision, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.pending
	if p == nil {
		return nil, ErrNothingPending
	}
	p.timer.Stop()
	e.pending = nil
	archived, err := e.commit(p.cfg, p.ruleset)
	if err != nil {
		// Kernel has the new ruleset but the store does not. Leave it: a
		// reboot loads the old one and the record has the next start put
		// the rest back to match, which is the safer failure.
		return nil, fmt.Errorf("apply confirmed but not saved: %w", err)
	}
	e.clearRecord()
	e.log.Info("apply confirmed")
	return archived, nil
}

// Revert cancels the pending apply and restores the previous ruleset. It
// runs to the end even when the caller goes away.
func (e *Engine) Revert(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.pending
	if p == nil {
		return ErrNothingPending
	}
	p.timer.Stop()
	e.pending = nil
	if err := e.undo(ctx, p); err != nil {
		return fmt.Errorf("revert: %w", err)
	}
	e.log.Info("apply reverted by request")
	return nil
}

// previousRuleset is what going back puts in the kernel: the last
// confirmed ruleset, or the fallback when nothing has been confirmed.
// Never an empty table: a router without one is wide open.
func (e *Engine) previousRuleset() (string, error) {
	ruleset, err := e.store.LoadRuleset()
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nft.Fallback(nil, e.defaultPorts), nil
	case errors.Is(err, store.ErrStaleRuleset):
		// A crash split the last save; the configuration is the half to go
		// back to.
		cfg, err := e.store.Load()
		if errors.Is(err, store.ErrNotFound) {
			return nft.Fallback(nil, e.defaultPorts), nil
		}
		if err != nil {
			return "", err
		}
		return nft.RenderWithFeeds(cfg, e.FeedEntries())
	}
	return ruleset, err
}

// restore puts the previous state back: the firewall, the network, and the
// router's own settings as the confirmed configuration has them. A backend
// with no "before" was not driven when it was taken and is left alone.
func (e *Engine) restore(ctx context.Context, p *pendingApply) error {
	var errs []error
	if err := e.load(ctx, p.previous); err != nil {
		errs = append(errs, fmt.Errorf("firewall: %w", err))
	}
	if e.net != nil && p.previousNet != nil {
		if err := e.net.Apply(ctx, p.previousNet); err != nil {
			errs = append(errs, fmt.Errorf("network: %w", err))
		}
	}
	if e.svc != nil && p.previousSvc != nil {
		if err := e.svc.Apply(ctx, p.previousSvc); err != nil {
			errs = append(errs, fmt.Errorf("services: %w", err))
		}
	}
	if e.shape != nil && p.previousShape != nil {
		if err := e.shape.Apply(ctx, p.previousShape); err != nil {
			errs = append(errs, fmt.Errorf("shaping: %w", err))
		}
	}
	// After the firewall, which a slow clock or sshd must not hold up.
	if confirmed, ok := e.confirmed(); ok {
		e.applyHost(ctx, confirmed, p.previousKernel)
		e.retime(ctx, p.previous)
	}
	return errors.Join(errs...)
}

// confirmed is the configuration last committed, nil before the first.
// ok is false when it cannot be read: the router's own settings are then
// left as they are rather than set to defaults, which would, for one, let
// passwords back into sshd.
func (e *Engine) confirmed() (cfg *model.Config, ok bool) {
	cfg, err := e.store.Load()
	switch {
	case err == nil:
		return cfg, true
	case errors.Is(err, store.ErrNotFound):
		return nil, true
	}
	e.log.Warn("could not read the confirmed configuration; the router's own settings stay as they are", "err", err)
	return nil, false
}

func (e *Engine) expire(id string) {
	// The notifier reads the configuration in force through Effective,
	// which takes the lock, so it is told once the lock is let go.
	if ev, ok := e.undoExpired(id); ok {
		e.tell(ev)
	}
}

// undoExpired puts back the apply whose window ran out, and says what to
// tell of it.
func (e *Engine) undoExpired(id string) (notify.Event, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.pending
	if p == nil || p.id != id {
		return notify.Event{}, false
	}
	e.pending = nil
	window := p.deadline.Sub(p.since)
	if err := e.undo(context.Background(), p); err != nil {
		e.log.Error("automatic revert failed; system may still have the unconfirmed configuration", "err", err)
		return notify.Event{
			Kind: notify.KindApplyReverted, Title: "An unconfirmed apply could not be undone",
			Detail: "Nobody confirmed it within " + minutes(window) + ", and putting the configuration before it back failed: " +
				err.Error() + ". The router may still run what nobody confirmed.",
		}, true
	}
	e.log.Warn("apply not confirmed in time; reverted to previous ruleset", "window", window)
	return notify.Event{
		Kind: notify.KindApplyReverted, Title: "An apply was not confirmed and was undone",
		Detail: "Nobody confirmed it within " + minutes(window) + ". The configuration before it is back.",
	}, true
}

func (e *Engine) tell(ev notify.Event) {
	if e.notifier != nil {
		e.notifier.Notify(ev)
	}
}

// minutes says how long a window was the way a person would.
func minutes(d time.Duration) string {
	switch m := d.Round(time.Minute) / time.Minute; {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case m == 1:
		return "a minute"
	default:
		return strconv.Itoa(int(m)) + " minutes"
	}
}

func (e *Engine) commit(cfg *model.Config, ruleset string) (*store.Revision, error) {
	unlock, err := e.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	archived, err := e.store.Save(cfg, ruleset)
	if err != nil {
		return archived, err
	}
	// The kernel runs a confirmed ruleset again, so neither note applies.
	e.clearFallback()
	e.recovered = nil
	return archived, nil
}

// Effective returns the configuration the kernel is actually running: the
// pending one while an apply awaits confirmation, otherwise the last saved
// one. Watchers like the gateway monitor follow it, so a change can be
// judged inside the confirmation window instead of only after it, and an
// expiry puts the old one back on the next pass.
func (e *Engine) Effective() *model.Config {
	e.mu.Lock()
	pending := e.pending
	e.mu.Unlock()
	if pending != nil {
		return pending.cfg
	}
	cfg, err := e.store.Load()
	if err != nil {
		return nil
	}
	return cfg
}

// RenewLease asks the network backend for a fresh lease on a configured
// dynamic interface. Release drops the lease first and starts over,
// which is the button to press when a modem has stopped answering.
func (e *Engine) RenewLease(ctx context.Context, name string, release bool) error {
	r, ok := e.net.(network.Renewer)
	if !ok {
		return ErrNoNetwork
	}
	cfg := e.Effective()
	if cfg == nil {
		return ErrUnknownInterface
	}
	in, found := cfg.Interface(name)
	if !found {
		return ErrUnknownInterface
	}
	if !in.DynamicAddressing() {
		return fmt.Errorf("%s: %w", name, ErrNotDynamic)
	}
	if err := r.Renew(ctx, name, release); err != nil {
		return err
	}
	e.log.Info("lease renewal requested", "interface", name, "release", release)
	return nil
}

// LoadSource says which ruleset Load put in the kernel.
type LoadSource string

// What Load can put in.
const (
	// LoadedSaved is the last confirmed ruleset, as saved.
	LoadedSaved LoadSource = "saved"
	// LoadedRendered is the saved configuration rendered again, because
	// the saved ruleset was missing or would not load.
	LoadedRendered LoadSource = "rendered"
	// LoadedFallback accepts the management ports and forwards nothing.
	LoadedFallback LoadSource = "fallback"
)

// LoadResult says what Load put in and, when it was not the saved
// ruleset, why.
type LoadResult struct {
	Source LoadSource
	Reason error
}

// Load puts the last confirmed ruleset in the kernel without touching the
// configuration. It is the early-boot path, so it never leaves the router
// without a firewall: a saved ruleset that is missing or that nft refuses
// is rendered again from the saved configuration, and when that will not
// load either, the fallback goes in. The error is set only when not even
// the fallback loaded.
func (e *Engine) Load(ctx context.Context) (LoadResult, error) {
	cfg, err := e.store.Load()
	if err != nil || cfg.Validate() != nil {
		cfg = nil
	}
	ruleset, why := e.store.LoadRuleset()
	if errors.Is(why, store.ErrNotFound) {
		why = ErrNoRuleset
	}
	if why == nil {
		if why = e.load(ctx, ruleset); why == nil {
			return e.loaded(cfg, LoadResult{Source: LoadedSaved}), nil
		}
	}
	if cfg != nil {
		if rendered, err := nft.RenderWithFeeds(cfg, e.FeedEntries()); err == nil && rendered != ruleset {
			if err := e.load(ctx, rendered); err == nil {
				return e.loaded(cfg, LoadResult{Source: LoadedRendered, Reason: why}), nil
			}
		}
	}
	if err := e.load(ctx, nft.Fallback(cfg, e.defaultPorts)); err != nil {
		return LoadResult{}, fmt.Errorf("the saved ruleset did not load (%w) and neither did the fallback: %w", why, err)
	}
	return e.loaded(cfg, LoadResult{Source: LoadedFallback, Reason: why}), nil
}

// loaded finishes a Load that put a ruleset in: it notes or clears the
// fallback and turns the router settings on. Those are best effort: a
// ruleset that loads is worth more than the settings that go with it, so
// an unreadable configuration still leaves the router forwarding on the
// kernel's own limits.
func (e *Engine) loaded(cfg *model.Config, res LoadResult) LoadResult {
	if res.Source == LoadedFallback {
		e.noteFallback(res.Reason)
	} else {
		e.clearFallback()
	}
	e.applySysctl(cfg, nil)
	return res
}

// PendingStatus describes an unconfirmed apply.
type PendingStatus struct {
	Since     time.Time     `json:"since"`
	Deadline  time.Time     `json:"deadline"`
	Remaining time.Duration `json:"remaining"`
}

// Status is a point-in-time view of the engine and kernel.
type Status struct {
	Configured  bool           `json:"configured"`
	TableLoaded bool           `json:"tableLoaded"`
	Network     string         `json:"network"` // backend name or "none"
	Pending     *PendingStatus `json:"pending,omitempty"`
	// Fallback is set while the kernel runs the fallback ruleset because
	// the saved one would not load.
	Fallback *FallbackStatus `json:"fallback,omitempty"`
	// Recovered is set when this daemon undid an apply that a crash or a
	// reboot left unconfirmed, until the next commit.
	Recovered *Recovered `json:"recovered,omitempty"`
	// SSH is why sshd does not let people in the way the last apply or
	// revert asked.
	SSH string `json:"ssh,omitempty"`
}

// Status reports whether configuration exists, whether the table is in the
// kernel, and any pending apply.
func (e *Engine) Status(ctx context.Context) (Status, error) {
	e.mu.Lock()
	var pending *PendingStatus
	if p := e.pending; p != nil {
		pending = &PendingStatus{Since: p.since, Deadline: p.deadline, Remaining: time.Until(p.deadline).Truncate(time.Second)}
	}
	recovered, sshErr := e.recovered, e.sshErr
	e.mu.Unlock()

	st := Status{
		Configured: e.store.Exists(), Pending: pending, Network: "none", Fallback: e.readFallback(),
		Recovered: recovered, SSH: sshErr,
	}
	if e.net != nil {
		st.Network = e.net.Name()
	}
	_, err := e.nft.ListTableJSON(ctx)
	switch {
	case err == nil:
		st.TableLoaded = true
	case errors.Is(err, nft.ErrNoTable):
	default:
		return st, err
	}
	return st, nil
}

// Counters reads live rule counters from the kernel.
func (e *Engine) Counters(ctx context.Context) (nft.Counters, error) {
	raw, err := e.nft.ListTableJSON(ctx)
	if err != nil {
		return nil, err
	}
	return nft.ParseCounters(raw)
}

// SystemRules lists the rules cfg makes Ostiole add on its own, for showing
// next to the operator's. It only renders: nothing is checked or applied,
// so it is cheap enough to call as the draft is edited.
func (e *Engine) SystemRules(cfg *model.Config) ([]nft.SystemRule, error) {
	return nft.SystemRules(cfg, e.FeedEntries())
}

// SystemNAT lists the outbound NAT rules cfg makes Ostiole write on its
// own, as cheaply as SystemRules.
func (e *Engine) SystemNAT(cfg *model.Config) ([]nft.SystemNAT, error) {
	out, err := nft.Build(cfg, e.FeedEntries())
	if err != nil {
		return nil, err
	}
	return out.NAT, nil
}

// Mappings reads the port mappings clients have opened for themselves. They
// come from the ruleset because that is where they are: the daemon that
// answers the mapping protocols writes into a chain of ours.
func (e *Engine) Mappings(ctx context.Context) ([]nft.Mapping, error) {
	raw, err := e.nft.ListTableJSON(ctx)
	if err != nil {
		return nil, err
	}
	return nft.ParseMappings(raw)
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
