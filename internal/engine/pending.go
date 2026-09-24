package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/store"
)

// record is written before an apply changes anything and removed once it
// is committed or undone. The pending state lives in memory, so without it
// a daemon that died inside the confirmation window, or a router that
// rebooted in it, would keep a configuration nobody confirmed. It holds
// the network, services and shaping files from before the apply; the
// firewall's half is the saved ruleset, which only a commit replaces.
type record struct {
	ID string `json:"id"`
	// PID and Boot say whose apply it is. One whose process is gone, or
	// that an earlier boot wrote, was left unfinished.
	PID   int       `json:"pid"`
	Boot  string    `json:"boot"`
	Since time.Time `json:"since"`
	// Config is the SHA-256 of the configuration being applied. Finding it
	// saved means the apply was committed and only the record was left
	// behind. Not the ruleset: many changes leave that as it was.
	Config string `json:"config"`
	// The files before the apply. Null and empty differ: null is a backend
	// the engine that wrote this did not drive, empty is one with no files.
	Network  network.Files `json:"network"`
	Services network.Files `json:"services"`
	Shaping  network.Files `json:"shaping"`
}

// Recovered describes an apply that Recover undid.
type Recovered struct {
	// Since is when the unfinished apply started, At when it was undone.
	Since time.Time `json:"since"`
	At    time.Time `json:"at"`
}

// digest identifies a configuration. JSON is what the store keeps, and the
// model round-trips through it unchanged.
func digest(cfg *model.Config) string {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func bootID() string {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// ostioleRunning reports whether pid is a live Ostiole process. The name
// check keeps a recycled PID from holding a record hostage.
func ostioleRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	comm, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(comm)), "ostiole")
}

func (e *Engine) readRecord() (*record, error) {
	raw, err := e.store.ReadState(store.PendingFile)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		// Written atomically, so this is damage from outside, and there is
		// nothing in it to put back.
		e.log.Warn("the record of an unfinished apply is unreadable; dropping it", "err", err)
		e.clearRecord()
		return nil, nil
	}
	return &r, nil
}

func (e *Engine) writeRecord(r *record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return e.store.WriteState(store.PendingFile, raw)
}

func (e *Engine) clearRecord() {
	if err := e.store.RemoveState(store.PendingFile); err != nil {
		e.log.Warn("could not remove the record of the last apply; the next start looks at it again", "err", err)
	}
}

// keep puts back the record an apply inherited, or removes the one it
// wrote, when it stops before changing anything.
func (e *Engine) keep(base *record) {
	if base == nil {
		e.clearRecord()
		return
	}
	if err := e.writeRecord(base); err != nil {
		e.log.Warn("could not keep the record of an earlier unfinished apply", "err", err)
	}
}

// elsewhere reports whether the record belongs to another Ostiole process
// that is still running, such as `ostiole apply` waiting at a terminal.
func (e *Engine) elsewhere(r *record) bool {
	return r.Boot == bootID() && r.PID != os.Getpid() && e.alive(r.PID)
}

// leftover returns the record an earlier apply left unfinished. It was
// never confirmed, so what it holds as "before" is still the last
// confirmed state, and the next apply keeps it as its own. An apply
// another process is still making is ErrPending.
func (e *Engine) leftover() (*record, error) {
	r, err := e.readRecord()
	if err != nil || r == nil {
		return nil, err
	}
	if e.elsewhere(r) {
		return nil, fmt.Errorf("%w (ostiole process %d is applying)", ErrPending, r.PID)
	}
	return r, nil
}

// snapshot fills in the "before" of every backend this engine drives that
// the record does not already carry.
func (e *Engine) snapshot(r *record) error {
	take := func(b network.Backend, into *network.Files, what string) error {
		if b == nil || *into != nil {
			return nil
		}
		files, err := b.Snapshot()
		if err != nil {
			return fmt.Errorf("%s snapshot: %w", what, err)
		}
		if files == nil {
			files = network.Files{}
		}
		*into = files
		return nil
	}
	if err := take(e.net, &r.Network, "network"); err != nil {
		return err
	}
	if err := take(e.svc, &r.Services, "services"); err != nil {
		return err
	}
	return take(e.shape, &r.Shaping, "shaping")
}

// Recover undoes an apply that a previous run left unfinished: one being
// made when the process died, or one waiting for confirmation when the
// process died or the router rebooted. Either way nobody confirmed it. The
// daemon calls this before anything reads the configuration; it leaves
// alone an apply another running process is making, and reports whether
// it put anything back.
func (e *Engine) Recover(ctx context.Context) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil {
		return false, nil
	}
	r, err := e.readRecord()
	if err != nil || r == nil || e.elsewhere(r) {
		return false, err
	}
	if saved, err := e.store.Load(); err == nil && digest(saved) == r.Config {
		// Committed; only the record was left behind.
		e.clearRecord()
		return false, nil
	}
	previous, err := e.previousRuleset()
	if err != nil {
		return false, err
	}
	p := &pendingApply{previous: previous, previousNet: r.Network, previousSvc: r.Services, previousShape: r.Shaping}
	if err := e.undo(ctx, p); err != nil {
		return false, fmt.Errorf("undo the apply started %s: %w", r.Since.Format(time.RFC3339), err)
	}
	e.recovered = &Recovered{Since: r.Since, At: time.Now()}
	e.log.Warn("undid an apply that was never confirmed; the process making it stopped or the router restarted",
		"started", r.Since)
	return true, nil
}

// undo puts back what p recorded, on a context of its own: the one the
// apply ran on may be what ran out. The record goes once it has worked.
func (e *Engine) undo(ctx context.Context, p *pendingApply) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.revert)
	defer cancel()
	if err := e.restore(ctx, p); err != nil {
		return err
	}
	e.clearRecord()
	return nil
}
