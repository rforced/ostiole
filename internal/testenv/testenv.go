// Package testenv lets a test say that the machine it runs on lacks what
// it needs: a tool, a kernel module, leave to make a namespace.
package testenv

import (
	"os"
	"testing"
)

// require is set where every test is expected to run, as in CI: there a
// test the machine cannot run fails instead of skipping, so a runner that
// loses a tool cannot quietly stop testing what needed it.
const require = "TESTENV_REQUIRE"

// Unavailable skips a test this machine cannot run, or fails it where
// TESTENV_REQUIRE is set.
func Unavailable(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv(require) != "" {
		t.Fatalf(format+" (and "+require+" says it must run here)", args...)
	}
	t.Skipf(format, args...)
}
