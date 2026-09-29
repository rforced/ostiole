package cli

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A second daemon on the same configuration is refused, and says which
// process holds it. The lock goes with the first, so a restart is free.
func TestOneDaemonPerConfiguration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	release, err := lockDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lockDaemon(dir)
	if err == nil || !strings.Contains(err.Error(), "already running") || !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("second daemon: %v", err)
	}
	release()
	again, err := lockDaemon(dir)
	if err != nil {
		t.Fatalf("after the first stopped: %v", err)
	}
	again()
}

// ostiole status reports the backend of the daemon holding the
// configuration, read from its command line, and ignores whatever took
// the pid of one that has gone.
func TestStatusFindsTheDaemonsCommandLine(t *testing.T) {
	t.Parallel()
	dir, proc := t.TempDir(), t.TempDir()
	if got := daemonArgs(dir, proc); got != nil {
		t.Errorf("no daemon ever ran, yet found %q", got)
	}
	release, err := lockDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	pid := filepath.Join(proc, strconv.Itoa(os.Getpid()))
	cmdline := func(args ...string) {
		t.Helper()
		if err := os.MkdirAll(pid, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pid, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmdline("/usr/local/bin/ostiole", "--config-dir", dir, "--network-backend", "none", "serve", "--tls")
	if got := flagValue(daemonArgs(dir, proc), "network-backend", "auto"); got != "none" {
		t.Errorf("backend = %q, want the daemon's none", got)
	}
	cmdline("/usr/local/bin/ostiole", "--config-dir="+dir, "serve")
	if got := flagValue(daemonArgs(dir, proc), "network-backend", "auto"); got != "auto" {
		t.Errorf("backend = %q, want the default a daemon without the flag runs with", got)
	}
	for _, args := range [][]string{
		{"/usr/bin/sleep", "60"},
		{"/usr/local/bin/ostiole", "--config-dir", "/etc/elsewhere", "serve"},
		{"/usr/local/bin/ostiole", "--config-dir", dir, "status"},
	} {
		cmdline(args...)
		if got := daemonArgs(dir, proc); got != nil {
			t.Errorf("%q taken for the daemon", args)
		}
	}
	if err := os.RemoveAll(pid); err != nil {
		t.Fatal(err)
	}
	if got := daemonArgs(dir, proc); got != nil {
		t.Errorf("a daemon that has gone, yet found %q", got)
	}
}

// A stopping daemon waits for its last writes, but not for ever.
func TestStopWaitsForTheLastWrites(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.DiscardHandler)
	var wg sync.WaitGroup
	wrote := false
	wg.Go(func() {
		time.Sleep(20 * time.Millisecond)
		wrote = true
	})
	if !waitFor(&wg, time.Minute, log) || !wrote {
		t.Error("returned before the write")
	}
	var stuck sync.WaitGroup
	block := make(chan struct{})
	defer close(block)
	stuck.Go(func() { <-block })
	started := time.Now()
	if waitFor(&stuck, 30*time.Millisecond, log) || time.Since(started) > 5*time.Second {
		t.Error("waited past the limit")
	}
}
