package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
)

// An installed router runs the bootstrap ruleset (policy drop, the
// management ports open) with no configuration saved yet. The first
// apply's revert must put that back, not delete the table: a router with
// no table at all is wide open.
func TestFirstApplyRevertsToTheBootstrapRuleset(t *testing.T) {
	t.Parallel()
	e, fr, st := newEngine(t)
	boot := nft.Bootstrap([]uint16{443, 22})
	if err := os.MkdirAll(st.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, store.RulesetFile), []byte(boot), 0o644); err != nil {
		t.Fatal(err)
	}
	if st.Exists() {
		t.Fatal("a bootstrap ruleset alone must not count as a configuration")
	}
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("first"), ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := e.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	// The bootstrap starts with the same delete-and-recreate as every
	// ruleset; what matters is that a table with a drop policy follows.
	if got := fr.last(); got != boot {
		t.Errorf("revert applied:\n%s\nwant the bootstrap ruleset:\n%s", got, boot)
	}
	if !strings.Contains(fr.last(), "policy drop") {
		t.Error("the revert left no drop policy in place")
	}
}

// Confirm that cannot save: the kernel keeps the new ruleset, the store
// keeps the old, and the error says so. Nothing stays pending, so a
// reboot loads the old configuration, which is the safer failure.
func TestConfirmThatCannotSaveSaysSo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only directory")
	}
	t.Parallel()
	e, fr, st := newEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, cfg("unsaved"), ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	applied := fr.last()
	if err := os.Chmod(st.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(st.Dir, 0o755) })
	_, err := e.Confirm(ctx)
	if err == nil || !strings.Contains(err.Error(), "not saved") {
		t.Fatalf("confirm on a read-only store: err = %v", err)
	}
	if fr.last() != applied {
		t.Error("the kernel ruleset changed on a failed confirm")
	}
	status, _ := e.Status(ctx)
	if status.Pending != nil {
		t.Errorf("still pending after a failed confirm: %+v", status.Pending)
	}
	_ = os.Chmod(st.Dir, 0o755)
	if saved, err := st.Load(); err != nil || saved.System.Hostname != "saved" {
		t.Errorf("store after a failed confirm = %v, %v", saved, err)
	}
}

// A revert whose restore fails leaves the unconfirmed ruleset in the
// kernel and says so; nothing stays pending to retry, so the operator's
// next move is another apply.
func TestRevertThatCannotRestoreSaysSo(t *testing.T) {
	t.Parallel()
	e, fr, _ := newEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, cfg("unconfirmed"), ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	applied := fr.last()
	fr.mu.Lock()
	fr.applyErr = errors.New("nft: kernel says no")
	fr.mu.Unlock()
	err := e.Revert(ctx)
	if err == nil || !strings.Contains(err.Error(), "revert:") || !strings.Contains(err.Error(), "kernel says no") {
		t.Fatalf("revert with a failing restore: err = %v", err)
	}
	if fr.last() != applied {
		t.Error("something else was applied after the failed restore")
	}
	status, _ := e.Status(ctx)
	if status.Pending != nil {
		t.Errorf("still pending after a failed revert: %+v", status.Pending)
	}
	if _, err := e.Confirm(ctx); !errors.Is(err, ErrNothingPending) {
		t.Errorf("confirm after a failed revert: %v", err)
	}
}
