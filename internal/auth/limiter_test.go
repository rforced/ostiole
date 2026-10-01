package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A host picks its IPv6 addresses from a /64, so walking through them is
// one client failing, not many.
func TestLimiterCountsAnIPv6ClientByItsPrefix(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l := newLimiter("", func() time.Time { return now })
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
	l := newLimiter("", func() time.Time { return now })
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

// The addresses that signed in outlive a restart, so a flood that runs
// across one, or an update in the middle of one, still lets them in.
func TestLimiterTrustOutlivesARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	clock := func() time.Time { return now }
	newLimiter(dir, clock).success("192.0.2.1")
	newLimiter(dir, clock).success("2001:db8:1:2::7")
	info, err := os.Stat(filepath.Join(dir, SignInsFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the file: %v, %v", info, err)
	}

	now = now.Add(time.Hour)
	l := newLimiter(dir, clock)
	for i := range MaxFailuresAll {
		l.failure(fmt.Sprintf("2001:db8:%x::1", i+100))
	}
	if l.blocked("192.0.2.1") || l.blocked("2001:db8:1:2::8") {
		t.Error("an address that signed in before the restart is shut out")
	}
	if !l.blocked("198.51.100.1") {
		t.Error("a stranger may still try")
	}

	// Trust lapses as it would have without the restart.
	now = now.Add(TrustedFor)
	l = newLimiter(dir, clock)
	for i := range MaxFailuresAll {
		l.failure(fmt.Sprintf("2001:db8:%x::1", i+200))
	}
	if !l.blocked("192.0.2.1") {
		t.Error("trust from more than TrustedFor ago survived a restart")
	}
}
