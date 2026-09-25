package model

import (
	"strconv"
	"time"
)

// LeaseDuration reads a lease time as dnsmasq does, 24h or 2d, with empty
// meaning DefaultLeaseTime. An infinite lease has no length, so it says
// false, as does anything validation refuses.
func LeaseDuration(s string) (time.Duration, bool) {
	if s == "" {
		s = DefaultLeaseTime
	}
	if s == "infinite" || !leaseTimeRe.MatchString(s) {
		return 0, false
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 's':
		unit = time.Second
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	}
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil || n > int64(1<<63-1)/int64(unit) {
		return 0, false
	}
	return time.Duration(n) * unit, true
}
