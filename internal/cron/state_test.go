package cron

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// errNope is how a cron fails, for the tests that care that it did.
var errNope = errors.New("the bucket refused it")

// The results live in memory, so before this they died with the process.
// A router that restarts five times in a day reported a nightly check it
// had run at four in the morning as never having happened.
func TestResultsSurviveARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config(model.Cron{ID: "nightly", Enabled: true, Schedule: "0 3 * * *", Kind: model.CronBackup})
	source := func() *model.Config { return cfg }
	ex := &fakeExec{}

	first := NewRunner(source, ex, slog.New(slog.DiscardHandler), dir)
	first.Tick(context.Background(), at(t, "2026-09-16 03:00"))
	waitForSave(t, dir, "nightly")
	if len(ex.calls()) != 1 {
		t.Fatalf("ran %v, want the one cron", ex.calls())
	}
	before := statusOf(first, "nightly")
	if before.LastRun == nil {
		t.Fatal("the run was not recorded at all")
	}

	// The daemon restarts: a new runner over the same directory.
	second := NewRunner(source, &fakeExec{}, slog.New(slog.DiscardHandler), dir)
	after := statusOf(second, "nightly")
	if after.LastRun == nil {
		t.Fatal("the nightly run was forgotten, so the page says it never happened")
	}
	if !after.LastRun.Equal(*before.LastRun) {
		t.Errorf("last run = %v, want %v", after.LastRun, before.LastRun)
	}
	if after.LastOutput != before.LastOutput {
		t.Errorf("output = %q, want %q", after.LastOutput, before.LastOutput)
	}
	// Whatever was going when the daemon stopped is not going now.
	if after.Running {
		t.Error("a cron came back from disk still running")
	}
}

// A failure is worth keeping too: "it ran and broke" and "it never ran"
// are the two answers this page exists to tell apart.
func TestFailureSurvivesARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config(model.Cron{ID: "nightly", Enabled: true, Schedule: "0 3 * * *", Kind: model.CronBackup})
	source := func() *model.Config { return cfg }

	first := NewRunner(source, &fakeExec{err: errNope}, slog.New(slog.DiscardHandler), dir)
	first.Tick(context.Background(), at(t, "2026-09-16 03:00"))
	waitForSave(t, dir, "nightly")

	second := NewRunner(source, &fakeExec{}, slog.New(slog.DiscardHandler), dir)
	if got := statusOf(second, "nightly").LastError; !strings.Contains(got, errNope.Error()) {
		t.Errorf("last error = %q, want the failure that was recorded", got)
	}
}

// The gateway probe notes itself every five seconds. Writing the file on
// every note would be thousands of writes a day for a timestamp nobody
// needs to the second, so a note waits for the tick.
func TestNoteIsWrittenOnTheTickRatherThanEveryNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := NewRunner(func() *model.Config { return config() }, &fakeExec{}, slog.New(slog.DiscardHandler), dir)

	r.Note("system:gateways")
	if _, err := os.Stat(filepath.Join(dir, "crons.json")); !os.IsNotExist(err) {
		t.Error("a note wrote the file on its own")
	}

	r.flush()
	if _, err := os.Stat(filepath.Join(dir, "crons.json")); err != nil {
		t.Fatalf("the flush did not write the file: %v", err)
	}
	// And it comes back, which is what keeps the hourly drive check from
	// reading as never having run for an hour after a restart.
	second := NewRunner(func() *model.Config { return config() }, &fakeExec{}, slog.New(slog.DiscardHandler), dir)
	if statusOf(second, "system:gateways").LastRun == nil {
		t.Error("the noted work was forgotten")
	}
}

// A second flush with nothing new does not rewrite the file.
func TestFlushOnlyWritesWhenSomethingHappened(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := NewRunner(func() *model.Config { return config() }, &fakeExec{}, slog.New(slog.DiscardHandler), dir)
	r.Note("system:gateways")
	r.flush()

	path := filepath.Join(dir, "crons.json")
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// A modification time has to move to be seen to move.
	time.Sleep(10 * time.Millisecond)
	r.flush()
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Error("a flush with nothing new rewrote the file")
	}
}

// A cron an operator deleted should not keep its results on disk for
// ever, output and all.
func TestDeletedCronsAreDroppedFromTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config(model.Cron{ID: "nightly", Enabled: true, Schedule: "0 3 * * *", Kind: model.CronBackup})
	source := func() *model.Config { return cfg }
	ex := &fakeExec{}

	// RunNow rather than Tick: it runs and records in this goroutine, so the
	// deletion below cannot land while a background run is still reading the
	// configuration, and no late save can put the cron back in the file.
	r := NewRunner(source, ex, slog.New(slog.DiscardHandler), dir)
	if err := r.RunNow(context.Background(), "nightly"); err != nil {
		t.Fatal(err)
	}
	if len(ex.calls()) != 1 {
		t.Fatalf("ran %v, want the one cron", ex.calls())
	}

	// The operator removes it and something else writes the file out.
	cfg.Crons = nil
	r.Note("system:gateways")
	r.flush()

	raw, err := os.ReadFile(filepath.Join(dir, "crons.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "nightly") {
		t.Errorf("the deleted cron is still on disk: %s", raw)
	}
}

// A binary with nowhere to write still runs its crons; it just forgets
// them, the way it did before any of this.
func TestNoDirectoryRemembersNothingAndDoesNotPanic(t *testing.T) {
	t.Parallel()
	cfg := config(model.Cron{ID: "nightly", Enabled: true, Schedule: "0 3 * * *", Kind: model.CronBackup})
	ex := &fakeExec{}
	r := NewRunner(func() *model.Config { return cfg }, ex, slog.New(slog.DiscardHandler), "")
	r.Tick(context.Background(), at(t, "2026-09-16 03:00"))
	waitFor(t, func() bool { return len(ex.calls()) == 1 })
	r.Note("system:gateways")
	r.flush()
	if statusOf(r, "nightly").LastRun == nil {
		t.Error("the run was not reported in memory")
	}
}

// Nonsense in the file is not worth refusing to start over.
func TestUnreadableFileStartsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "crons.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(func() *model.Config { return config() }, &fakeExec{}, slog.New(slog.DiscardHandler), dir)
	if statusOf(r, "system:gateways").LastRun != nil {
		t.Error("something was read out of a broken file")
	}
}
