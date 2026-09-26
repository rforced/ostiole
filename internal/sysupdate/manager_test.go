package sysupdate

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// discard keeps a failing update out of the test output.
func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

const showUnit = "systemctl show ostiole-sysupdate -p LoadState -p ActiveState -p SubState -p Result -p ExecMainStatus -p InvocationID"

// dnfRunner answers the commands a dnf check makes, so a manager test can
// concentrate on what the manager does with the answers.
func dnfRunner(t *testing.T) *fakeRunner {
	t.Helper()
	return &fakeRunner{
		out: map[string]string{
			"dnf -q --setopt=*.countme=0 --refresh check-update":                                             fixture(t, "dnf-check-update.txt"),
			"dnf -q --setopt=*.countme=0 --cacheonly check-update --security":                                fixture(t, "dnf-check-update-security.txt"),
			"rpm -q --qf %{NAME} %{EVR}\\n NetworkManager-libnm bash kernel kernel-core openssl-libs tzdata": fixture(t, "rpm-installed.txt"),
			"dnf --setopt=*.countme=0 needs-restarting -r":                                                   "Reboot is required to fully utilize these updates.",
		},
		code: map[string]int{
			"dnf -q --setopt=*.countme=0 --refresh check-update":              dnfPending,
			"dnf -q --setopt=*.countme=0 --cacheonly check-update --security": dnfPending,
			"dnf --setopt=*.countme=0 needs-restarting -r":                    1,
		},
	}
}

// withSystemd makes the transient unit look available, whatever the router
// running the tests actually has.
func withSystemd(t *testing.T, present bool) {
	t.Helper()
	restore := lookPath
	lookPath = func(name string) (string, error) {
		if !present && (name == "systemd-run" || name == "systemctl") {
			return "", os.ErrNotExist
		}
		return "/usr/bin/" + name, nil
	}
	t.Cleanup(func() { lookPath = restore })
}

func TestManagerCheckIsRemembered(t *testing.T) {
	dir := t.TempDir()
	run := dnfRunner(t)
	m := New(Options{PackageManager: "dnf", StateDir: dir, Run: run, Root: true})
	if !m.Available() {
		t.Fatalf("manager unavailable: %s", m.Unavailable)
	}
	if _, err := m.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := m.Status(true)
	if len(st.Pending.Packages) != 6 || st.Pending.Security != 3 {
		t.Errorf("pending = %+v", st.Pending)
	}
	if !st.RebootRequired || st.RebootReason == "" {
		t.Errorf("reboot = %v %q", st.RebootRequired, st.RebootReason)
	}
	if st.LastCheck.IsZero() {
		t.Error("the check was not dated")
	}

	// An update restarts the daemon often enough that the page has to
	// survive it.
	reloaded := NewState(dir).Snapshot()
	if len(reloaded.Pending.Packages) != 6 || !reloaded.RebootRequired {
		t.Errorf("state on disk = %+v", reloaded)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Error(err)
	}
}

func TestManagerApplyRunsInATransientUnit(t *testing.T) {
	withSystemd(t, true)
	run := dnfRunner(t)
	run.out[showUnit] = "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0\nInvocationID=abc123\n"
	run.out["journalctl --no-pager -o cat _SYSTEMD_INVOCATION_ID=abc123"] = "Upgraded:\n  openssl-libs-1:3.2.2-12.el10.x86_64"

	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: run, Root: true})
	out, err := m.Apply(t.Context(), true, []string{"kernel"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "openssl-libs") {
		t.Errorf("output = %q, want what the journal held", out)
	}

	want := "systemd-run --unit=ostiole-sysupdate --quiet --no-block --property=Type=oneshot " +
		"--property=RemainAfterExit=yes --property=TimeoutStartSec=3600 " +
		"--setenv=LC_ALL=C --setenv=LANG=C --setenv=DEBIAN_FRONTEND=noninteractive " +
		"-- dnf --setopt=*.countme=0 -y upgrade --security --exclude=kernel"
	if !run.ran(want) {
		t.Errorf("the transaction was not started as asked:\n%s", run.transcript())
	}
	// The unit name is reused every run, so it has to be given back.
	if !run.ran("systemctl stop ostiole-sysupdate") {
		t.Error("the unit was left behind")
	}
	st := m.Status(true)
	if st.LastMode != "security" || st.LastError != "" || st.Running {
		t.Errorf("status = %+v", st)
	}
}

