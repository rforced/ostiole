package sysupdate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A finished unit keeps its name (RemainAfterExit), and systemd-run
// refuses to start a second one by that name. The console never
// collects the daemon's runs, so the stale one is cleared first; a unit
// that is still running is somebody else's transaction and refused.
func TestStartClearsAFinishedUnitAndRefusesARunningOne(t *testing.T) {
	t.Parallel()
	show := "systemctl show " + TransientUnit + " -p LoadState -p ActiveState -p SubState -p Result -p ExecMainStatus -p InvocationID"
	run := &fakeRunner{out: map[string]string{
		show: "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0\nInvocationID=abc",
	}}
	tr := transient{unit: TransientUnit, run: run}
	if err := tr.start(context.Background(), []string{"apt-get", "-s", "remove", "ufw"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var stopped, started bool
	for _, c := range run.calls {
		if c == "systemctl stop "+TransientUnit {
			stopped = true
		}
		if strings.HasPrefix(c, "systemd-run --unit="+TransientUnit) && stopped {
			started = true
		}
	}
	if !stopped || !started {
		t.Errorf("the finished unit was not cleared before the new one started: %v", run.calls)
	}

	run = &fakeRunner{out: map[string]string{
		show: "LoadState=loaded\nActiveState=activating\nSubState=start\nResult=\nExecMainStatus=0\nInvocationID=def",
	}}
	tr = transient{unit: TransientUnit, run: run}
	err := tr.start(context.Background(), []string{"apt-get", "-s", "remove", "ufw"}, time.Minute)
	if !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want busy", err)
	}
	for _, c := range run.calls {
		if strings.HasPrefix(c, "systemd-run") || strings.HasPrefix(c, "systemctl stop") {
			t.Errorf("a running transaction was touched: %v", run.calls)
		}
	}
}

// systemd-run exiting 1 after the job was queued — the bus dropped under
// it — is not a failure to start: the unit is there, and the state poll
// takes it from here.
func TestStartSurvivesABusDropAfterQueueing(t *testing.T) {
	t.Parallel()
	show := "systemctl show " + TransientUnit + " -p LoadState -p ActiveState -p SubState -p Result -p ExecMainStatus -p InvocationID"
	run := &fakeRunner{
		out: map[string]string{
			show: "LoadState=loaded\nActiveState=activating\nSubState=start\nResult=\nExecMainStatus=0\nInvocationID=abc",
		},
		code: map[string]int{},
	}
	// The unit is unknown before the start and known after it.
	tr := transient{unit: TransientUnit, run: run}
	calls := 0
	run.out[show] = ""
	wrapped := &sequencedRunner{inner: run, before: func(line string) {
		if strings.HasPrefix(line, "systemd-run") {
			calls++
			run.mu.Lock()
			run.out[show] = "LoadState=loaded\nActiveState=activating\nSubState=start\nResult=\nExecMainStatus=0\nInvocationID=abc"
			run.code["systemd-run --unit="+TransientUnit+" --quiet --no-block --property=Type=oneshot --property=RemainAfterExit=yes --property=TimeoutStartSec=60 --setenv=LC_ALL=C --setenv=LANG=C --setenv=DEBIAN_FRONTEND=noninteractive -- apt-get -s remove ufw"] = 1
			run.mu.Unlock()
		}
	}}
	tr.run = wrapped
	if err := tr.start(context.Background(), []string{"apt-get", "-s", "remove", "ufw"}, time.Minute); err != nil {
		t.Fatalf("a queued unit was reported as not started: %v", err)
	}
	if calls != 1 {
		t.Errorf("systemd-run ran %d times", calls)
	}
}

// sequencedRunner lets a test change the router's answers as commands
// run, in the order they run.
type sequencedRunner struct {
	inner  *fakeRunner
	before func(line string)
}

func (s *sequencedRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	s.before(strings.TrimSpace(name + " " + strings.Join(args, " ")))
	return s.inner.Run(ctx, name, args...)
}

func TestLocateReturnsNothingForAnUnknownCommand(t *testing.T) {
	t.Parallel()
	if got := Locate("definitely-not-a-command-anywhere"); got != "" {
		t.Errorf("Locate invented %q", got)
	}
}

type argvRecorder struct{ calls []string }

func (a *argvRecorder) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	a.calls = append(a.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func TestHostRunnerNamesNeverRepeat(t *testing.T) {
	t.Parallel()
	rec := &argvRecorder{}
	a, b := NewHostRunner(rec), NewHostRunner(rec)
	for _, r := range []Runner{a, b, a} {
		_, _ = r.Run(context.Background(), "true")
	}
	seen := map[string]bool{}
	for _, call := range rec.calls {
		if !strings.HasPrefix(call, "systemd-run --unit=") {
			continue
		}
		unit := strings.Fields(call)[1]
		if seen[unit] {
			t.Errorf("unit name %s was handed out twice", unit)
		}
		seen[unit] = true
	}
	if len(seen) != 3 {
		t.Errorf("saw %d unit names in %v, want 3", len(seen), rec.calls)
	}
}
