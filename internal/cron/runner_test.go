package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/model"
)

func config(crons ...model.Cron) *model.Config {
	cfg := model.Starter(model.StarterOptions{
		Hostname: "gateway", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0",
	})
	cfg.Crons = crons
	return cfg
}

type fakeExec struct {
	mu    sync.Mutex
	ran   []string
	err   error
	block chan struct{}
}

func (f *fakeExec) Run(_ context.Context, c model.Cron) (string, error) {
	f.mu.Lock()
	f.ran = append(f.ran, c.ID)
	block, err := f.block, f.err
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return "output of " + c.ID, err
}

func (f *fakeExec) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ran...)
}

// runner gives the runner no directory. Tick runs crons in the background
// and each saves after it finishes, which can land after the test returns
// and break the TempDir cleanup. Tests that read the file pass their own
// directory and wait with waitForSave.
func runner(t *testing.T, ex Executor, crons ...model.Cron) *Runner {
	t.Helper()
	cfg := config(crons...)
	return NewRunner(func() *model.Config { return cfg }, ex, slog.New(slog.DiscardHandler), "")
}

func TestTickRunsWhatIsDue(t *testing.T) {
	t.Parallel()
	ex := &fakeExec{}
	// Three in the morning: the update checks Ostiole schedules for itself
	// run at four, and would be counted here.
	r := runner(t, ex,
		model.Cron{ID: "nightly", Enabled: true, Schedule: "0 3 * * *", Kind: model.CronBackup},
		model.Cron{ID: "hourly", Enabled: true, Schedule: "@hourly", Kind: model.CronRefreshAliases},
		model.Cron{ID: "off", Enabled: false, Schedule: "* * * * *", Kind: model.CronRefreshAliases},
	)

	// Nothing is due at half past one.
	r.Tick(context.Background(), at(t, "2026-09-16 01:30"))
	if got := ex.calls(); len(got) != 0 {
		t.Errorf("ran %v when nothing was due", got)
	}

	r.Tick(context.Background(), at(t, "2026-09-16 03:00"))
	waitFor(t, func() bool { return len(ex.calls()) == 2 })
	got := strings.Join(ex.calls(), ",")
	if !strings.Contains(got, "nightly") || !strings.Contains(got, "hourly") {
		t.Errorf("ran %q, want both crons due at 03:00", got)
	}
	if strings.Contains(got, "off") {
		t.Error("a disabled cron ran")
	}
}

// A schedule is read in the router's own timezone, so "0 3 * * *" is three
// in the morning where the router stands rather than wherever the process
// that runs it happens to think it is.
func TestTickReadsSchedulesInTheConfiguredZone(t *testing.T) {
	t.Parallel()
	ex := &fakeExec{}
	cfg := config(model.Cron{ID: "nightly", Enabled: true, Schedule: "0 3 * * *", Kind: model.CronBackup})
	cfg.System.Timezone = "Europe/Berlin"
	r := NewRunner(func() *model.Config { return cfg }, ex, slog.New(slog.DiscardHandler), "")

	// September puts Berlin two hours ahead, so 03:00 UTC is 05:00 there.
	r.Tick(context.Background(), at(t, "2026-09-16 03:00"))
	time.Sleep(50 * time.Millisecond)
	if got := ex.calls(); len(got) != 0 {
		t.Errorf("ran %v at 05:00 Berlin time", got)
	}
	r.Tick(context.Background(), at(t, "2026-09-16 01:00"))
	waitFor(t, func() bool { return len(ex.calls()) == 1 })

	// And the time it reports next is in that zone too.
	next := statusOf(r, "nightly").Next
	if next == nil {
		t.Fatal("no next time reported")
	}
	if h, m, _ := next.Clock(); h != 3 || m != 0 {
		t.Errorf("next = %s, want 03:00 Berlin time", next)
	}
}

