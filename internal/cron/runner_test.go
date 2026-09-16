package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/model"
)

func config(jobs ...model.Cron) *model.Config {
	cfg := model.Starter(model.StarterOptions{
		Hostname: "gateway", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0",
	})
	cfg.Crons = jobs
	return cfg
}

type fakeExec struct {
	mu    sync.Mutex
	ran   []string
	err   error
	block chan struct{}
}

func (f *fakeExec) Run(_ context.Context, job model.Cron) (string, error) {
	f.mu.Lock()
	f.ran = append(f.ran, job.ID)
	block, err := f.block, f.err
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return "output of " + job.ID, err
}

func (f *fakeExec) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ran...)
}

func runner(t *testing.T, ex Executor, jobs ...model.Cron) *Runner {
	t.Helper()
	cfg := config(jobs...)
	return NewRunner(func() *model.Config { return cfg }, ex, slog.New(slog.DiscardHandler))
}

func TestTickRunsWhatIsDue(t *testing.T) {
	t.Parallel()
	ex := &fakeExec{}
	r := runner(t, ex,
		model.Cron{ID: "nightly", Enabled: true, Schedule: "0 4 * * *", Job: model.CronBackup},
		model.Cron{ID: "hourly", Enabled: true, Schedule: "@hourly", Job: model.CronRefreshAliases},
		model.Cron{ID: "off", Enabled: false, Schedule: "* * * * *", Job: model.CronRefreshAliases},
	)

	// Nothing is due at half past one.
	r.Tick(context.Background(), at(t, "2026-09-16 01:30"))
	if got := ex.calls(); len(got) != 0 {
		t.Errorf("ran %v when nothing was due", got)
	}

	r.Tick(context.Background(), at(t, "2026-09-16 04:00"))
	waitFor(t, func() bool { return len(ex.calls()) == 2 })
	got := strings.Join(ex.calls(), ",")
	if !strings.Contains(got, "nightly") || !strings.Contains(got, "hourly") {
		t.Errorf("ran %q, want both jobs due at 04:00", got)
	}
	if strings.Contains(got, "off") {
		t.Error("a disabled job ran")
	}
}

// A job that takes longer than its period must not pile up on itself.
func TestALongJobIsNotStartedTwice(t *testing.T) {
	t.Parallel()
	ex := &fakeExec{block: make(chan struct{})}
	r := runner(t, ex, model.Cron{ID: "slow", Enabled: true, Schedule: "* * * * *", Job: model.CronRefreshAliases})

	r.Tick(context.Background(), at(t, "2026-09-16 04:00"))
	waitFor(t, func() bool { return len(ex.calls()) == 1 })
	r.Tick(context.Background(), at(t, "2026-09-16 04:01"))
	time.Sleep(50 * time.Millisecond)
	if got := ex.calls(); len(got) != 1 {
		t.Errorf("ran %d times while the first was still going", len(got))
	}
	// The status says it is going, which is what the page shows.
	if st := statusOf(r, "slow"); !st.Running {
		t.Errorf("status = %+v, want it marked running", st)
	}
	close(ex.block)
	waitFor(t, func() bool { return !statusOf(r, "slow").Running })
}

func TestStatusesRecordSuccessAndFailure(t *testing.T) {
	t.Parallel()
	ex := &fakeExec{err: errors.New("the disk is full")}
	r := runner(t, ex, model.Cron{
		ID: "nightly", Enabled: true, Schedule: "0 4 * * *",
		Job: model.CronBackup, Description: "Nightly backup",
	})

	if err := r.RunNow(context.Background(), "nightly"); err == nil {
		t.Fatal("a failing job reported success")
	}
	st := statusOf(r, "nightly")
	if st.LastError != "the disk is full" {
		t.Errorf("error = %q", st.LastError)
	}
	if st.LastOutput != "output of nightly" {
		t.Errorf("output = %q", st.LastOutput)
	}
	if st.LastRun == nil {
		t.Error("the run was not recorded")
	}
	if st.Next == nil {
		t.Error("the next run should be worked out from the schedule")
	}
	if st.Description != "Nightly backup" || st.Kind != KindUser {
		t.Errorf("status = %+v", st)
	}
	if err := r.RunNow(context.Background(), "nope"); err == nil {
		t.Error("running an unknown job reported success")
	}
}

