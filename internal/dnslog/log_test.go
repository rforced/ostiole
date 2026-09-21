package dnslog

import (
	"fmt"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
)

// onLog returns a log switched on with the given bounds and no index: the
// ring is what these tests are about.
func onLog(t *testing.T, entries, hours int) *Log {
	t.Helper()
	l := New()
	l.Slog = slog.New(slog.DiscardHandler)
	l.Configure(model.QueryLog{Enabled: true, Entries: entries, Hours: hours}, dnsblock.Options{}, nil)
	return l
}

func at(l *Log, when time.Time, name string, status Status, client string, lists ...string) {
	l.Add(Entry{
		Time: when, Name: name, Type: 1, Status: status,
		Client: netip.MustParseAddr(client),
	}, lists)
}

func TestAddEvictsBySize(t *testing.T) {
	t.Parallel()
	l := onLog(t, 3, 24)
	now := time.Now()
	for i := range 5 {
		at(l, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("n%d.example", i), StatusOK, "10.0.0.1")
	}
	got, total := l.Query(Filter{})
	if len(got) != 3 || total != 3 {
		t.Fatalf("held %d, total %d", len(got), total)
	}
	// Newest first, and the two oldest are gone.
	for i, want := range []string{"n4.example", "n3.example", "n2.example"} {
		if got[i].Name != want {
			t.Errorf("row %d = %s, want %s", i, got[i].Name, want)
		}
	}
}

func TestAddEvictsByAge(t *testing.T) {
	t.Parallel()
	l := onLog(t, 100, 1)
	now := time.Now()
	at(l, now.Add(-90*time.Minute), "old.example", StatusOK, "10.0.0.1")
	at(l, now.Add(-30*time.Minute), "recent.example", StatusOK, "10.0.0.1")
	got, total := l.Query(Filter{})
	if total != 1 || len(got) != 1 || got[0].Name != "recent.example" {
		t.Fatalf("got %+v, total %d", got, total)
	}
	// A quiet log does not show stale rows either: nothing was added, but
	// the retention cutoff moves on its own.
	l2 := onLog(t, 100, 1)
	at(l2, time.Now().Add(-59*time.Minute), "nearly.example", StatusOK, "10.0.0.1")
	if _, total := l2.Query(Filter{Since: time.Now().Add(-30 * time.Minute)}); total != 0 {
		t.Errorf("since filter matched %d", total)
	}
}

func TestQueryFilters(t *testing.T) {
	t.Parallel()
	l := onLog(t, 100, 24)
	now := time.Now()
	at(l, now, "ads.example.com", StatusBlocked, "10.0.0.1", "one", "two")
	at(l, now, "tracker.example.net", StatusBlocked, "10.0.0.2", "one")
	at(l, now, "www.example.com", StatusOK, "10.0.0.1")
	at(l, now, "missing.example.org", StatusNXDomain, "10.0.0.2")

	cases := []struct {
		what string
		f    Filter
		want int
	}{
		{"everything", Filter{}, 4},
		{"by name", Filter{Name: "example.com"}, 2},
		{"by name, uppercase", Filter{Name: "EXAMPLE.NET"}, 1},
		{"by status", Filter{Status: StatusBlocked}, 2},
		{"by status, answered", Filter{Status: StatusOK}, 1},
		{"by client", Filter{Clients: []netip.Addr{netip.MustParseAddr("10.0.0.1")}}, 2},
		{"by two clients", Filter{Clients: []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}}, 4},
		{"by a client nobody is", Filter{Clients: []netip.Addr{netip.MustParseAddr("10.9.9.9")}}, 0},
		{"by list", Filter{List: "one"}, 2},
		{"by the other list", Filter{List: "two"}, 1},
		{"by a list nobody has", Filter{List: "three"}, 0},
		{"by type", Filter{Type: 1}, 4},
		{"by a type nobody asked", Filter{Type: 65}, 0},
		{"by name and status together", Filter{Name: "example", Status: StatusBlocked}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			got, total := l.Query(tc.f)
			if total != tc.want || len(got) != tc.want {
				t.Errorf("got %d rows, total %d, want %d", len(got), total, tc.want)
			}
		})
	}
}

