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
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
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
	log    *slog.Logger
	revert time.Duration // time budget for an automatic revert

	mu      sync.Mutex
	pending *pendingApply
}

type pendingApply struct {
	id       string
	cfg      *model.Config
	ruleset  string
	previous string
	since    time.Time
	deadline time.Time
	timer    *time.Timer
}

// New returns an engine over st and runner.
func New(st *store.Store, runner nft.Runner, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{store: st, nft: runner, log: log, revert: 15 * time.Second}
}

// Store exposes the underlying store for read-only callers.
func (e *Engine) Store() *store.Store { return e.store }

// Check validates cfg, renders it, and has nft dry-run the result. It
// returns the rendered ruleset.
func (e *Engine) Check(ctx context.Context, cfg *model.Config) (string, error) {
	ruleset, err := nft.Render(cfg)
	if err != nil {
		return "", err
	}
	if err := e.nft.Check(ctx, ruleset); err != nil {
		return "", err
	}
	return ruleset, nil
}

// ApplyOptions tunes Apply.
type ApplyOptions struct {
	// ConfirmTimeout is how long to wait for Confirm before reverting.
	// Zero commits immediately with no safety window.
	ConfirmTimeout time.Duration
}

// ApplyResult describes what Apply did.
type ApplyResult struct {
	Ruleset  string          `json:"ruleset"`
	Pending  bool            `json:"pending"`
	Deadline time.Time       `json:"deadline,omitempty"`
	Archived *store.Revision `json:"archived,omitempty"`
}

// Apply loads cfg into the kernel. With a confirm timeout the change stays
// provisional until Confirm; otherwise it is committed to the store at once.
func (e *Engine) Apply(ctx context.Context, cfg *model.Config, opts ApplyOptions) (*ApplyResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil {
		return nil, ErrPending
	}

	ruleset, err := e.Check(ctx, cfg)
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

	if err := e.nft.Apply(ctx, ruleset); err != nil {
		return nil, err
	}
	e.log.Info("ruleset applied", "rules", len(cfg.Rules), "confirmTimeout", opts.ConfirmTimeout)

	if opts.ConfirmTimeout <= 0 {
		archived, err := e.commit(cfg, ruleset)
		if err != nil {
			return nil, err
		}
		return &ApplyResult{Ruleset: ruleset, Archived: archived}, nil
	}

	now := time.Now()
	p := &pendingApply{
		id:       newID(),
		cfg:      cfg,
		ruleset:  ruleset,
		previous: previous,
		since:    now,
		deadline: now.Add(opts.ConfirmTimeout),
	}
	id := p.id
	p.timer = time.AfterFunc(opts.ConfirmTimeout, func() { e.expire(id) })
	e.pending = p
	return &ApplyResult{Ruleset: ruleset, Pending: true, Deadline: p.deadline}, nil
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
	if err := e.nft.Apply(ctx, p.previous); err != nil {
		return fmt.Errorf("revert: %w", err)
	}
	e.log.Info("apply reverted by request")
	return nil
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
	if err := e.nft.Apply(ctx, p.previous); err != nil {
		e.log.Error("automatic revert failed; kernel still has the unconfirmed ruleset", "err", err)
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
	return e.nft.Apply(ctx, ruleset)
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

	st := Status{Configured: e.store.Exists(), Pending: pending}
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