// The background work Ostiole does is listed beside the operator's jobs,
// because "what does this box do while I am not looking" is one question.
func TestStatusesIncludeTheSystemWork(t *testing.T) {
	t.Parallel()
	r := runner(t, &fakeExec{}, model.Cron{ID: "mine", Enabled: true, Schedule: "@daily", Job: model.CronBackup})
	got := r.Statuses()
	kinds := map[Kind]int{}
	for _, s := range got {
		kinds[s.Kind]++
		if s.Description == "" || s.Schedule == "" {
			t.Errorf("a job with nothing to say: %+v", s)
		}
	}
	if kinds[KindUser] != 1 || kinds[KindSystem] < 3 {
		t.Errorf("kinds = %v", kinds)
	}
	// Noting that a piece of background work happened shows up.
	r.Note("system:aliases")
	for _, s := range r.Statuses() {
		if s.ID == "system:aliases" && s.LastRun == nil {
			t.Error("a noted run was not recorded")
		}
	}
}

// A schedule that no longer parses is reported against the job rather
// than stopping the runner.
func TestABrokenScheduleIsReported(t *testing.T) {
	t.Parallel()
	r := runner(t, &fakeExec{}, model.Cron{ID: "broken", Enabled: true, Schedule: "not a schedule", Job: model.CronBackup})
	r.Tick(context.Background(), at(t, "2026-09-16 04:00"))
	if st := statusOf(r, "broken"); !strings.Contains(st.LastError, "schedule") {
		t.Errorf("status = %+v", st)
	}
}

func TestBackupJobWritesAndPrunes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config()
	jobs := &Jobs{
		Config:  func() *model.Config { return cfg },
		Users:   func() []auth.User { return []auth.User{{Username: "admin", Hash: "x"}} },
		Version: "test",
	}
	job := model.Cron{ID: "nightly", Job: model.CronBackup, Directory: dir, Keep: 2, WithUsers: true}

	// Three backups, named by the minute, so the oldest can be pruned.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("gateway-2026091%d-000000.json", i)), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := jobs.Run(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("output = %q, want it to mention pruning", out)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 2 {
		t.Fatalf("%d files left, want 2", len(left))
	}
	// The newest is the one just written, and it is not world-readable.
	for _, e := range left {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is mode %o; backups hold tunnel keys", e.Name(), info.Mode().Perm())
		}
	}
}

func TestCommandJobRunsWithoutAShell(t *testing.T) {
	t.Parallel()
	var gotName string
	var gotArgs []string
	jobs := &Jobs{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("  done  \n"), nil
	}}
	out, err := jobs.Run(context.Background(), model.Cron{
		Job: model.CronCommand, Command: "/usr/bin/true", Args: []string{"a b", "; rm -rf /"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" {
		t.Errorf("output = %q", out)
	}
	if gotName != "/usr/bin/true" || len(gotArgs) != 2 || gotArgs[1] != "; rm -rf /" {
		// The arguments go straight to exec, so a semicolon is just a
		// semicolon.
		t.Errorf("ran %q %v", gotName, gotArgs)
	}
}

func TestJobsSayWhatIsMissing(t *testing.T) {
	t.Parallel()
	jobs := &Jobs{}
	for _, job := range []model.Cron{
		{Job: model.CronRefreshAliases},
		{Job: model.CronRestartService, Service: "dnsmasq"},
		{Job: "invented"},
	} {
		if _, err := jobs.Run(context.Background(), job); err == nil {
			t.Errorf("%q reported success with nothing wired up", job.Job)
		}
	}
}

func statusOf(r *Runner, id string) Status {
	for _, s := range r.Statuses() {
		if s.ID == id {
			return s
		}
	}
	return Status{}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}
