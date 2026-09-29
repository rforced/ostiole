package traffic

import (
	"net/netip"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// destRouter is a test router recording destinations.
func destRouter(t *testing.T) *router {
	t.Helper()
	r := newRouter(t)
	r.cfg.Traffic.Destinations = model.TrafficDestinations{Enabled: true}
	r.c.follow(r.now)
	r.step(0)
	return r
}

func rowsOf(r *router, span time.Duration, device string) map[string]DestinationRow {
	rows, _, _ := r.c.Destinations(span, device)
	out := map[string]DestinationRow{}
	for _, d := range rows {
		out[d.Device+" "+d.Destination] = d
	}
	return out
}

// A connection is keyed by the name its device asked for when it is first
// counted, else by the address, and stays under it; its bytes land in the
// hour's row with the connections counted once an hour.
func TestDestinationsTakeTheNameAskedFor(t *testing.T) {
	t.Parallel()
	r := destRouter(t)
	web := netip.MustParseAddr("198.51.100.7")
	r.c.Names().Answered(netip.MustParseAddr("10.0.0.5"), "example.com", []netip.Addr{web, netip.MustParseAddr("2001:db8::7")}, r.now)
	r.step(5*time.Second,
		tcp(1, "10.0.0.5", "198.51.100.7", 443, 100, 1000),
		tcp(2, "10.0.0.5", "198.51.100.7", 443, 10, 20),
		tcp(3, "10.0.0.5", "203.0.113.9", 22, 1, 2))
	// The name changes hands; the connection keeps the one it started with.
	r.c.Names().Answered(netip.MustParseAddr("10.0.0.5"), "other.example", []netip.Addr{web}, r.now)
	r.step(5*time.Second,
		tcp(1, "10.0.0.5", "198.51.100.7", 443, 200, 3000),
		tcp(2, "10.0.0.5", "198.51.100.7", 443, 10, 20),
		tcp(3, "10.0.0.5", "203.0.113.9", 22, 1, 2))
	got := rowsOf(r, time.Hour, "")
	ex := got["10.0.0.5 example.com"]
	if ex.Up != 210 || ex.Down != 3020 || ex.Connections != 2 || ex.Port != 443 || ex.Protocol != "tcp" || ex.Address != "198.51.100.7" {
		t.Errorf("example.com = %+v", ex)
	}
	if ssh := got["10.0.0.5 203.0.113.9"]; ssh.Up != 1 || ssh.Port != 22 {
		t.Errorf("by address = %+v", ssh)
	}
	if _, ok := got["10.0.0.5 other.example"]; ok {
		t.Error("a counted connection moved to a later name")
	}
	// Narrowed to one device, and a window from before counting is the
	// same rows.
	if n := len(rowsOf(r, 7*24*time.Hour, "10.0.0.5")); n != 2 {
		t.Errorf("%d rows for the device", n)
	}
	if n := len(rowsOf(r, time.Hour, "10.0.0.9")); n != 0 {
		t.Errorf("%d rows for nobody", n)
	}
	// Between two devices, each has a row for the other, on the port that
	// answered.
	r.step(5*time.Second, tcp(4, "10.0.2.7", "10.0.0.5", 8080, 5, 6))
	got = rowsOf(r, time.Hour, "")
	if a, b := got["10.0.2.7 10.0.0.5"], got["10.0.0.5 10.0.2.7"]; a.Up != 5 || a.Down != 6 || a.Port != 8080 ||
		b.Up != 6 || b.Down != 5 || b.Port != 8080 {
		t.Errorf("device to device = %+v and %+v", a, b)
	}
}

// Hours close as the clock passes them, a new hour counts the connection
// again, and the rows keep to their bounds.
func TestDestinationRowsRollByTheHourAndKeepTheirBounds(t *testing.T) {
	t.Parallel()
	r := destRouter(t)
	r.step(5*time.Second, tcp(1, "10.0.0.5", "198.51.100.7", 443, 10, 10))
	r.step(time.Hour, tcp(1, "10.0.0.5", "198.51.100.7", 443, 20, 20))
	rows, held, oldest := r.c.Destinations(3*time.Hour, "")
	if len(rows) != 1 || rows[0].Connections != 2 || rows[0].Up != 20 || held != 2 || oldest.IsZero() {
		t.Fatalf("rows = %+v, held %d", rows, held)
	}
	// Past the days kept, the old hours go.
	r.cfg.Traffic.Destinations.Days = 1
	r.c.follow(r.now)
	r.step(26*time.Hour, tcp(1, "10.0.0.5", "198.51.100.7", 443, 30, 30))
	if _, held, _ := r.c.Destinations(72*time.Hour, ""); held != 1 {
		t.Errorf("held %d after a day and more", held)
	}
	// Past the entries kept, the oldest go.
	r.cfg.Traffic.Destinations.Entries = 2
	r.c.follow(r.now)
	r.step(time.Hour,
		tcp(1, "10.0.0.5", "198.51.100.7", 443, 40, 40),
		tcp(4, "10.0.0.5", "198.51.100.8", 443, 1, 1),
		tcp(5, "10.0.0.5", "198.51.100.9", 443, 1, 1))
	if _, held, _ := r.c.Destinations(72*time.Hour, ""); held != 2 {
		t.Errorf("held %d past two entries", held)
	}
}

// Off, destinations and the names are forgotten, and nothing is recorded.
func TestDestinationsOffForgetThem(t *testing.T) {
	t.Parallel()
	r := destRouter(t)
	r.step(5*time.Second, tcp(1, "10.0.0.5", "198.51.100.7", 443, 10, 10))
	r.cfg.Traffic.Destinations.Enabled = false
	r.c.follow(r.now)
	if r.c.Names().Wanted() {
		t.Error("answers kept while off")
	}
	r.step(5*time.Second, tcp(1, "10.0.0.5", "198.51.100.7", 443, 20, 20))
	if rows, held, _ := r.c.Destinations(time.Hour, ""); len(rows) != 0 || held != 0 {
		t.Errorf("rows while off = %+v", rows)
	}
	if got := r.totals()["10.0.0.5"]; got.Up != 20 {
		t.Errorf("the device stopped counting: %+v", got)
	}
}

// Every address of an answer names it, for a day, and the oldest go past
// the bound.
func TestNamesKeepADayOfAnswers(t *testing.T) {
	t.Parallel()
	var n Names
	client := netip.MustParseAddr("10.0.0.5")
	a4, a6 := netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("2001:db8::7")
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	n.Answered(client, "example.com", []netip.Addr{a4}, at)
	if n.lookup(client, a4) != "" {
		t.Error("kept while not wanted")
	}
	n.set(true)
	n.Answered(client, "example.com", []netip.Addr{a4, a6}, at)
	if n.lookup(client, a4) != "example.com" || n.lookup(client, a6) != "example.com" {
		t.Error("an answer's addresses are not named")
	}
	if n.lookup(netip.MustParseAddr("10.0.0.6"), a4) != "" {
		t.Error("another client's answer names it")
	}
	n.Answered(client, "later.example", []netip.Addr{netip.MustParseAddr("198.51.100.8")}, at.Add(25*time.Hour))
	if n.lookup(client, a4) != "" {
		t.Error("kept past a day")
	}
	for i := range maxNames + 10 {
		addr := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		n.Answered(client, "bulk.example", []netip.Addr{addr}, at.Add(26*time.Hour))
	}
	if len(n.m) > maxNames {
		t.Errorf("%d names past the bound", len(n.m))
	}
	n.set(false)
	if n.lookup(client, netip.AddrFrom4([4]byte{10, 1, 134, 159})) != "" {
		t.Error("kept once switched off")
	}
}
