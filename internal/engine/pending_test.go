package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/store"
)

// A daemon that dies inside the confirmation window starts again with no
// memory of the apply. The record it wrote has the next start put the
// firewall and the network back as they were before.
func TestRecoverUndoesAnApplyTheLastRunLeftUnconfirmed(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	fn := &fakeNet{files: network.Files{"00-ostiole-eth1.network": "before"}}
	first := New(st, fr, fn, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	saved, err := first.Apply(ctx, cfg("saved"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before := fn.current()
	if _, err := first.Apply(ctx, cfg("unconfirmed"), ApplyOptions{ConfirmTimeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	first.pending.timer.Stop() // the process is gone; so is its timer

	next := New(st, fr, fn, slog.New(slog.DiscardHandler))
	undone, err := next.Recover(ctx)
	if err != nil || !undone {
		t.Fatalf("Recover = %v, %v", undone, err)
	}
	if fr.last() != saved.Ruleset {
		t.Error("the firewall did not go back to the confirmed ruleset")
	}
	if !reflect.DeepEqual(fn.current(), before) {
		t.Errorf("network after recovery = %v, want %v", fn.current(), before)
	}
	if _, err := st.ReadState(store.PendingFile); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the record outlived the recovery: %v", err)
	}
	if status, _ := next.Status(ctx); status.Recovered == nil {
		t.Error("the dashboard is not told an apply was undone")
	}
	if again, err := next.Recover(ctx); err != nil || again {
		t.Errorf("a second Recover = %v, %v; want nothing to do", again, err)
	}
}

// A record left behind by an apply that was committed has nothing to undo.
func TestRecoverLeavesACommittedApplyAlone(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	// Committed, then killed before the record went.
	saved, _ := st.Load()
	raw, _ := json.Marshal(record{ID: "x", PID: os.Getpid(), Config: digest(saved)})
	if err := st.WriteState(store.PendingFile, raw); err != nil {
		t.Fatal(err)
	}
	applied := fr.count()
	if undone, err := e.Recover(ctx); err != nil || undone {
		t.Fatalf("Recover = %v, %v", undone, err)
	}
	if fr.count() != applied {
		t.Error("recovery touched the kernel for a committed apply")
	}
	if _, err := st.ReadState(store.PendingFile); !errors.Is(err, store.ErrNotFound) {
		t.Error("the stale record was not removed")
	}
}

// `ostiole apply` at a terminal writes the same record. The daemon starting
// meanwhile leaves it alone, and neither side applies over the other.
func TestAnApplyInAnotherProcessIsLeftAlone(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)
	e.alive = func(int) bool { return true }
	ctx := context.Background()
	raw, _ := json.Marshal(record{ID: "x", PID: os.Getpid() + 1, Boot: bootID(), Config: "other"})
	if err := st.WriteState(store.PendingFile, raw); err != nil {
		t.Fatal(err)
	}
	if undone, err := e.Recover(ctx); err != nil || undone {
		t.Fatalf("Recover = %v, %v", undone, err)
	}
	if _, err := e.Apply(ctx, cfg("x"), ApplyOptions{}); !errors.Is(err, ErrPending) {
		t.Fatalf("apply over another process's apply: %v", err)
	}
	if fr.count() != 0 {
		t.Error("something was applied")
	}
	// Once that process is gone, its apply is only a leftover.
	e.alive = func(int) bool { return false }
	if _, err := e.Apply(ctx, cfg("x"), ApplyOptions{}); err != nil {
		t.Fatalf("apply after the other process died: %v", err)
	}
}

// A caller that goes away mid-apply, like a browser tab closed while the
// request runs, does not stop the apply halfway.
func TestApplyOutlivesItsCaller(t *testing.T) {
	t.Parallel()
	e, fr, fn := newEngineWithNet(t)
	ctx, cancel := context.WithCancel(context.Background())
	fr.onCheck = cancel
	res, err := e.Apply(ctx, cfg("x"), ApplyOptions{ConfirmTimeout: time.Minute})
	if err != nil {
		t.Fatalf("apply whose caller left: %v", err)
	}
	if fr.last() != res.Ruleset || len(fn.current()) == 0 {
		t.Error("the apply stopped when its caller did")
	}
	// Nor does a revert whose caller has already gone.
	if err := e.Revert(ctx); err != nil {
		t.Fatalf("revert whose caller left: %v", err)
	}
}
