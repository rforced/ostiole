package model

import (
	"testing"
	"time"
)

func TestLeaseDuration(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]time.Duration{
		"":    24 * time.Hour,
		"90s": 90 * time.Second,
		"45m": 45 * time.Minute,
		"12h": 12 * time.Hour,
		"2d":  48 * time.Hour,
		"1w":  7 * 24 * time.Hour,
	} {
		if got, ok := LeaseDuration(in); !ok || got != want {
			t.Errorf("LeaseDuration(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	// Nothing to take away from an expiry.
	for _, in := range []string{"infinite", "3600", "1y", "h", "9999999999999h", "99999999999999999999s"} {
		if got, ok := LeaseDuration(in); ok {
			t.Errorf("LeaseDuration(%q) = %v, want none", in, got)
		}
	}
}
