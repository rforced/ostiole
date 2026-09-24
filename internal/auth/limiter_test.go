package auth

import (
	"fmt"
	"testing"
	"time"
)

// A host picks its IPv6 addresses from a /64, so walking through them is
// one client failing, not many.
func TestLimiterCountsAnIPv6ClientByItsPrefix(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := newLimiter(func() time.Time { return now })
	for i := range MaxFailures {
		l.failure(fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if !l.blocked("2001:db8:1:2:ffff::1") {
		t.Error("another address in the same /64 may still try")
	}
	if l.blocked("2001:db8:1:3::1") {
		t.Error("the next /64 is blocked too")
	}
	for range MaxFailures {
		l.failure("::ffff:192.0.2.7")
	}
	if !l.blocked("192.0.2.7") {
		t.Error("an IPv4 address written as IPv6 is counted apart from itself")
	}
}

// Failures from everywhere at once shut out every address that has not
// signed in lately, for the rest of the window, and nobody else.
func TestLimiterShutsOutStrangersDuringAFlood(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := newLimiter(func() time.Time { return now })
	l.success("192.0.2.1")
	for i := range MaxFailuresAll {
		l.failure(fmt.Sprintf("2001:db8:%x::1", i))
	}
	if !l.blocked("198.51.100.1") {
		t.Error("an address that never signed in may still try")
	}
	if l.blocked("192.0.2.1") {
		t.Error("the address that signed in is shut out with the rest")
	}
	now = now.Add(FailureWindow + time.Second)
	if l.blocked("198.51.100.1") {
		t.Error("still shut out after the window")
	}
}