func TestManagerApplyReportsAFailedTransaction(t *testing.T) {
	withSystemd(t, true)
	run := dnfRunner(t)
	run.out[showUnit] = "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\nExecMainStatus=1\nInvocationID=def456\n"
	run.out["journalctl --no-pager -o cat _SYSTEMD_INVOCATION_ID=def456"] = "Error: Transaction test error"

	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: run, Root: true})
	_, err := m.Apply(t.Context(), false, nil)
	if err == nil {
		t.Fatal("a failed transaction was reported as success")
	}
	if st := m.Status(false); !strings.Contains(st.LastError, "exit-code") || !strings.Contains(st.LastOutput, "Transaction test error") {
		t.Errorf("status = %+v", st)
	}
}

func TestManagerFallsBackToAChildProcess(t *testing.T) {
	withSystemd(t, false)
	run := dnfRunner(t)
	run.out["dnf --setopt=*.countme=0 -y upgrade"] = "Nothing to do."

	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: run, Root: true})
	out, err := m.Apply(t.Context(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "Nothing to do." {
		t.Errorf("output = %q", out)
	}
}

func TestManagerRefusesSecurityWhereThereIsNone(t *testing.T) {
	run := &fakeRunner{}
	m := New(Options{PackageManager: "pacman", StateDir: t.TempDir(), Run: run, Root: true})
	_, err := m.Apply(t.Context(), true, nil)
	if err == nil || !strings.Contains(err.Error(), "All or Manual") {
		t.Fatalf("err = %v, want the explanation the page shows", err)
	}
	if run.count() != 0 {
		t.Errorf("it ran something anyway: %s", run.transcript())
	}
	if st := m.Status(true); st.SecurityCapable {
		t.Error("pacman is reported as able to install security fixes alone")
	}
}

func TestManagerWithoutRootExplainsItself(t *testing.T) {
	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: &fakeRunner{}, Root: false})
	if m.Available() {
		t.Fatal("an unprivileged daemon claimed it could update the router")
	}
	st := m.Status(false)
	if !strings.Contains(st.Unavailable, "root") {
		t.Errorf("unavailable = %q", st.Unavailable)
	}
	// The manager is still named, so the page can say what it would use.
	if st.Manager != "dnf" {
		t.Errorf("manager = %q", st.Manager)
	}
	if _, err := m.Check(t.Context()); err == nil {
		t.Error("checked anyway")
	}
}

func TestManagerReattachesToARunningUpdate(t *testing.T) {
	withSystemd(t, true)
	run := dnfRunner(t)
	run.out[showUnit] = "LoadState=loaded\nActiveState=activating\nSubState=start\nResult=success\nExecMainStatus=0\nInvocationID=ghi789\n"
	run.out["journalctl --no-pager -o cat _SYSTEMD_INVOCATION_ID=ghi789"] = "Upgrading…"

	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: run, Root: true})
	m.unit.poll = time.Millisecond
	m.Reattach(t.Context())
	if !m.Status(false).Running {
		t.Fatal("an update that outlived the daemon is not reported as running")
	}

	// When it finishes, the result is picked up without anyone asking, and
	// the re-check is in before the router is let go: the page stops
	// polling at the first status that is not running.
	run.say(showUnit, "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0\nInvocationID=ghi789\n")
	waitUntil(t, func() bool { return !m.Status(false).Running })
	if st := m.Status(false); st.LastRun.IsZero() || st.LastCheck.IsZero() {
		t.Errorf("the router was let go before the update was recorded: %+v", st.Snapshot)
	}
}

func TestDistroName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")
	body := "NAME=\"Rocky Linux\"\nVERSION=\"10.0 (Red Quartz)\"\nID=\"rocky\"\nPRETTY_NAME=\"Rocky Linux 10.0 (Red Quartz)\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := osRelease
	osRelease = path
	defer func() { osRelease = restore }()

	if got := distroName(); got != "Rocky Linux 10.0 (Red Quartz)" {
		t.Errorf("distroName = %q", got)
	}
}

