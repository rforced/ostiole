package engine

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/network"
	"ostiole/internal/store"
)

var (
	tester = audit.Actor{Name: "tester", Kind: audit.Account, Role: "admin"}
	alice  = audit.Actor{Name: "alice", Kind: audit.Account, Role: "operator", Address: "192.0.2.5"}
	bob    = audit.Actor{Name: "bob", Kind: audit.Token, Role: "operator", Address: "192.0.2.6"}
)

func auditedEngine(t *testing.T) (*Engine, *store.Store, *audit.Log) {
	t.Helper()
	e, _, st := newEngine(t)
	l := audit.Open(st.Dir)
	return e.WithAudit(l), st, l
}

func actions(t *testing.T, l *audit.Log) []audit.Event {
	t.Helper()
	events, err := l.Events()
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestAnApplyIsSavedAsMadeByWhoAsked(t *testing.T) {
	t.Parallel()
	e, st, l := auditedEngine(t)
	if _, err := e.Apply(context.Background(), cfg("first"), ApplyOptions{By: alice}); err != nil {
		t.Fatal(err)
	}
	applied, err := st.Applied()
	if err != nil || applied == nil || applied.By != alice || applied.ConfirmedBy != nil {
		t.Fatalf("Applied = %+v, %v", applied, err)
	}
	got := actions(t, l)
	if len(got) != 1 || got[0].Action != audit.Apply || got[0].By != alice || got[0].Detail != "" {
		t.Errorf("recorded %+v", got)
	}
}

func TestAConfirmBySomeoneElseIsKeptWithTheApply(t *testing.T) {
	t.Parallel()
	e, st, l := auditedEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("second"), ApplyOptions{ConfirmTimeout: 2 * time.Minute, By: alice}); err != nil {
		t.Fatal(err)
	}
	if applied, err := st.Applied(); err != nil || applied != nil {
		t.Fatalf("saved before the confirm: %+v, %v", applied, err)
	}
	if _, err := e.Confirm(ctx, bob); err != nil {
		t.Fatal(err)
	}
	applied, err := st.Applied()
	if err != nil || applied == nil || applied.By != alice || applied.ConfirmedBy == nil || *applied.ConfirmedBy != bob {
		t.Fatalf("Applied = %+v, %v", applied, err)
	}
	got := actions(t, l)
	if len(got) != 2 {
		t.Fatalf("recorded %+v", got)
	}
	if got[0].Action != audit.Apply || got[0].By != alice || got[0].Detail != "120" {
		t.Errorf("apply recorded as %+v", got[0])
	}
	if got[1].Action != audit.Confirm || got[1].By != bob || got[1].Target != "alice" {
		t.Errorf("confirm recorded as %+v", got[1])
	}
}

func TestAConfirmByWhoAppliedNamesNoOtherConfirmer(t *testing.T) {
	t.Parallel()
	e, st, _ := auditedEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("second"), ApplyOptions{ConfirmTimeout: time.Minute, By: alice}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Confirm(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if applied, err := st.Applied(); err != nil || applied == nil || applied.ConfirmedBy != nil {
		t.Fatalf("Applied = %+v, %v", applied, err)
	}
}

func TestRevertsAndExpiriesAreRecorded(t *testing.T) {
	t.Parallel()
	e, _, l := auditedEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("first"), ApplyOptions{ConfirmTimeout: time.Minute, By: alice}); err != nil {
		t.Fatal(err)
	}
	if err := e.Revert(ctx, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, cfg("second"), ApplyOptions{ConfirmTimeout: 30 * time.Millisecond, By: alice}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(actions(t, l)) < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := actions(t, l)
	if len(got) != 4 {
		t.Fatalf("recorded %+v", got)
	}
	if got[1].Action != audit.Revert || got[1].By != bob || got[1].Target != "alice" {
		t.Errorf("revert recorded as %+v", got[1])
	}
	if got[3].Action != audit.Expire || got[3].By != (audit.Actor{}) || got[3].Target != "alice" {
		t.Errorf("expiry recorded as %+v", got[3])
	}
}

func TestRecoverNamesWhoseApplyItUndid(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	fn := &fakeNet{files: network.Files{}}
	first := New(st, fr, fn, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	if _, err := first.Apply(ctx, cfg("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Apply(ctx, cfg("unconfirmed"), ApplyOptions{ConfirmTimeout: time.Hour, By: alice}); err != nil {
		t.Fatal(err)
	}
	first.pending.timer.Stop()

	l := audit.Open(st.Dir)
	next := New(st, fr, fn, slog.New(slog.DiscardHandler)).WithAudit(l)
	if undone, err := next.Recover(ctx); err != nil || !undone {
		t.Fatalf("Recover = %v, %v", undone, err)
	}
	got := actions(t, l)
	if len(got) != 1 || got[0].Action != audit.Recover || got[0].Target != "alice" || got[0].By != (audit.Actor{}) {
		t.Errorf("recorded %+v", got)
	}
}
