package services

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A failed verb names the step and what systemctl said, and nothing
// trails the error when systemctl said nothing.
func TestSystemctlNamesTheStepAndTheOutput(t *testing.T) {
	t.Parallel()
	const unit = "ostiole-example.service"
	failed := errors.New("exit status 1")
	for _, tc := range []struct{ out, want string }{
		{"Failed to disable unit: Unit file " + unit + " does not exist.\n", "stop " + unit + ": exit status 1: Failed to disable unit: Unit file " + unit + " does not exist."},
		{"", "stop " + unit + ": exit status 1"},
		{" \n", "stop " + unit + ": exit status 1"},
	} {
		var ran []string
		run := commanderFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
			ran = append([]string{name}, args...)
			return []byte(tc.out), failed
		})
		err := systemctl(t.Context(), run, "stop "+unit, "disable", "--now", unit)
		if err == nil || err.Error() != tc.want || !errors.Is(err, failed) {
			t.Errorf("output %q: err = %v, want %q", tc.out, err, tc.want)
		}
		if got := strings.Join(ran, " "); got != "systemctl disable --now "+unit {
			t.Errorf("ran %q", got)
		}
	}
	ok := commanderFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("said something"), nil
	})
	if err := systemctl(t.Context(), ok, "stop "+unit, "disable", "--now", unit); err != nil {
		t.Errorf("a verb that worked failed: %v", err)
	}
}