func TestQueryPages(t *testing.T) {
	t.Parallel()
	l := onLog(t, 100, 24)
	now := time.Now()
	for i := range 10 {
		at(l, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("n%d.example", i), StatusOK, "10.0.0.1")
	}
	first, total := l.Query(Filter{Limit: 4})
	if total != 10 || len(first) != 4 || first[0].Name != "n9.example" {
		t.Fatalf("first page %+v, total %d", first, total)
	}
	next, total := l.Query(Filter{Limit: 4, Before: first[len(first)-1].Seq})
	// The total is what matches, not what the page holds, so a reader
	// knows there is more without asking for it.
	if total != 10 || len(next) != 4 || next[0].Name != "n5.example" {
		t.Fatalf("second page %+v, total %d", next, total)
	}
	last, _ := l.Query(Filter{Limit: 4, Before: next[len(next)-1].Seq})
	if len(last) != 2 || last[0].Name != "n1.example" {
		t.Fatalf("last page %+v", last)
	}
}

func TestSummary(t *testing.T) {
	t.Parallel()
	l := onLog(t, 100, 24)
	now := time.Now()
	for range 3 {
		at(l, now, "ads.example.com", StatusBlocked, "10.0.0.1", "one")
	}
	at(l, now, "www.example.com", StatusOK, "10.0.0.1")
	at(l, now, "www.example.com", StatusOK, "10.0.0.2")

	s := l.Summary(10)
	if s.Total != 5 || s.Blocked != 3 || s.Clients != 2 {
		t.Fatalf("summary = %+v", s)
	}
	if len(s.TopNames) != 2 || s.TopNames[0].Name != "ads.example.com" || s.TopNames[0].Count != 3 {
		t.Errorf("top names = %+v", s.TopNames)
	}
	if len(s.TopBlocked) != 1 || s.TopBlocked[0].Name != "ads.example.com" {
		t.Errorf("top blocked = %+v", s.TopBlocked)
	}
	if len(s.TopClients) != 2 || s.TopClients[0].Total != 4 || s.TopClients[0].Blocked != 3 {
		t.Errorf("top clients = %+v", s.TopClients)
	}
}

// The counts say what each list is earning its place with, so they have to
// outlive the entries they were counted from.
func TestListCountsOutliveEviction(t *testing.T) {
	t.Parallel()
	l := onLog(t, 2, 24)
	now := time.Now()
	at(l, now, "ads.example.com", StatusBlocked, "10.0.0.1", "one", "two")
	at(l, now, "tracker.example.net", StatusBlocked, "10.0.0.1", "one")
	at(l, now, "other.example.net", StatusBlocked, "10.0.0.1", "two")
	at(l, now, "www.example.com", StatusOK, "10.0.0.1")

	counts, since := l.ListCounts()
	if since.IsZero() {
		t.Error("no since")
	}
	if got := counts["one"]; got.Blocked != 2 || got.Alone != 1 {
		t.Errorf("one = %+v, want 2 blocked and 1 alone", got)
	}
	if got := counts["two"]; got.Blocked != 2 || got.Alone != 1 {
		t.Errorf("two = %+v, want 2 blocked and 1 alone", got)
	}
	// Two of the four entries are gone, but the counts are not.
	if _, total := l.Query(Filter{}); total != 2 {
		t.Errorf("held %d", total)
	}
}

func TestListNamesAndInternOverflow(t *testing.T) {
	t.Parallel()
	l := onLog(t, 100, 24)
	now := time.Now()
	var many []string
	for i := range MaxLists + 4 {
		many = append(many, fmt.Sprintf("list%d", i))
	}
	at(l, now, "ads.example.com", StatusBlocked, "10.0.0.1", many...)
	got, _ := l.Query(Filter{})
	names := l.ListNames(got[0].Lists)
	if len(names) != MaxLists {
		t.Fatalf("attributed %d lists, want %d", len(names), MaxLists)
	}
	if names[0] != "list0" || names[MaxLists-1] != fmt.Sprintf("list%d", MaxLists-1) {
		t.Errorf("names = %v", names)
	}
	// The ones past the table are not attributed, and not counted either.
	counts, _ := l.ListCounts()
	if _, ok := counts[fmt.Sprintf("list%d", MaxLists)]; ok {
		t.Error("a list past the table was counted")
	}
	if l.ListNames(0) != nil {
		t.Error("an unblocked entry named a list")
	}
}

