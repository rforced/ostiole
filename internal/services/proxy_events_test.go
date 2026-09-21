package services

import (
	"testing"
	"time"
)

// A connector writes the transaction's time in whatever unit it likes;
// coraza-caddy writes nanoseconds, and a value read as seconds lands
// tens of thousands of years from now.
func TestAuditTimeReadsTheUnitByMagnitude(t *testing.T) {
	t.Parallel()
	want := time.Unix(1789939819, 0).UTC()
	fallback := time.Unix(1, 0).UTC()
	for name, unix := range map[string]int64{
		"nanoseconds":  1789939819236948730,
		"microseconds": 1789939819236948,
		"milliseconds": 1789939819236,
		"seconds":      1789939819,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := auditTime("", unix, fallback).Truncate(time.Second); !got.Equal(want) {
				t.Errorf("%s = %v, want %v", name, got, want)
			}
		})
	}

	// Without a number the written form is read, in either shape.
	for _, written := range []string{"2026/09/20 17:30:19", "20/Sep/2026:17:30:19 +0000"} {
		if got := auditTime(written, 0, fallback); got.Year() != 2026 || got.Month() != time.September {
			t.Errorf("%q parsed as %v", written, got)
		}
	}
	// And with neither, the journal's own timestamp stands.
	if got := auditTime("nonsense", 0, fallback); !got.Equal(fallback) {
		t.Errorf("fallback = %v", got)
	}
}
