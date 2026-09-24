package cli

import (
	"os"
	"strconv"
	"strings"
	"testing"
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
