package sysupdate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TransientUnit is where a package transaction runs. It is a unit of its
// own rather than a child of the daemon because an update can upgrade
// Ostiole itself: systemd would then restart ostiole.service and kill
// everything in its cgroup, which is a fine way to end up with a package
// database half written. In its own unit the transaction finishes, and
// the daemon reattaches to it when it comes back.
const TransientUnit = "ostiole-sysupdate"

// transient starts commands in a unit of that name and reports on them.
type transient struct {
	unit string
	run  Runner
	// poll is how often the unit's state is read while it works.
	poll time.Duration
}

// result is how a transient run ended.
type result struct {
	// Active is true while the command is still going.
	Active bool
	// Known is false when the unit does not exist at all, which is the
	// usual state and not an error.
	Known bool
	// Result is systemd's word for it: success, exit-code, timeout…
	Result string
	// Status is the command's exit status.
	Status int
	// Invocation identifies this run in the journal.
	Invocation string
}

func (t transient) systemctl(ctx context.Context, args ...string) (string, error) {
	out, err := t.run.Run(ctx, "systemctl", args...)
	return strings.TrimSpace(string(out)), err
}

// start queues the command. It returns as soon as systemd has taken it,
// not when it has finished.
func (t transient) start(ctx context.Context, argv []string, timeout time.Duration) error {
	if len(argv) == 0 {
		return errors.New("nothing to run")
	}
	// A unit left failed from a previous run would refuse to start.
	_, _ = t.systemctl(ctx, "reset-failed", t.unit)
	args := []string{
		"--unit=" + t.unit,
		"--quiet",
		"--property=Type=oneshot",
		// Without this the unit is gone the moment it exits, and with it
		// the result and the invocation the output is read by.
		"--property=RemainAfterExit=yes",
		"--property=TimeoutStartSec=" + strconv.Itoa(int(timeout.Seconds())),
		"--setenv=LC_ALL=C",
		"--setenv=LANG=C",
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"--",
	}
	args = append(args, argv...)
	out, err := t.run.Run(ctx, "systemd-run", args...)
	if err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, tail(out))
	}
	return nil
}

// state reads what systemd knows about the unit.
func (t transient) state(ctx context.Context) result {
	out, err := t.systemctl(ctx, "show", t.unit,
		"-p", "LoadState", "-p", "ActiveState", "-p", "SubState",
		"-p", "Result", "-p", "ExecMainStatus", "-p", "InvocationID")
	if err != nil && out == "" {
		return result{}
	}
	fields := map[string]string{}
	for _, line := range lines([]byte(out)) {
		if k, v, ok := strings.Cut(line, "="); ok {
			fields[k] = v
		}
	}
	if fields["LoadState"] == "not-found" || fields["ActiveState"] == "" || fields["ActiveState"] == "inactive" {
		return result{}
	}
	status, _ := strconv.Atoi(fields["ExecMainStatus"])
	return result{
		Known:      true,
		Active:     fields["ActiveState"] == "activating" || fields["SubState"] == "start",
		Result:     fields["Result"],
		Status:     status,
		Invocation: fields["InvocationID"],
	}
}

// wait blocks until the unit stops running.
func (t transient) wait(ctx context.Context) (result, error) {
	poll := t.poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		st := t.state(ctx)
		if !st.Known || !st.Active {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// output reads what the command printed, from the journal.
func (t transient) output(ctx context.Context, invocation string) string {
	args := []string{"--no-pager", "-o", "cat"}
	if invocation != "" {
		args = append(args, "_SYSTEMD_INVOCATION_ID="+invocation)
	} else {
		args = append(args, "-u", t.unit+".service", "-n", "500")
	}
	out, err := t.run.Run(ctx, "journalctl", args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// clear puts the unit back so the next run can have the same name.
func (t transient) clear(ctx context.Context) {
	_, _ = t.systemctl(ctx, "stop", t.unit)
	_, _ = t.systemctl(ctx, "reset-failed", t.unit)
}

// supported reports whether this router can run a transaction in a unit of
// its own. A dev run or a container falls back to a plain child process,
// where the worst case is an update cut short by a restart nobody is
// doing anyway.
func (t transient) supported() bool {
	if _, err := lookPath("systemd-run"); err != nil {
		return false
	}
	_, err := lookPath("systemctl")
	return err == nil
}

// runDirect is the fallback: run the command as a child and wait.
func runDirect(ctx context.Context, run Runner, argv []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run.Run(ctx, argv[0], argv[1:]...)
	text := strings.TrimSpace(string(out))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return text, fmt.Errorf("gave up after %s", timeout)
	}
	if err != nil {
		return text, fmt.Errorf("%s: %w", argv[0], err)
	}
	return text, nil
}

// exitError keeps a failed transient run readable: the status and what
// systemd called it.
func (r result) err() error {
	if r.Result == "" || r.Result == "success" {
		if r.Status == 0 {
			return nil
		}
		return fmt.Errorf("exit status %d", r.Status)
	}
	if r.Status != 0 {
		return fmt.Errorf("%s (exit status %d)", r.Result, r.Status)
	}
	return errors.New(r.Result)
}
