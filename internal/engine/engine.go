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
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
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
)

// Engine coordinates the store and the nft runner. One Engine per process;
// the pending state is in memory, so CLI and daemon applies must not be
// interleaved (the store lock only protects the files).
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
	// ssh decides whether sshd takes a password; nil leaves it alone.
	ssh sshd.Applier
	// feeds supplies the contents of aliases fetched from a URL or a
	// country list; nil renders them empty.
	feeds  FeedSource
	log    *slog.Logger
	revert time.Duration // time budget for an automatic revert

	mu      sync.Mutex
	pending *pendingApply
}

type pendingApply struct {
	id            string
	cfg           *model.Config
	ruleset       string
	previous      string
	previousNet   network.Files
	previousSvc   network.Files
	previousShape network.Files
	since         time.Time
	deadline      time.Time
	timer         *time.Timer
}

// New returns an engine over st and runner. net may be nil to leave
// network configuration alone (firewall only).
func New(st *store.Store, runner nft.Runner, net network.Backend, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{store: st, nft: runner, net: net, log: log, revert: 15 * time.Second}
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
// installed, which is better learned now than after the configuration has
// been saved and the queues have quietly not appeared.
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

func (e *Engine) applySysctl() {
	if e.sysctl == nil {
		return
	}
	if err := e.sysctl.Apply(); err != nil {
		e.log.Warn("could not set router sysctls; forwarding may not work", "err", err)
	}
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

// WithSSH makes every apply set whether sshd accepts a password.
func (e *Engine) WithSSH(a sshd.Applier) *Engine {
	e.ssh = a
	return e
}

// applySSH writes sshd's drop-in. Like the timezone it is not part of the
// revert: locking yourself out of SSH is not something a ruleset rollback
// can help with, and the setting is in the configuration a backup carries.
func (e *Engine) applySSH(ctx context.Context, cfg *model.Config) {
	if e.ssh == nil {
		return
	}
	if err := e.ssh.Apply(ctx, cfg.System.Management.SSHPasswords); err != nil {
		e.log.Warn("could not set how sshd lets people in; it admits people as it did before",
			"passwords", cfg.System.Management.SSHPasswords, "err", err)
	}
}

// applyJournal writes the journal ceiling. Like the timezone it is not
// part of the revert: a log ceiling is nothing anybody can be locked out
// by.
func (e *Engine) applyJournal(ctx context.Context, cfg *model.Config) {
	if e.journal == nil {
		return
	}
	if err := e.journal.Apply(ctx, cfg.System.JournalMaxUse()); err != nil {
		e.log.Warn("could not bound the system journal; it keeps what journald's own defaults allow",
			"gb", cfg.System.JournalMaxUse(), "err", err)
	}
}

// applyTimezone puts the router in the configured zone. It is not part of
// the revert: the zone is persistent state on the router rather than
// something a bad ruleset can lock anybody out of, and a clock that
// jumps back on an expiry would only confuse the logs of the attempt.
func (e *Engine) applyTimezone(ctx context.Context, cfg *model.Config) {
	if e.clock == nil {
		return
	}
	if err := e.clock.Apply(ctx, cfg.System.Zone()); err != nil {
		e.log.Warn("could not set the router's timezone; its clock still reads in the old one",
			"zone", cfg.System.Zone(), "err", err)
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
	Deadline time.Time       `json:"deadline,omitempty"`
	Archived *store.Revision `json:"archived,omitempty"`
}

// Apply loads cfg into the kernel and, when a network backend is present,
// installs the network units. With a confirm timeout the change stays
// provisional until Confirm; otherwise it is committed to the store at once.
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
	previous, err := e.store.LoadRuleset()
	if errors.Is(err, store.ErrNotFound) {
		// Nothing confirmed yet: reverting means removing our table entirely.
		previous = nft.EmptyRuleset()
	} else if err != nil {
		return nil, fmt.Errorf("load previous ruleset: %w", err)
	}
	var previousNet, previousSvc, previousShape network.Files
	if e.net != nil {
		if previousNet, err = e.net.Snapshot(); err != nil {
			return nil, fmt.Errorf("network snapshot: %w", err)
		}
	}
	if e.svc != nil {
		if previousSvc, err = e.svc.Snapshot(); err != nil {
			return nil, fmt.Errorf("services snapshot: %w", err)
		}
	}
	if e.shape != nil {
		if previousShape, err = e.shape.Snapshot(); err != nil {
			return nil, fmt.Errorf("shaping snapshot: %w", err)
		}
	}

	if err := e.nft.Apply(ctx, plan.Ruleset); err != nil {
		return nil, err
	}
	e.applySysctl()
	e.applyTimezone(ctx, cfg)
	e.applyJournal(ctx, cfg)
	e.applySSH(ctx, cfg)
	if e.net != nil {
		if err := e.net.Apply(ctx, plan.Network); err != nil {
			if rerr := e.nft.Apply(ctx, previous); rerr != nil {
				e.log.Error("network apply failed and firewall revert failed too", "networkErr", err, "err", rerr)
				return nil, fmt.Errorf("network apply failed (%w) and firewall revert failed (%w)", err, rerr)
			}
			return nil, fmt.Errorf("network apply failed, firewall reverted: %w", err)
		}
	}
	rollback := &pendingApply{
		previous: previous, previousNet: previousNet,
		previousSvc: previousSvc, previousShape: previousShape,
	}
	if e.svc != nil {
		if err := e.svc.Apply(ctx, plan.Services); err != nil {
			if rerr := e.restore(ctx, rollback); rerr != nil {
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
			if rerr := e.restore(ctx, rollback); rerr != nil {
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
			return nil, err
		}
		return &ApplyResult{Plan: *plan, Archived: archived}, nil
	}

	now := time.Now()
	p := &pendingApply{
		id:            newID(),
		cfg:           cfg,
		ruleset:       plan.Ruleset,
		previous:      previous,
		previousNet:   previousNet,
		previousSvc:   previousSvc,
		previousShape: previousShape,
		since:         now,
		deadline:      now.Add(opts.ConfirmTimeout),
	}
	id := p.id
	p.timer = time.AfterFunc(opts.ConfirmTimeout, func() { e.expire(id) })
	e.pending = p
	return &ApplyResult{Plan: *plan, Pending: true, Deadline: p.deadline}, nil
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
		// reboot loads the old one, which is the safer failure.
		return nil, fmt.Errorf("apply confirmed but not saved: %w", err)
	}
	e.log.Info("apply confirmed")
	return archived, nil
}

// Revert cancels the pending apply and restores the previous ruleset.
func (e *Engine) Revert(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.pending
	if p == nil {
		return ErrNothingPending
	}
	p.timer.Stop()
	e.pending = nil
	if err := e.restore(ctx, p); err != nil {
		return fmt.Errorf("revert: %w", err)
	}
	e.log.Info("apply reverted by request")
	return nil
}

// restore puts the previous firewall and network state back.
func (e *Engine) restore(ctx context.Context, p *pendingApply) error {
	var errs []error
	if err := e.nft.Apply(ctx, p.previous); err != nil {
		errs = append(errs, fmt.Errorf("firewall: %w", err))
	}
	if e.net != nil {
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
	return errors.Join(errs...)
}

func (e *Engine) expire(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.pending
	if p == nil || p.id != id {
		return
	}
	e.pending = nil
	ctx, cancel := context.WithTimeout(context.Background(), e.revert)
	defer cancel()
	if err := e.restore(ctx, p); err != nil {
		e.log.Error("automatic revert failed; system may still have the unconfirmed configuration", "err", err)
		return
	}
	e.log.Warn("apply not confirmed in time; reverted to previous ruleset",
		"window", p.deadline.Sub(p.since))
}

func (e *Engine) commit(cfg *model.Config, ruleset string) (*store.Revision, error) {
	unlock, err := e.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return e.store.Save(cfg, ruleset)
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

// Load applies the last confirmed ruleset without touching the store. It
// is the early-boot path.
func (e *Engine) Load(ctx context.Context) error {
	ruleset, err := e.store.LoadRuleset()
	if errors.Is(err, store.ErrNotFound) {
		return ErrNoRuleset
	}
	if err != nil {
		return err
	}
	if err := e.nft.Apply(ctx, ruleset); err != nil {
		return err
	}
	e.applySysctl()
	return nil
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
}

// Status reports whether configuration exists, whether the table is in the
// kernel, and any pending apply.
func (e *Engine) Status(ctx context.Context) (Status, error) {
	e.mu.Lock()
	var pending *PendingStatus
	if p := e.pending; p != nil {
		pending = &PendingStatus{Since: p.since, Deadline: p.deadline, Remaining: time.Until(p.deadline).Truncate(time.Second)}
	}
	e.mu.Unlock()

	st := Status{Configured: e.store.Exists(), Pending: pending, Network: "none"}
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
