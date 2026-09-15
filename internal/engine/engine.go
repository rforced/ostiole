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

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysctl"
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
	net    network.Backend // nil when network management is disabled
	sysctl sysctl.Applier  // nil in tests without a kernel
	log    *slog.Logger
	revert time.Duration // time budget for an automatic revert

	mu      sync.Mutex
	pending *pendingApply
}

type pendingApply struct {
	id          string
	cfg         *model.Config
	ruleset     string
	previous    string
	previousNet network.Files
	since       time.Time
	deadline    time.Time
	timer       *time.Timer
}

// New returns an engine over st and runner. net may be nil to leave
// network configuration alone (firewall only).
func New(st *store.Store, runner nft.Runner, net network.Backend, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{store: st, nft: runner, net: net, log: log, revert: 15 * time.Second}
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

// Plan is everything rendered from a configuration.
type Plan struct {
	Ruleset string        `json:"ruleset"`
	Network network.Files `json:"network,omitempty"`
}

// Store exposes the underlying store for read-only callers.
func (e *Engine) Store() *store.Store { return e.store }

// Check validates cfg, renders the ruleset and network units, and has nft
// dry-run the ruleset.
func (e *Engine) Check(ctx context.Context, cfg *model.Config) (*Plan, error) {
	ruleset, err := nft.Render(cfg)
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
	var previousNet network.Files
	if e.net != nil {
		if previousNet, err = e.net.Snapshot(); err != nil {
			return nil, fmt.Errorf("network snapshot: %w", err)
		}
	}

	if err := e.nft.Apply(ctx, plan.Ruleset); err != nil {
		return nil, err
	}
	e.applySysctl()
	if e.net != nil {
		if err := e.net.Apply(ctx, plan.Network); err != nil {
			if rerr := e.nft.Apply(ctx, previous); rerr != nil {
				e.log.Error("network apply failed and firewall revert failed too", "networkErr", err, "err", rerr)
				return nil, fmt.Errorf("network apply failed (%w) and firewall revert failed (%w)", err, rerr)
			}
			return nil, fmt.Errorf("network apply failed, firewall reverted: %w", err)
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
		id:          newID(),
		cfg:         cfg,
		ruleset:     plan.Ruleset,
		previous:    previous,
		previousNet: previousNet,
		since:       now,
		deadline:    now.Add(opts.ConfirmTimeout),
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

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
