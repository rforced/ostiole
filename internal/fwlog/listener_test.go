package fwlog

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/netnstest"
)

// lockedBuffer takes log lines from the listener's goroutines while the
// test reads them.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// start runs a listener on ring until it says it is running, and returns
// what it logs and a stop that waits for it to end.
func start(t *testing.T, ring *Ring) (*lockedBuffer, func()) {
	t.Helper()
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	l := &Listener{Ring: ring, Log: slog.New(slog.NewTextHandler(out, nil))}
	go func() { done <- l.Run(ctx) }()
	for begun := time.Now(); !strings.Contains(out.String(), "listener running"); time.Sleep(10 * time.Millisecond) {
		select {
		case err := <-done:
			t.Fatalf("listener stopped before it ran: %v", err)
		default:
		}
		if time.Since(begun) > 5*time.Second {
			t.Fatalf("listener never ran:\n%s", out.String())
		}
	}
	return out, func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoppingTheListenerLogsNoWarning(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	out, stop := start(t, NewRing(8))
	stop()
	if strings.Contains(out.String(), "level=WARN") {
		t.Errorf("stopping logged a warning:\n%s", out.String())
	}
}

// A packet a log statement sends to the group reaches the ring with the
// rule its prefix names, the interface it left by, and its ports.
func TestALoggedPacketReachesTheRing(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	netnstest.Dummy(t, "lan0", "192.0.2.1/24")
	netnstest.Ruleset(t, `table inet t {
	chain out { type filter hook output priority 0; udp dport 9 log prefix "ostiole:r:web:accept: " group 1; }
}`)
	ring := NewRing(8)
	_, stop := start(t, ring)
	defer stop()
	c, err := net.Dial("udp", "192.0.2.77:9")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	// The kernel hands logged packets over in batches, within a second.
	for begun := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		for _, e := range ring.Recent(8) {
			if e.RuleID != "web" {
				continue
			}
			if e.Kind != "rule" || e.Action != "accept" || e.OutIface != "lan0" || e.InIface != "" ||
				e.Proto != "udp" || e.Dst != "192.0.2.77" || e.DstPort != 9 || e.Family != "ipv4" {
				t.Errorf("entry = %+v", e)
			}
			return
		}
		if time.Since(begun) > 5*time.Second {
			t.Fatalf("nothing reached the ring: %+v", ring.Recent(8))
		}
	}
}