func TestRunScheduledObeysTheMode(t *testing.T) {
	withSystemd(t, true)

	newManager := func(run *fakeRunner) *Manager {
		run.say(showUnit, "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0\nInvocationID=abc123\n")
		run.say("journalctl --no-pager -o cat _SYSTEMD_INVOCATION_ID=abc123", "Complete!")
		return New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: run, Root: true})
	}

	// Manual still checks, so the page can say what is waiting, but the
	// router does not change under anyone.
	run := dnfRunner(t)
	m := newManager(run)
	out, err := m.RunScheduled(t.Context(), ModeManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "by hand") || !strings.Contains(out, "6 update(s)") {
		t.Errorf("manual = %q", out)
	}
	if run.ran("systemd-run --unit=ostiole-sysupdate --quiet --no-block --property=Type=oneshot --property=RemainAfterExit=yes --property=TimeoutStartSec=3600 --setenv=LC_ALL=C --setenv=LANG=C --setenv=DEBIAN_FRONTEND=noninteractive -- dnf --setopt=*.countme=0 -y upgrade") {
		t.Error("manual mode installed something")
	}
	if st := m.Status(false); len(st.Pending.Packages) != 6 {
		t.Errorf("manual mode did not record what is waiting: %+v", st.Pending)
	}

	// Security installs, and only the security fixes.
	run = dnfRunner(t)
	m = newManager(run)
	if _, err := m.RunScheduled(t.Context(), ModeSecurity, nil); err != nil {
		t.Fatal(err)
	}
	if !run.ran("systemd-run --unit=ostiole-sysupdate --quiet --no-block --property=Type=oneshot --property=RemainAfterExit=yes --property=TimeoutStartSec=3600 --setenv=LC_ALL=C --setenv=LANG=C --setenv=DEBIAN_FRONTEND=noninteractive -- dnf --setopt=*.countme=0 -y upgrade --security") {
		t.Errorf("security did not run the security upgrade:\n%s", run.transcript())
	}

	// Security with nothing security-flagged waiting leaves the router
	// alone rather than upgrading everything.
	run = dnfRunner(t)
	run.say("dnf -q --setopt=*.countme=0 --cacheonly check-update --security", "")
	run.code["dnf -q --setopt=*.countme=0 --cacheonly check-update --security"] = 0
	m = newManager(run)
	out, err = m.RunScheduled(t.Context(), ModeSecurity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing installed") {
		t.Errorf("security with no fixes = %q", out)
	}
	if run.ran("systemd-run --unit=ostiole-sysupdate --quiet --no-block --property=Type=oneshot --property=RemainAfterExit=yes --property=TimeoutStartSec=3600 --setenv=LC_ALL=C --setenv=LANG=C --setenv=DEBIAN_FRONTEND=noninteractive -- dnf --setopt=*.countme=0 -y upgrade --security") {
		t.Error("it upgraded anyway")
	}
}

// The scheduled check asks what is waiting and touches nothing, whatever
// the mode is, so a router that installs by hand still knows where it
// stands.
func TestCheckScheduledInstallsNothing(t *testing.T) {
	withSystemd(t, true)
	run := dnfRunner(t)
	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: run, Root: true, Log: discard()})

	out, err := m.CheckScheduled(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "6 update(s) waiting") || !strings.Contains(out, "security fixes") {
		t.Errorf("out = %q", out)
	}
	if run.ran("systemd-run --unit=ostiole-sysupdate --quiet --no-block --property=Type=oneshot --property=RemainAfterExit=yes --property=TimeoutStartSec=3600 --setenv=LC_ALL=C --setenv=LANG=C --setenv=DEBIAN_FRONTEND=noninteractive -- dnf --setopt=*.countme=0 -y upgrade") {
		t.Errorf("a check installed something:\n%s", run.transcript())
	}
	// What it found is what the page draws itself from.
	st := m.Status(false)
	if len(st.Pending.Packages) != 6 || st.LastCheck.IsZero() {
		t.Errorf("status = %+v", st)
	}
	// And the router is free again afterwards.
	if st.Running {
		t.Error("the check did not let go of the router")
	}
}