// A cron that takes longer than its period must not pile up on itself.
func TestALongCronIsNotStartedTwice(t *testing.T) {
	t.Parallel()
	ex := &fakeExec{block: make(chan struct{})}
	dir := t.TempDir()
	cfg := config(model.Cron{ID: "slow", Enabled: true, Schedule: "* * * * *", Kind: model.CronRefreshAliases})
	r := NewRunner(func() *model.Config { return cfg }, ex, slog.New(slog.DiscardHandler), dir)

	r.Tick(context.Background(), at(t, "2026-09-16 04:10"))
	waitFor(t, func() bool { return len(ex.calls()) == 1 })
	r.Tick(context.Background(), at(t, "2026-09-16 04:11"))
	time.Sleep(50 * time.Millisecond)
	if got := ex.calls(); len(got) != 1 {
		t.Errorf("ran %d times while the first was still going", len(got))
	}
	// The status says it is going, which is what the page shows.
	if st := statusOf(r, "slow"); !st.Running {
		t.Errorf("status = %+v, want it marked running", st)
	}
	close(ex.block)
	waitForSave(t, dir, "slow")
	if st := statusOf(r, "slow"); st.Running {
		t.Errorf("status = %+v, want the finished run marked done", st)
	}
}

func TestStatusesRecordSuccessAndFailure(t *testing.T) {
	t.Parallel()
	ex := &fakeExec{err: errors.New("the disk is full")}
	r := runner(t, ex, model.Cron{
		ID: "nightly", Enabled: true, Schedule: "0 4 * * *",
		Kind: model.CronBackup, Description: "Nightly backup",
	})

	if err := r.RunNow(context.Background(), "nightly"); err == nil {
		t.Fatal("a failing cron reported success")
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
	if st.Description != "Nightly backup" || st.Origin != OriginUser {
		t.Errorf("status = %+v", st)
	}
	if err := r.RunNow(context.Background(), "nope"); err == nil {
		t.Error("running an unknown cron reported success")
	}
}

// The background work Ostiole does is listed beside the operator's crons,
// because "what does this router do while I am not looking" is one question.
// Every timer the daemon starts has to be here, or the page quietly
// under-reports what the router is doing.
func TestStatusesIncludeTheSystemWork(t *testing.T) {
	t.Parallel()
	r := runner(t, &fakeExec{}, model.Cron{ID: "mine", Enabled: true, Schedule: "@daily", Kind: model.CronBackup})
	got := r.Statuses()
	origins := map[Origin]int{}
	listed := map[string]bool{}
	for _, s := range got {
		origins[s.Origin]++
		listed[s.ID] = true
		if s.Description == "" || s.Schedule == "" {
			t.Errorf("a cron with nothing to say: %+v", s)
		}
	}
	if origins[OriginUser] != 1 {
		t.Errorf("origins = %v", origins)
	}
	// The ids the daemon's workers report against, plus the four update
	// crons the update settings imply.
	for _, id := range []string{
		"system:gateways", "system:aliases", "system:blocklists",
		"system:sessions", "system:firewall-log", "system:drives",
		model.CronIDSystemUpdateCheck, model.CronIDSystemUpdate,
		model.CronIDOstioleUpdateCheck, model.CronIDOstioleUpdate,
		model.CronIDRemoteBackup,
	} {
		if !listed[id] {
			t.Errorf("%s is not on the page that says what this router does by itself", id)
		}
	}
	// Noting that a piece of background work happened shows up.
	r.Note("system:aliases")
	for _, s := range r.Statuses() {
		if s.ID == "system:aliases" && s.LastRun == nil {
			t.Error("a noted run was not recorded")
		}
	}
}

// The gateway row promises failover only where there is a second gateway
// to fail over to, so the page does not advertise work that cannot happen.
func TestTheGatewayRowOnlyPromisesFailoverWhenItCanHappen(t *testing.T) {
	t.Parallel()
	cfg := config()
	r := NewRunner(func() *model.Config { return cfg }, &fakeExec{}, slog.New(slog.DiscardHandler), "")

	gateways := func() Status {
		t.Helper()
		return statusOf(r, "system:gateways")
	}
	if got := gateways().Description; strings.Contains(got, "move traffic") {
		t.Errorf("description with no gateways = %q, want no promise of failover", got)
	}

	cfg.Gateways = []model.Gateway{{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"}}
	if got := gateways().Description; strings.Contains(got, "move traffic") {
		t.Errorf("description with one gateway = %q, want no promise of failover", got)
	}

	// A second gateway, but switched off, is still nowhere to fail over to.
	cfg.Gateways = append(cfg.Gateways, model.Gateway{Name: "backup", Interface: "eth2", Address: "198.51.100.1"})
	if got := gateways().Description; strings.Contains(got, "move traffic") {
		t.Errorf("description with a disabled second gateway = %q, want no promise of failover", got)
	}

	cfg.Gateways[1].Enabled = true
	if got := gateways().Description; !strings.Contains(got, "move traffic") {
		t.Errorf("description with two gateways = %q, want the failover promise", got)
	}
}

// A schedule that no longer parses is reported against the cron rather
// than stopping the runner.
func TestABrokenScheduleIsReported(t *testing.T) {
	t.Parallel()
	r := runner(t, &fakeExec{}, model.Cron{ID: "broken", Enabled: true, Schedule: "not a schedule", Kind: model.CronBackup})
	// Three, not four: the update checks are due at four, and this test does
	// not wait for the runs it would start.
	r.Tick(context.Background(), at(t, "2026-09-16 03:00"))
	if st := statusOf(r, "broken"); !strings.Contains(st.LastError, "schedule") {
		t.Errorf("status = %+v", st)
	}
}

func TestBackupCronWritesAndPrunes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := config()
	actions := &Actions{
		Config:  func() *model.Config { return cfg },
		Users:   func() []auth.User { return []auth.User{{Username: "admin", Hash: "x"}} },
		Version: "test",
	}
	c := model.Cron{ID: "nightly", Kind: model.CronBackup, Directory: dir, Keep: 2, WithUsers: true}

	// Three backups, named by the minute, so the oldest can be pruned.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("gateway-2026091%d-000000.json", i)), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := actions.Run(context.Background(), c)
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

func TestCommandCronRunsWithoutAShell(t *testing.T) {
	t.Parallel()
	var gotName string
	var gotArgs []string
	actions := &Actions{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("  done  \n"), nil
	}}
	out, err := actions.Run(context.Background(), model.Cron{
		Kind: model.CronCommand, Command: "/usr/bin/true", Args: []string{"a b", "; rm -rf /"},
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

func TestActionsSayWhatIsMissing(t *testing.T) {
	t.Parallel()
	actions := &Actions{}
	for _, c := range []model.Cron{
		{Kind: model.CronRefreshAliases},
		{Kind: model.CronRefreshBlocklists},
		{Kind: model.CronRestartService, Service: "dnsmasq"},
		{Kind: model.CronWake, Device: "nas"},
		{Kind: "invented"},
	} {
		if _, err := actions.Run(context.Background(), c); err == nil {
			t.Errorf("%q reported success with nothing wired up", c.Kind)
		}
	}
}

// A wake cron reads its device from the configuration in force, so a
// device that was edited is woken where it is now, and one that cannot be
// woken fails the run with nothing sent.
func TestWakeCronSendsToTheDevice(t *testing.T) {
	t.Parallel()
	cfg := config()
	cfg.Services.WoL.Devices = []model.WoLDevice{{ID: "nas", Interface: "eth1", MAC: "AA:BB:CC:00:00:01"}}
	var sent []string
	actions := &Actions{
		Config: func() *model.Config { return cfg },
		Wake: func(iface string, mac net.HardwareAddr) error {
			sent = append(sent, iface+" "+mac.String())
			return nil
		},
	}
	wake := model.Cron{Kind: model.CronWake, Device: "nas"}
	out, err := actions.Run(context.Background(), wake)
	if err != nil {
		t.Fatal(err)
	}
	if out != "sent a wake packet to aa:bb:cc:00:00:01 on eth1" {
		t.Errorf("output = %q", out)
	}
	if len(sent) != 1 || sent[0] != "eth1 aa:bb:cc:00:00:01" {
		t.Errorf("sent = %v", sent)
	}

	lan, _ := cfg.Interface("eth1")
	lan.Enabled = false
	if _, err := actions.Run(context.Background(), wake); err == nil || !strings.Contains(err.Error(), "off") {
		t.Errorf("interface off: err = %v", err)
	}
	lan.Enabled = true
	cfg.Services.WoL.Devices[0].Interface = "eth0"
	if _, err := actions.Run(context.Background(), wake); err == nil || !strings.Contains(err.Error(), "external zone") {
		t.Errorf("moved to the WAN: err = %v", err)
	}
	if _, err := actions.Run(context.Background(), model.Cron{Kind: model.CronWake, Device: "gone"}); err == nil {
		t.Error("a device that is gone reported success")
	}
	if len(sent) != 1 {
		t.Errorf("sent = %v, want only the first", sent)
	}

	failing := &Actions{
		Config: func() *model.Config { return cfg },
		Wake:   func(string, net.HardwareAddr) error { return errors.New("send on eth1: network is down") },
	}
	cfg.Services.WoL.Devices[0].Interface = "eth1"
	if _, err := failing.Run(context.Background(), wake); err == nil || !strings.Contains(err.Error(), "network is down") {
		t.Errorf("failed send: err = %v", err)
	}
}

// panicExec panics the way a library reading a malformed answer might.
type panicExec struct{}

func (panicExec) Run(context.Context, model.Cron) (string, error) { panic("unexpected end of answer") }

// A cron that panics fails like any other, and is not left marked as
// running, which would skip every scheduled run after it.
func TestAPanickingCronFailsThatRun(t *testing.T) {
	t.Parallel()
	cfg := config(model.Cron{ID: "nightly", Enabled: true, Schedule: "0 3 * * *", Kind: model.CronBackup})
	r := NewRunner(func() *model.Config { return cfg }, panicExec{}, slog.New(slog.DiscardHandler), "")
	if err := r.RunNow(context.Background(), "nightly"); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("RunNow = %v", err)
	}
	if st := statusOf(r, "nightly"); st.Running || !strings.Contains(st.LastError, "unexpected end of answer") {
		t.Errorf("status = %+v", st)
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

// waitForSave waits until a background run has written its result out.
// The status says so first: record marks the result and then saves, so a
// test that reads the file on the status alone reads it too early, and a
// test that ends there leaves a write landing in a directory the harness
// is already deleting.
func waitForSave(t *testing.T, dir, id string) {
	t.Helper()
	waitFor(t, func() bool {
		raw, err := os.ReadFile(filepath.Join(dir, "crons.json"))
		return err == nil && strings.Contains(string(raw), id)
	})
}

func TestUpdateCronsRunOnTheirOwnSchedule(t *testing.T) {
	t.Parallel()
	ex := &fakeExec{}
	cfg := config()
	r := NewRunner(func() *model.Config { return cfg }, ex, slog.New(slog.DiscardHandler), "")

	// Nobody wrote these out; they come from the update settings. Midweek
	// both sources are asked what is waiting, and neither installs.
	wednesday := time.Date(2026, 9, 16, 4, 0, 0, 0, time.UTC)
	if wednesday.Weekday() == time.Sunday {
		t.Fatalf("16 September 2026 is a %s; pick another date", wednesday.Weekday())
	}
	r.Tick(t.Context(), wednesday)
	waitFor(t, func() bool { return len(ex.calls()) == 2 })
	for _, id := range []string{model.CronIDSystemUpdateCheck, model.CronIDOstioleUpdateCheck} {
		if !contains(ex.calls(), id) {
			t.Errorf("ran %v midweek, want both checks", ex.calls())
		}
	}
	if contains(ex.calls(), model.CronIDSystemUpdate) || contains(ex.calls(), model.CronIDOstioleUpdate) {
		t.Errorf("ran %v midweek, want nothing installed", ex.calls())
	}

	// Sunday, half an hour after the check, is when they install.
	sunday := time.Date(2026, 9, 20, 4, 30, 0, 0, time.UTC)
	if sunday.Weekday() != time.Sunday {
		t.Fatalf("20 September 2026 is a %s; pick another date", sunday.Weekday())
	}
	r.Tick(t.Context(), sunday)
	waitFor(t, func() bool { return len(ex.calls()) == 4 })
	for _, id := range []string{model.CronIDSystemUpdate, model.CronIDOstioleUpdate} {
		if !contains(ex.calls(), id) {
			t.Errorf("ran %v on Sunday, want both installs", ex.calls())
		}
	}
}

func TestUpdateCronsAreReportedAsOstioleOwnWork(t *testing.T) {
	t.Parallel()
	cfg := config()
	// The distro patches itself; Ostiole is installed by hand.
	cfg.Updates.Ostiole.Mode = model.UpdateManual
	r := NewRunner(func() *model.Config { return cfg }, &fakeExec{}, slog.New(slog.DiscardHandler), "")
	found := map[string]Status{}
	for _, st := range r.Statuses() {
		if st.Origin == OriginSystem {
			found[st.ID] = st
		}
	}
	for _, want := range []struct {
		id       string
		schedule string
		enabled  bool
	}{
		{model.CronIDSystemUpdateCheck, model.DefaultUpdateCheckSchedule, true},
		{model.CronIDSystemUpdate, model.DefaultUpdateSchedule, true},
		{model.CronIDOstioleUpdateCheck, model.DefaultUpdateCheckSchedule, true},
		{model.CronIDOstioleUpdate, model.DefaultUpdateSchedule, false},
	} {
		st, ok := found[want.id]
		if !ok {
			t.Fatalf("%s is not on the page that says what this router does by itself", want.id)
		}
		if st.Schedule != want.schedule || st.Enabled != want.enabled {
			t.Errorf("%s = %+v, want schedule %q, enabled %v", want.id, st, want.schedule, want.enabled)
		}
		// A cron that is turned off has no next run to promise.
		if want.enabled && st.Next == nil {
			t.Errorf("%s does not say when it next runs", want.id)
		}
		if !want.enabled && st.Next != nil {
			t.Errorf("%s is disabled but says it runs at %s", want.id, st.Next)
		}
	}
}

func TestRunNowFindsTheUpdateCrons(t *testing.T) {
	t.Parallel()
	for _, id := range []string{
		model.CronIDSystemUpdate, model.CronIDSystemUpdateCheck,
		model.CronIDOstioleUpdate, model.CronIDOstioleUpdateCheck,
	} {
		ex := &fakeExec{}
		r := runner(t, ex)
		if err := r.RunNow(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if got := ex.calls(); len(got) != 1 || got[0] != id {
			t.Errorf("ran %v, want %s", got, id)
		}
	}
}

func TestUpdateCronsObeyTheMode(t *testing.T) {
	t.Parallel()
	cfg := config()
	cfg.Updates = model.Updates{
		System:  model.PackageUpdates{Mode: model.UpdateManual, Exclude: []string{"kernel"}},
		Ostiole: model.SelfUpdates{Mode: model.UpdateAll, Channel: model.ChannelBeta},
	}
	var sawMode, sawChannel string
	var sawExclude []string
	actions := &Actions{
		Config: func() *model.Config { return cfg },
		SystemUpdate: func(_ context.Context, mode string, exclude []string) (string, error) {
			sawMode, sawExclude = mode, exclude
			return "checked", nil
		},
		SelfUpdate: func(_ context.Context, mode, channel string) (string, error) {
			sawChannel = channel
			return "mode " + mode, nil
		},
	}
	if _, err := actions.Run(t.Context(), model.Cron{ID: "x", Kind: model.CronSystemUpdate}); err != nil {
		t.Fatal(err)
	}
	if sawMode != "manual" || len(sawExclude) != 1 || sawExclude[0] != "kernel" {
		t.Errorf("mode = %q, exclude = %v", sawMode, sawExclude)
	}
	out, err := actions.Run(t.Context(), model.Cron{ID: "y", Kind: model.CronOstioleUpdate})
	if err != nil || out != "mode all" {
		t.Errorf("out = %q, err = %v", out, err)
	}
	if sawChannel != model.ChannelBeta {
		t.Errorf("channel = %q", sawChannel)
	}

	// A router with nothing wired up says so rather than failing obscurely.
	bare := &Actions{Config: actions.Config}
	for _, kind := range []model.CronKind{
		model.CronSystemUpdate, model.CronOstioleUpdate,
		model.CronSystemUpdateCheck, model.CronOstioleUpdateCheck,
	} {
		if _, err := bare.Run(t.Context(), model.Cron{ID: "z", Kind: kind}); err == nil {
			t.Errorf("a router with nothing wired up pretended to run %s", kind)
		}
	}
}

// The checks run whatever the mode is, which is what keeps the answer on
// the page fresh on a router that installs nothing by itself.
func TestCheckCronsRunWhateverTheModeIs(t *testing.T) {
	t.Parallel()
	cfg := config()
	cfg.Updates = model.Updates{
		System:  model.PackageUpdates{Mode: model.UpdateManual},
		Ostiole: model.SelfUpdates{Mode: model.UpdateManual, Channel: model.ChannelBeta},
	}
	var sawChannel string
	actions := &Actions{
		Config:      func() *model.Config { return cfg },
		SystemCheck: func(context.Context) (string, error) { return "3 update(s) waiting", nil },
		SelfCheck: func(_ context.Context, channel string) (string, error) {
			sawChannel = channel
			return "0.4.0 is available", nil
		},
	}
	out, err := actions.Run(t.Context(), model.Cron{ID: "a", Kind: model.CronSystemUpdateCheck})
	if err != nil || out != "3 update(s) waiting" {
		t.Errorf("out = %q, err = %v", out, err)
	}
	out, err = actions.Run(t.Context(), model.Cron{ID: "b", Kind: model.CronOstioleUpdateCheck})
	if err != nil || out != "0.4.0 is available" {
		t.Errorf("out = %q, err = %v", out, err)
	}
	// The channel comes from the configuration, not from whoever asked.
	if sawChannel != model.ChannelBeta {
		t.Errorf("channel = %q", sawChannel)
	}
}

// The copy in the bucket is the one backup nobody takes by hand, so what
// goes into it is worth being sure of: it is encrypted, it carries the
// accounts, and it names the cron that took it.
func TestRemoteBackupCronHandsOverAnEncryptedArchive(t *testing.T) {
	t.Parallel()
	cfg := config()
	cfg.Backup.Remote = model.RemoteBackup{
		Enabled: true, Endpoint: "https://s3.us-west-004.backblazeb2.com", Bucket: "router-backups",
		KeyID: "k", Secret: "s", Passphrase: "correct horse",
	}
	var gotArchive *backup.Archive
	var gotHostname string
	var gotSettings model.RemoteBackup
	actions := &Actions{
		Config:  func() *model.Config { return cfg },
		Users:   func() []auth.User { return []auth.User{{Username: "admin", Hash: "x"}} },
		Version: "test",
		Remote: func(_ context.Context, r model.RemoteBackup, hostname string, a *backup.Archive) (string, error) {
			gotSettings, gotHostname, gotArchive = r, hostname, a
			return "uploaded ostiole/gateway-20260920-030000.json.age (21 KB)", nil
		},
	}
	out, err := actions.Run(t.Context(), model.Cron{ID: model.CronIDRemoteBackup, Kind: model.CronRemoteBackup})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "uploaded ") {
		t.Errorf("output = %q", out)
	}
	if gotHostname != "gateway" || gotSettings.Bucket != "router-backups" {
		t.Errorf("handed over %q and %+v", gotHostname, gotSettings)
	}
	if gotArchive == nil || !gotArchive.Encrypted() {
		t.Fatalf("archive = %+v, want an encrypted one", gotArchive)
	}
	if len(gotArchive.Users) != 1 {
		t.Errorf("archive carries %d accounts, want the administrators in it", len(gotArchive.Users))
	}
	if gotArchive.Note != "scheduled backup "+model.CronIDRemoteBackup {
		t.Errorf("note = %q", gotArchive.Note)
	}

	cfg.Backup.Remote.Enabled = false
	if _, err := actions.Run(t.Context(), model.Cron{Kind: model.CronRemoteBackup}); err == nil ||
		err.Error() != "remote backup is off" {
		t.Errorf("switched off: %v", err)
	}

	cfg.Backup.Remote.Enabled = true
	bare := &Actions{Config: actions.Config}
	if _, err := bare.Run(t.Context(), model.Cron{Kind: model.CronRemoteBackup}); err == nil ||
		err.Error() != "this router cannot reach a bucket from here" {
		t.Errorf("nothing wired up: %v", err)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
