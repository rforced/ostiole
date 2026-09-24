package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysctl"
)

// hostCalls is what the engine asked of the router's own settings, and of
// the firewall, in the order it asked.
type hostCalls struct {
	mu    sync.Mutex
	calls []string
}

func (h *hostCalls) add(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, fmt.Sprintf(format, args...))
}

// take returns the calls so far and starts a new list.
func (h *hostCalls) take() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.calls
	h.calls = nil
	return out
}

type zoneFake struct{ *hostCalls }

func (f zoneFake) Apply(_ context.Context, zone string) error { f.add("zone %s", zone); return nil }

type journalFake struct{ *hostCalls }

func (f journalFake) Apply(_ context.Context, gb, days int) error {
	f.add("journal %dG %dd", gb, days)
	return nil
}

type loggingFake struct{ *hostCalls }

func (f loggingFake) Apply(_ context.Context, level model.LogLevel) error {
	f.add("logging %s", level)
	return nil
}

type sshFake struct{ *hostCalls }

func (f sshFake) Apply(_ context.Context, passwords bool) error {
	f.add("ssh passwords=%v", passwords)
	return nil
}

// kernelFake keeps a conntrack ceiling the way the kernel does: it changes
// only when something writes one.
type kernelFake struct {
	*hostCalls
	mu  sync.Mutex
	now sysctl.Settings
}

func (f *kernelFake) Apply(s sysctl.Settings) error {
	f.add("conntrack %d/%d", s.ConntrackMax, s.ConntrackBuckets)
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.ConntrackMax > 0 {
		if s.ConntrackBuckets == 0 {
			s.ConntrackBuckets = s.ConntrackMax / 4
		}
		f.now = s
	}
	return nil
}

func (f *kernelFake) Current() (sysctl.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now, nil
}

// kernelOwn is the ceiling the kernel picked for itself.
var kernelOwn = sysctl.Settings{ConntrackMax: 65536, ConntrackBuckets: 65536}

func hostEngine(t *testing.T, st *store.Store, calls *hostCalls, kernel *kernelFake) (*Engine, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{onApply: func() { calls.add("nft") }}
	e := New(st, fr, nil, slog.New(slog.DiscardHandler)).
		WithSysctl(kernel).WithTimezone(zoneFake{calls}).WithJournal(journalFake{calls}).
		WithLogging(loggingFake{calls}).WithSSH(sshFake{calls})
	return e, fr
}

// unconfirmed changes every one of the router's own settings.
func unconfirmed() *model.Config {
	c := cfg("unconfirmed")
	c.System.Timezone = "Europe/Berlin"
	c.System.Management.SSHPasswords = false
	c.System.Logging = model.Logging{Level: model.LogDebug, MaxUseGB: 2, RetentionDays: 7}
	c.System.ConntrackMax = 1_000_000
	return c
}

// The settings that are not the firewall's follow a revert and an expiry
// like the firewall does. Before, sshd kept a password setting the store
// said the opposite of until the next apply.
func TestUndoPutsTheRoutersOwnSettingsBack(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"revert", "expiry"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			calls := &hostCalls{}
			kernel := &kernelFake{hostCalls: calls, now: kernelOwn}
			e, _ := hostEngine(t, store.New(t.TempDir()), calls, kernel)
			ctx := context.Background()
			saved := cfg("saved")
			saved.System.Management.SSHPasswords = true
			if _, err := e.Apply(ctx, saved, ApplyOptions{}); err != nil {
				t.Fatal(err)
			}
			calls.take()

			window := time.Hour
			if how == "expiry" {
				window = 20 * time.Millisecond
			}
			if _, err := e.Apply(ctx, unconfirmed(), ApplyOptions{ConfirmTimeout: window}); err != nil {
				t.Fatal(err)
			}
			if how == "revert" {
				if err := e.Revert(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				waitFor(t, func() bool { st, _ := e.Status(ctx); return st.Pending == nil })
			}
			// The firewall first each way, so nothing slow holds it up.
			want := []string{
				"nft", "conntrack 1000000/0", "zone Europe/Berlin", "journal 2G 7d", "logging debug", "ssh passwords=false",
				"nft", "conntrack 65536/65536", "zone UTC", "journal 10G 90d", "logging warning", "ssh passwords=true",
			}
			if got := calls.take(); !reflect.DeepEqual(got, want) {
				t.Errorf("apply and %s asked for %q, want %q", how, got, want)
			}
			if now, _ := kernel.Current(); now != kernelOwn {
				t.Errorf("conntrack after the %s = %+v, want the kernel's own %+v", how, now, kernelOwn)
			}
		})
	}
}

