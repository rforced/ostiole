// Package cron runs the jobs an appliance needs on a schedule: the
// backups nobody remembers to take, the blocklists that go stale, and
// whatever else the operator wants run at four in the morning. It also
// reports the work Ostiole does on its own account, so one page answers
// "what does this box do while I am not looking".
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed five-field cron expression: minute, hour, day of
// month, month, day of week.
type Schedule struct {
	expr    string
	minute  field
	hour    field
	dom     field
	month   field
	dow     field
	domStar bool
	dowStar bool
}

// String returns the expression it was parsed from.
func (s Schedule) String() string { return s.expr }

// field is a set of allowed values, as a bitmask over 0-63.
type field uint64

func (f field) has(v int) bool { return f&(1<<uint(v)) != 0 }

// Shorthands people expect, because nobody remembers the field order for
// "every day at midnight".
var shorthands = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// Shorthands lists the accepted @-forms, for the UI to offer.
func Shorthands() []string {
	return []string{"@hourly", "@daily", "@weekly", "@monthly", "@yearly"}
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// Parse reads a cron expression. Both the five-field form and the
// familiar @daily shorthands are accepted.
func Parse(expr string) (Schedule, error) {
	original := strings.TrimSpace(expr)
	if original == "" {
		return Schedule{}, fmt.Errorf("a schedule is required")
	}
	text := original
	if strings.HasPrefix(text, "@") {
		expanded, ok := shorthands[strings.ToLower(text)]
		if !ok {
			return Schedule{}, fmt.Errorf("%q is not a schedule I know; try @daily or a five-field expression", original)
		}
		text = expanded
	}
	parts := strings.Fields(text)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("a schedule has five fields (minute hour day month weekday), got %d", len(parts))
	}
	s := Schedule{expr: original}
	var err error
	if s.minute, err = parseField(parts[0], 0, 59, nil); err != nil {
		return Schedule{}, fmt.Errorf("minute: %w", err)
	}
	if s.hour, err = parseField(parts[1], 0, 23, nil); err != nil {
		return Schedule{}, fmt.Errorf("hour: %w", err)
	}
	if s.dom, err = parseField(parts[2], 1, 31, nil); err != nil {
		return Schedule{}, fmt.Errorf("day of month: %w", err)
	}
	if s.month, err = parseField(parts[3], 1, 12, monthNames); err != nil {
		return Schedule{}, fmt.Errorf("month: %w", err)
	}
	if s.dow, err = parseField(parts[4], 0, 6, dayNames); err != nil {
		return Schedule{}, fmt.Errorf("weekday: %w", err)
	}
	s.domStar = parts[2] == "*"
	s.dowStar = parts[4] == "*"
	return s, nil
}

func parseField(spec string, lowest, highest int, names map[string]int) (field, error) {
	var out field
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, fmt.Errorf("empty value in %q", spec)
		}
		step := 1
		if base, stepText, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%q is not a step", stepText)
			}
			step = n
			part = base
		}
		lo, hi := lowest, highest
		if part != "*" {
			loText, hiText, isRange := strings.Cut(part, "-")
			var err error
			if lo, err = value(loText, lowest, highest, names); err != nil {
				return 0, err
			}
			hi = lo
			if isRange {
				if hi, err = value(hiText, lowest, highest, names); err != nil {
					return 0, err
				}
				if hi < lo {
					return 0, fmt.Errorf("%q counts backwards", part)
				}
			}
		}
		for v := lo; v <= hi; v += step {
			out |= 1 << uint(v)
		}
	}
	if out == 0 {
		return 0, fmt.Errorf("%q matches nothing", spec)
	}
	return out, nil
}

func value(text string, lowest, highest int, names map[string]int) (int, error) {
	text = strings.TrimSpace(text)
	if names != nil {
		if v, ok := names[strings.ToLower(text)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", text)
	}
	// Sunday is both 0 and 7 in every cron anyone has used.
	if lowest == 0 && highest == 6 && v == 7 {
		v = 0
	}
	if v < lowest || v > highest {
		return 0, fmt.Errorf("%d is outside %d-%d", v, lowest, highest)
	}
	return v, nil
}

// Matches reports whether t falls in this schedule, to the minute.
//
// Day of month and weekday are ORed when both are given, which is what
// every cron does and surprises everybody: "0 0 13 * fri" is the
// thirteenth *or* any Friday, not Friday the thirteenth.
func (s Schedule) Matches(t time.Time) bool {
	if !s.minute.has(t.Minute()) || !s.hour.has(t.Hour()) || !s.month.has(int(t.Month())) {
		return false
	}
	dom := s.dom.has(t.Day())
	dow := s.dow.has(int(t.Weekday()))
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	}
	return dom || dow
}

// Next is when this schedule fires after t. It searches minute by minute
// and gives up after four years, which covers the 29th of February.
func (s Schedule) Next(t time.Time) (time.Time, bool) {
	next := t.Truncate(time.Minute).Add(time.Minute)
	limit := next.AddDate(4, 0, 0)
	for next.Before(limit) {
		if s.Matches(next) {
			return next, true
		}
		// Skip a whole day when the date cannot match, rather than
		// stepping through 1440 minutes of it.
		if !s.month.has(int(next.Month())) || !s.dayMatches(next) {
			next = time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, next.Location()).AddDate(0, 0, 1)
			continue
		}
		next = next.Add(time.Minute)
	}
	return time.Time{}, false
}

func (s Schedule) dayMatches(t time.Time) bool {
	dom := s.dom.has(t.Day())
	dow := s.dow.has(int(t.Weekday()))
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	}
	return dom || dow
}