func TestClearKeepsTheLogOn(t *testing.T) {
	t.Parallel()
	l := onLog(t, 10, 24)
	at(l, time.Now(), "ads.example.com", StatusBlocked, "10.0.0.1", "one")
	l.Clear()
	if _, total := l.Query(Filter{}); total != 0 {
		t.Errorf("held %d after clear", total)
	}
	if counts, _ := l.ListCounts(); len(counts) != 0 {
		t.Errorf("counts = %v after clear", counts)
	}
	if !l.Enabled() {
		t.Error("clear switched the log off")
	}
	at(l, time.Now(), "ads.example.com", StatusBlocked, "10.0.0.1", "one")
	if _, total := l.Query(Filter{}); total != 1 {
		t.Errorf("held %d after clear and one answer", total)
	}
}

// Switching the log off is the privacy promise the tab makes, so it has to
// empty the ring rather than stop adding to it.
func TestConfigureOffClearsAndGatesAdd(t *testing.T) {
	t.Parallel()
	l := onLog(t, 10, 24)
	at(l, time.Now(), "ads.example.com", StatusBlocked, "10.0.0.1", "one")
	l.Configure(model.QueryLog{}, dnsblock.Options{}, nil)
	if l.Enabled() {
		t.Fatal("still on")
	}
	if _, total := l.Query(Filter{}); total != 0 {
		t.Errorf("held %d after being switched off", total)
	}
	at(l, time.Now(), "ads.example.com", StatusBlocked, "10.0.0.1", "one")
	if _, total := l.Query(Filter{}); total != 0 {
		t.Errorf("kept %d while off", total)
	}
	if counts, _ := l.ListCounts(); len(counts) != 0 {
		t.Errorf("counts = %v while off", counts)
	}
}

// Making the ring smaller keeps the newest answers, making it bigger keeps
// them all.
func TestConfigureResizes(t *testing.T) {
	t.Parallel()
	l := onLog(t, 10, 24)
	now := time.Now()
	for i := range 6 {
		at(l, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("n%d.example", i), StatusOK, "10.0.0.1")
	}
	l.Configure(model.QueryLog{Enabled: true, Entries: 3, Hours: 24}, dnsblock.Options{}, nil)
	got, total := l.Query(Filter{})
	if total != 3 || got[0].Name != "n5.example" || got[2].Name != "n3.example" {
		t.Fatalf("after shrinking: %+v, total %d", got, total)
	}
	l.Configure(model.QueryLog{Enabled: true, Entries: 20, Hours: 24}, dnsblock.Options{}, nil)
	if _, total := l.Query(Filter{}); total != 3 {
		t.Errorf("after growing: total %d", total)
	}
	at(l, now.Add(time.Minute), "later.example", StatusOK, "10.0.0.1")
	if _, total := l.Query(Filter{}); total != 4 {
		t.Errorf("after growing and one more: total %d", total)
	}
}