// Before the first confirmed apply there is no setting to go back to: the
// defaults, and sshd as the host has it, without Ostiole's drop-in.
func TestUndoingTheFirstApplyLeavesTheDefaults(t *testing.T) {
	t.Parallel()
	calls := &hostCalls{}
	e, _ := hostEngine(t, store.New(t.TempDir()), calls, &kernelFake{hostCalls: calls, now: kernelOwn})
	ctx := context.Background()
	if _, err := e.Apply(ctx, unconfirmed(), ApplyOptions{ConfirmTimeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	calls.take()
	if err := e.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"nft", "conntrack 65536/65536", "zone UTC", "journal 10G 90d",
		"logging warning", "ssh passwords=true"}
	if got := calls.take(); !reflect.DeepEqual(got, want) {
		t.Errorf("revert asked for %q, want %q", got, want)
	}
}

// A daemon that died in the window leaves only the record. The next start
// puts the router's own settings back with the rest, the ceiling from the
// figure the record kept.
func TestRecoverPutsTheRoutersOwnSettingsBack(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	calls := &hostCalls{}
	kernel := &kernelFake{hostCalls: calls, now: kernelOwn}
	first, _ := hostEngine(t, st, calls, kernel)
	ctx := context.Background()
	saved := cfg("saved")
	saved.System.Management.SSHPasswords = true
	if _, err := first.Apply(ctx, saved, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Apply(ctx, unconfirmed(), ApplyOptions{ConfirmTimeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	first.pending.timer.Stop()
	calls.take()

	next, _ := hostEngine(t, st, calls, kernel)
	if undone, err := next.Recover(ctx); err != nil || !undone {
		t.Fatalf("Recover = %v, %v", undone, err)
	}
	want := []string{"nft", "conntrack 65536/65536", "zone UTC", "journal 10G 90d",
		"logging warning", "ssh passwords=true"}
	if got := calls.take(); !reflect.DeepEqual(got, want) {
		t.Errorf("recovery asked for %q, want %q", got, want)
	}
}

// With the confirmed configuration unreadable there is nothing to say what
// the settings were, and defaults would let passwords back into sshd, so
// they are left as they are.
func TestUndoLeavesTheSettingsWhenTheStoreIsUnreadable(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	calls := &hostCalls{}
	e, _ := hostEngine(t, st, calls, &kernelFake{hostCalls: calls, now: kernelOwn})
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(ctx, unconfirmed(), ApplyOptions{ConfirmTimeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	calls.take()
	if err := os.WriteFile(filepath.Join(st.Dir, store.ConfigFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	if got := calls.take(); !reflect.DeepEqual(got, []string{"nft"}) {
		t.Errorf("revert with an unreadable store asked for %q, want the firewall only", got)
	}
}

// A ruleset nft refuses changes nothing, the router's own settings
// included.
func TestARefusedRulesetChangesNothingElse(t *testing.T) {
	t.Parallel()
	calls := &hostCalls{}
	e, fr := hostEngine(t, store.New(t.TempDir()), calls, &kernelFake{hostCalls: calls, now: kernelOwn})
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	calls.take()
	fr.applyErr = errors.New("nft: kernel refuses it")
	if _, err := e.Apply(ctx, unconfirmed(), ApplyOptions{}); err == nil {
		t.Fatal("a refused ruleset applied")
	}
	if got, want := calls.take(), []string{"nft"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a refused apply asked for %q, want %q", got, want)
	}
}

// The record carries the ceiling from before the apply, so a restart
// between the two still knows it.
func TestTheRecordKeepsTheCeilingFromBefore(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	calls := &hostCalls{}
	e, _ := hostEngine(t, st, calls, &kernelFake{hostCalls: calls, now: kernelOwn})
	if _, err := e.Apply(context.Background(), unconfirmed(), ApplyOptions{ConfirmTimeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	e.pending.timer.Stop()
	raw, err := st.ReadState(store.PendingFile)
	if err != nil {
		t.Fatal(err)
	}
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Kernel == nil || *rec.Kernel != kernelOwn {
		t.Errorf("record kept %+v, want %+v", rec.Kernel, kernelOwn)
	}
}

// waitFor polls until ok holds, for what happens on a timer.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type sshRefuses struct{}

func (sshRefuses) Apply(_ context.Context, passwords bool) error {
	if passwords {
		return nil
	}
	return errors.New("sshd still accepts passwords")
}

// A password setting sshd does not follow is a security setting that is
// not in force, so the status says so until an apply that sshd follows.
func TestStatusSaysWhenSSHDoesNotFollow(t *testing.T) {
	t.Parallel()
	e := New(store.New(t.TempDir()), &fakeRunner{}, nil, slog.New(slog.DiscardHandler)).WithSSH(sshRefuses{})
	ctx := context.Background()
	if _, err := e.Apply(ctx, unconfirmed(), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx); st.SSH != "sshd still accepts passwords" {
		t.Errorf("status SSH = %q", st.SSH)
	}
	allowed := cfg("passwords")
	allowed.System.Management.SSHPasswords = true
	if _, err := e.Apply(ctx, allowed, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Status(ctx); st.SSH != "" {
		t.Errorf("status SSH after a followed apply = %q", st.SSH)
	}
}