func TestStartClaimsTheBoxBeforeItReturns(t *testing.T) {
	withSystemd(t, true)
	run := dnfRunner(t)
	// The unit is absent to begin with and reports activating from the
	// moment the transaction's own systemd-run is issued, so the update
	// really is still going while the assertions below run. Told it was
	// activating from the start, transient.start would refuse it as
	// somebody else's transaction, the worker would end straight away, and
	// the test would only pass when it won the race against its own
	// goroutine. The line is matched on the unit and not on systemd-run
	// alone because every command the check runs leaves the sandbox through
	// a transient unit of its own first.
	const absent = "LoadState=not-found\nActiveState=inactive\nSubState=dead\nResult=success\nExecMainStatus=0\nInvocationID=\n"
	const running = "LoadState=loaded\nActiveState=activating\nSubState=start\nResult=success\nExecMainStatus=0\nInvocationID=abc123\n"
	run.say(showUnit, absent)
	started := &sequencedRunner{inner: run, before: func(line string) {
		if strings.HasPrefix(line, "systemd-run --unit="+TransientUnit+" ") {
			run.say(showUnit, running)
		}
	}}
	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: started, Root: true, Log: discard()})
	m.unit.poll = time.Millisecond

	if err := m.Start(false, nil); err != nil {
		t.Fatal(err)
	}
	if !m.Status(false).Running {
		t.Error("the status the browser gets back does not say an update is running")
	}
	// A second press is refused rather than starting a second dnf.
	if err := m.Start(false, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("second Start = %v, want ErrBusy", err)
	}

	// Let it finish before the test does: a goroutine still polling
	// after the router it was told about has been taken away is a data race
	// waiting to be reported against the next test. A unit can only be
	// told it exited once it has been started, or the hook above overwrites
	// the answer and the worker polls an activating unit until the timeout.
	waitUntil(t, func() bool { return run.ranMatching("systemd-run", "--unit="+TransientUnit+" ") })
	run.say(showUnit, "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0\nInvocationID=abc123\n")
	waitUntil(t, func() bool { return !m.Status(false).Running })
}

// waitUntil spins until something becomes true, or the test fails.
func waitUntil(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

// The daemon's unit is hardened: ProtectSystem=strict leaves /var
// read-only, and every package manager writes there — dnf to
// /var/log/dnf.log before it reads a single package. So the commands
// have to leave the sandbox, not just the long upgrade.
func TestPackageCommandsLeaveTheSandbox(t *testing.T) {
	withSystemd(t, true)
	run := dnfRunner(t)
	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: run, Root: true, Log: discard()})
	if _, err := m.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-- dnf -q --setopt=*.countme=0 --refresh check-update", "-- dnf --setopt=*.countme=0 needs-restarting -r"} {
		if !run.ranMatching("systemd-run --unit="+commandTag, "--wait", want) {
			t.Errorf("%q ran inside the sandbox, where /var is read-only:\n%s", want, run.transcript())
		}
	}
	// Neither our own descriptors nor a file the daemon can write will
	// do, so the output is read back out of the journal — tagged, so it
	// is the command's output and not systemd's commentary about it.
	if !run.ranMatching("journalctl", "SYSLOG_IDENTIFIER="+commandTag, "-o", "cat") {
		t.Errorf("the output was never read back:\n%s", run.transcript())
	}
	if !run.ranMatching("--property=SyslogIdentifier=" + commandTag) {
		t.Errorf("the output was not tagged, so it cannot be told from systemd's:\n%s", run.transcript())
	}

	// systemd's own tools work perfectly well from inside the sandbox,
	// and wrapping them in a second transient unit would be absurd.
	for line := range strings.SplitSeq(run.transcript(), "\n") {
		if strings.Contains(line, "-- systemctl") || strings.Contains(line, "-- journalctl") {
			t.Errorf("systemd was driven through a transient unit: %q", line)
		}
	}
}

func TestWithoutSystemdCommandsRunDirectly(t *testing.T) {
	withSystemd(t, false)
	run := dnfRunner(t)
	m := New(Options{PackageManager: "dnf", StateDir: t.TempDir(), Run: run, Root: true, Log: discard()})
	if _, err := m.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !run.ran("dnf -q --setopt=*.countme=0 --refresh check-update") {
		t.Errorf("a router without systemd-run could not check at all:\n%s", run.transcript())
	}
}
