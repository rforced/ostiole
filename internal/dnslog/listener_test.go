package dnslog

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/dnsblock"
	"ostiole/internal/model"
	"ostiole/internal/netnstest"
	"ostiole/internal/nft"
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

// start runs a listener feeding qlog until it says it is running, and
// returns what it logs and a stop that waits for it to end.
func start(t *testing.T, qlog *Log, names ...Names) (*lockedBuffer, func()) {
	t.Helper()
	out := &lockedBuffer{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	l := &Listener{Log: qlog, Slog: slog.New(slog.NewTextHandler(out, nil))}
	if len(names) > 0 {
		l.Names = names[0]
	}
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
	out, stop := start(t, New())
	stop()
	if strings.Contains(out.String(), "level=WARN") {
		t.Errorf("stopping logged a warning:\n%s", out.String())
	}
}

// An answer the resolver sends out through the rule the renderer writes
// reaches the log, with the client it went to and the name it answered.
func TestAnAnswerReachesTheLog(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	netnstest.Dummy(t, "lan0", "192.0.2.1/24")
	netnstest.Ruleset(t, fmt.Sprintf(`table inet t {
	chain out { type filter hook output priority 0; oifname "lan0" udp sport 53 log group %d; }
}`, nft.QueryLogGroup))
	qlog := New()
	qlog.Configure(model.QueryLog{Enabled: true, Entries: 10}, 24*time.Hour, dnsblock.Options{}, nil)
	_, stop := start(t, qlog)
	defer stop()

	resolver, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 53})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolver.Close() }()
	msg := build(t, answer{name: "example.com.", addresses: []netip.Addr{netip.MustParseAddr("93.184.215.14")}})
	if _, err := resolver.WriteToUDP(msg, &net.UDPAddr{IP: net.ParseIP("192.0.2.50"), Port: 40000}); err != nil {
		t.Fatal(err)
	}
	// The kernel hands logged packets over in batches, within a second.
	for begun := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		if entries, _ := query(t, qlog, Filter{}); len(entries) > 0 {
			e := entries[0]
			if e.Name != "example.com" || e.Client != netip.MustParseAddr("192.0.2.50") || e.Status != StatusOK ||
				e.Answer != netip.MustParseAddr("93.184.215.14") {
				t.Errorf("entry = %+v", e)
			}
			return
		}
		if time.Since(begun) > 5*time.Second {
			t.Fatal("nothing reached the log")
		}
	}
}

// names keeps what the listener tells it.
type names struct {
	mu  sync.Mutex
	got map[netip.Addr]string
}

func (n *names) Wanted() bool { return true }

func (n *names) Answered(client netip.Addr, name string, addrs []netip.Addr, _ time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range addrs {
		n.got[a] = client.String() + " " + name
	}
}

// With the query log off, an answer still names Traffic's destinations
// while they want it, and the log keeps nothing.
func TestAnAnswerNamesDestinationsWithTheLogOff(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	netnstest.Dummy(t, "lan0", "192.0.2.1/24")
	netnstest.Ruleset(t, fmt.Sprintf(`table inet t {
	chain out { type filter hook output priority 0; oifname "lan0" udp sport 53 log group %d; }
}`, nft.QueryLogGroup))
	qlog := New()
	want := &names{got: map[netip.Addr]string{}}
	_, stop := start(t, qlog, want)
	defer stop()
	resolver, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 53})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolver.Close() }()
	msg := build(t, answer{name: "example.com.", addresses: []netip.Addr{
		netip.MustParseAddr("93.184.215.14"), netip.MustParseAddr("2606:2800:21f:cb07:6820:80da:af6b:8b2c"),
	}})
	if _, err := resolver.WriteToUDP(msg, &net.UDPAddr{IP: net.ParseIP("192.0.2.50"), Port: 40000}); err != nil {
		t.Fatal(err)
	}
	for begun := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		want.mu.Lock()
		n := len(want.got)
		got := want.got[netip.MustParseAddr("2606:2800:21f:cb07:6820:80da:af6b:8b2c")]
		want.mu.Unlock()
		if n == 2 {
			if got != "192.0.2.50 example.com" {
				t.Errorf("named %q", got)
			}
			break
		}
		if time.Since(begun) > 5*time.Second {
			t.Fatal("nothing was named")
		}
	}
	if total, _, _ := qlog.Totals(); total != 0 {
		t.Errorf("the log kept %d answers while off", total)
	}
}
