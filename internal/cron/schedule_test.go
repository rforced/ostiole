package cron

import (
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	when, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		t.Fatal(err)
	}
	return when
}

func TestParseAndMatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		expr    string
		when    string
		matches bool
	}{
		{"* * * * *", "2026-09-16 13:37", true},
		{"0 4 * * *", "2026-09-16 04:00", true},
		{"0 4 * * *", "2026-09-16 04:01", false},
		{"@daily", "2026-09-16 00:00", true},
		{"@hourly", "2026-09-16 13:00", true},
		{"*/15 * * * *", "2026-09-16 13:30", true},
		{"*/15 * * * *", "2026-09-16 13:31", false},
		{"0 2-4 * * *", "2026-09-16 03:00", true},
		{"0 2-4 * * *", "2026-09-16 05:00", false},
		{"0 0 1 * *", "2026-09-01 00:00", true},
		{"0 0 1 * *", "2026-09-02 00:00", false},
		{"0 0 * * wed", "2026-09-16 00:00", true}, // a Wednesday
		{"0 0 * * 3", "2026-09-16 00:00", true},
		{"0 0 * * sun", "2026-09-16 00:00", false},
		{"0 0 * jan,sep *", "2026-09-16 00:00", true},
		{"0 0 * feb *", "2026-09-16 00:00", false},
		{"5,10,15 * * * *", "2026-09-16 13:10", true},
		{"0 0 * * 7", "2026-09-20 00:00", true}, // Sunday is 0 and 7
		// Day of month and weekday are ORed, as in every other cron.
		{"0 0 13 * fri", "2026-09-16 00:00", false},
		{"0 0 16 * fri", "2026-09-16 00:00", true},
		{"0 0 13 * wed", "2026-09-16 00:00", true},
	}
	for _, tc := range cases {
		t.Run(tc.expr+" at "+tc.when, func(t *testing.T) {
			t.Parallel()
			s, err := Parse(tc.expr)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.expr, err)
			}
			if got := s.Matches(at(t, tc.when)); got != tc.matches {
				t.Errorf("Matches = %v, want %v", got, tc.matches)
			}
		})
	}
}

func TestParseRejectsNonsense(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{
		"", "   ", "* * * *", "* * * * * *", "@never",
		"60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8",
		"*/0 * * * *", "5-1 * * * *", "abc * * * *", "* * * * mon-",
		",", "1,,2 * * * *",
	} {
		t.Run(expr, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(expr); err == nil {
				t.Errorf("Parse(%q) was accepted", expr)
			}
		})
	}
}

func TestNextFindsTheFollowingRun(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ expr, from, want string }{
		"tonight":     {"0 4 * * *", "2026-09-16 13:37", "2026-09-17 04:00"},
		"later today": {"0 22 * * *", "2026-09-16 13:37", "2026-09-16 22:00"},
		"next minute": {"* * * * *", "2026-09-16 13:37", "2026-09-16 13:38"},
		"next Sunday": {"30 3 * * sun", "2026-09-16 13:37", "2026-09-20 03:30"},
		"a leap day":  {"0 0 29 2 *", "2026-09-16 13:37", "2028-02-29 00:00"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, err := Parse(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := s.Next(at(t, tc.from))
			if !ok {
				t.Fatalf("Next(%q) found nothing", tc.expr)
			}
			if !got.Equal(at(t, tc.want)) {
				t.Errorf("Next = %s, want %s", got.Format("2006-01-02 15:04"), tc.want)
			}
		})
	}
}

// A schedule that can never happen should say so rather than loop.
func TestNextGivesUpOnTheImpossible(t *testing.T) {
	t.Parallel()
	s, err := Parse("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if when, ok := s.Next(at(t, "2026-09-16 13:37")); ok {
		t.Errorf("Next = %s, but the 30th of February does not happen", when)
	}
}