func TestSubscribeAndDropped(t *testing.T) {
	t.Parallel()
	l := onLog(t, 10, 24)
	ch, cancel := l.Subscribe(1)
	at(l, time.Now(), "ads.example.com", StatusBlocked, "10.0.0.1", "one")
	select {
	case e := <-ch:
		if e.Name != "ads.example.com" {
			t.Errorf("streamed %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing streamed")
	}
	// A subscriber that does not read loses entries rather than stalling
	// the listener.
	for range 5 {
		at(l, time.Now(), "www.example.com", StatusOK, "10.0.0.1")
	}
	if l.Dropped() == 0 {
		t.Error("a full subscriber dropped nothing")
	}
	cancel()
	for len(ch) > 0 {
		<-ch
	}
	at(l, time.Now(), "www.example.com", StatusOK, "10.0.0.1")
	select {
	case e := <-ch:
		t.Errorf("still subscribed: %+v", e)
	default:
	}
}

// The watcher runs every few seconds, so the index must not be rebuilt on
// every pass: on this router that is 3.3 million names.
func TestIndexRebuildsOnlyWhenTheInputsChange(t *testing.T) {
	t.Parallel()
	l := New()
	o := dnsblock.Options{Enabled: true, Lists: []string{"one"}, Max: 1000}
	if !l.stale(o) {
		t.Fatal("a log that has never indexed is not stale")
	}
	l.tried, l.triedAt = o, time.Now()
	l.idx.Store(&Index{})
	if l.stale(o) {
		t.Error("rebuilt for inputs it already has")
	}
	raised := o
	raised.Max = 2000
	if l.stale(raised) {
		t.Error("rebuilt because the ceiling on merged names moved")
	}
	more := o
	more.Lists = []string{"one", "two"}
	if !l.stale(more) {
		t.Error("did not rebuild for a list that was switched on")
	}
	// A build that failed leaves no index. Retry it a minute apart, not
	// every few seconds.
	l.idx.Store(nil)
	if l.stale(o) {
		t.Error("retried a failed build at once")
	}
	l.triedAt = time.Now().Add(-2 * RetryIndex)
	if !l.stale(o) {
		t.Error("never retried a failed build")
	}
}

// The log follows the configuration the router is running rather than
// being written by an apply, so a revert switches it off without the
// rollback knowing it exists.
func TestWatcherFollowsTheRunningConfiguration(t *testing.T) {
	t.Parallel()
	on := &model.Config{}
	on.Services.DNS.Enabled = true
	on.Services.DNS.QueryLog = model.QueryLog{Enabled: true}
	cfg := on

	l := New()
	l.Slog = slog.New(slog.DiscardHandler)
	w := &Watcher{Log: l, Source: func() *model.Config { return cfg }}
	w.tick()
	if !l.Enabled() {
		t.Fatal("the log did not follow a configuration that asks for it")
	}
	at(l, time.Now(), "ads.example.com", StatusBlocked, "10.0.0.1", "one")

	// What a revert leaves behind.
	cfg = &model.Config{}
	w.tick()
	if l.Enabled() {
		t.Error("still on after the configuration stopped asking")
	}
	if _, total := l.Query(Filter{}); total != 0 {
		t.Errorf("held %d after the configuration stopped asking", total)
	}

	// A router with nothing saved keeps no log either.
	cfg = nil
	w.tick()
	if l.Enabled() {
		t.Error("on with no configuration at all")
	}
}

// The ring is not allocated whole: a ceiling of a million on a router that
// has answered a thousand costs a thousand. It doubles as answers arrive,
// keeps its order across each growth and across wrapping, and the running
// totals the dashboard reads agree with what a query finds.
func TestRingGrowsOnDemandAndKeepsTotals(t *testing.T) {
	t.Parallel()
	l := onLog(t, 5000, 24)
	if len(l.ring) != initialRing {
		t.Fatalf("a fresh ring has %d places, want %d", len(l.ring), initialRing)
	}
	now := time.Now()
	for i := range 3000 {
		st := StatusOK
		if i%3 == 0 {
			st = StatusBlocked
		}
		at(l, now.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("n%d.example", i), st, "10.0.0.1", "one")
	}
	if len(l.ring) != 4096 {
		t.Errorf("after 3000 answers the ring has %d places, want 4096", len(l.ring))
	}
	total, blocked, oldest := l.Totals()
	if total != 3000 || blocked != 1000 || !oldest.Equal(now) {
		t.Fatalf("totals = %d/%d since %v", total, blocked, oldest)
	}
	// Past the ceiling it wraps and the oldest go, counts included.
	for i := 3000; i < 7000; i++ {
		at(l, now.Add(time.Duration(i)*time.Millisecond), fmt.Sprintf("n%d.example", i), StatusOK, "10.0.0.1")
	}
	if len(l.ring) != 5000 {
		t.Errorf("a full ring has %d places, want the ceiling 5000", len(l.ring))
	}
	got, matched := l.Query(Filter{Limit: 5000})
	total, blocked, _ = l.Totals()
	if matched != 5000 || total != 5000 || len(got) != 5000 {
		t.Fatalf("query %d, totals %d, held %d", matched, total, len(got))
	}
	// Answers 2000..2999 are the oldest left, and every third was blocked.
	if blocked != 333 {
		t.Errorf("blocked = %d, want 333", blocked)
	}
	for i, e := range got {
		if want := fmt.Sprintf("n%d.example", 6999-i); e.Name != want {
			t.Fatalf("row %d = %s, want %s", i, e.Name, want)
		}
	}
	// Shrinking keeps the newest and the counts follow.
	l.Configure(model.QueryLog{Enabled: true, Entries: 10, Hours: 24}, dnsblock.Options{}, nil)
	if total, blocked, _ := l.Totals(); total != 10 || blocked != 0 {
		t.Errorf("after shrinking: %d/%d", total, blocked)
	}
}
