package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/engine"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
)

// slowRunner loads rulesets like the fake, taking delay over each, the
// way a router's revert takes seconds.
type slowRunner struct {
	*nfttest.Fake
	mu    sync.Mutex
	delay time.Duration
}

func (s *slowRunner) Apply(ctx context.Context, rs string) error {
	s.mu.Lock()
	d := s.delay
	s.mu.Unlock()
	time.Sleep(d)
	return s.Fake.Apply(ctx, rs)
}

// At the deadline `ostiole apply` waits for the engine to go back before it
// returns, since the process exits right after. It used to return half a
// second after the deadline, which left a slow revert half done and its
// record behind.
func TestTheConfirmDeadlineWaitsForTheRevert(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	run := &slowRunner{Fake: &nfttest.Fake{}}
	eng := engine.New(st, run, nil, slog.New(slog.DiscardHandler))
	ctx := context.Background()
	router := func(hostname string) *model.Config {
		return model.Starter(model.StarterOptions{Hostname: hostname, LAN: "eth1", LANAddress: "192.0.2.1/24", WAN: "eth0"})
	}
	if _, err := eng.Apply(ctx, router("saved"), engine.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := eng.Apply(ctx, router("unconfirmed"), engine.ApplyOptions{ConfirmTimeout: 50 * time.Millisecond})
	if err != nil || !res.Pending {
		t.Fatalf("apply with a window: %+v, %v", res, err)
	}
	run.mu.Lock()
	run.delay = time.Second
	run.mu.Unlock()

	nobody, typing := io.Pipe()
	defer func() { _ = typing.Close() }()
	err = waitForConfirmation(ctx, eng, audit.ShellActor(), res.Deadline, nobody, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not confirmed in time; reverted") {
		t.Fatalf("err = %v", err)
	}
	applied := run.Applied()
	if len(applied) != 3 || applied[2] != applied[0] {
		t.Errorf("returned with %d rulesets loaded, want the saved one back as the third", len(applied))
	}
	if _, err := st.ReadState(store.PendingFile); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the record of the apply is still there (%v)", err)
	}
}
