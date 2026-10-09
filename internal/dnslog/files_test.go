package dnslog

import (
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"

	"ostiole/internal/model"
)

// A line names the lists, and reading it back interns them again, so the
// row shows the same lists after a restart.
func TestAnswersComeBackWithTheirListsByName(t *testing.T) {
	t.Parallel()
	l := onLog(t, 100, 7)
	now := time.Now().UTC().Truncate(time.Second)
	at(l, now.Add(-2*time.Minute), "ok.example", StatusOK, "10.0.0.2")
	at(l, now.Add(-time.Minute), "ads.example", StatusBlocked, "10.0.0.3", "hagezi", "oisd")
	buf := make([]Entry, 4)
	n, more := l.After(0, buf)
	if n != 2 || more || l.Newest() != 2 {
		t.Fatalf("after: %d, %v", n, more)
	}
	var lines [][]byte
	l.Files().Lines(0, 10, func(_ uint64, _ time.Time, line []byte) { lines = append(lines, slices.Clone(line)) })
	if len(lines) != 2 {
		t.Fatalf("lines = %s", lines)
	}
	want := `{"seq":2,"time":"` + now.Add(-time.Minute).Format(time.RFC3339) +
		`","client":"10.0.0.3","name":"ads.example","type":"A","status":"blocked","lists":["hagezi","oisd"]}`
	if string(lines[1]) != want {
		t.Errorf("line = %s\nwant   %s", lines[1], want)
	}

	var back []Stored
	for _, line := range lines {
		s, when, err := ParseLine(line)
		if err != nil || !when.Equal(s.Time) {
			t.Fatalf("%s: %v", line, err)
		}
		back = append(back, s)
	}
	fresh := New()
	fresh.Slog = slog.New(slog.DiscardHandler)
	if err := fresh.Restore(model.QueryLog{Enabled: true}, week, back, 5); err != nil {
		t.Fatal(err)
	}
	got, _ := query(t, fresh, Filter{})
	if len(got) != 2 || got[0].Seq != 2 || got[0].Name != "ads.example" ||
		!slices.Equal(fresh.ListNames(got[0].Lists), []string{"hagezi", "oisd"}) || got[1].Client != netip.MustParseAddr("10.0.0.2") {
		t.Errorf("restored = %+v", got)
	}
	// The totals and the counts per list are what came back, counted from
	// the oldest of it.
	total, blocked, oldest := fresh.Totals()
	counts, since := fresh.ListCounts()
	if total != 2 || blocked != 1 || !oldest.Equal(now.Add(-2*time.Minute)) || !since.Equal(oldest) ||
		counts["hagezi"] != (ListCount{Blocked: 1}) {
		t.Errorf("totals %d %d %v, counts %v since %v", total, blocked, oldest, counts, since)
	}
	if err := fresh.Restore(model.QueryLog{Enabled: true}, week, back, 0); err == nil {
		t.Error("restored over answers already taken")
	}
	// Numbering goes on after the highest number in the files.
	if fresh.Newest() != 5 {
		t.Errorf("newest %d", fresh.Newest())
	}
}

// Past sixty-four names an answer keeps its reason and loses the names
// that do not fit, as it would have arriving.
func TestRestoreBeyondSixtyFourLists(t *testing.T) {
	t.Parallel()
	var back []Stored
	now := time.Now()
	for i := range MaxLists + 1 {
		back = append(back, Stored{
			Seq: uint64(i + 1), Time: now, Name: "n.example", Type: 1, Status: StatusBlocked, Reason: ReasonList,
			ListNames: []string{fmt.Sprintf("list%d", i)},
		})
	}
	l := New()
	l.Slog = slog.New(slog.DiscardHandler)
	if err := l.Restore(model.QueryLog{Enabled: true}, week, back, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := query(t, l, Filter{})
	if got[0].Lists != 0 || got[0].Reason != ReasonList || len(l.Lists()) != MaxLists {
		t.Errorf("65th = %+v, %d lists", got[0], len(l.Lists()))
	}
}

// A line that does not say what it is is refused, so the reader skips and
// counts it.
func TestParseLineRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		`not json`,
		`{"time":"2026-09-27T00:00:00Z","client":"nope","name":"a","type":"A","status":"ok"}`,
		`{"time":"2026-09-27T00:00:00Z","client":"10.0.0.1","name":"a","type":"NOTATYPE","status":"ok"}`,
		`{"time":"2026-09-27T00:00:00Z","client":"10.0.0.1","name":"a","type":"A","status":"maybe"}`,
		`{"client":"10.0.0.1","name":"a","type":"A","status":"ok"}`,
	} {
		if _, _, err := ParseLine([]byte(line)); err == nil {
			t.Errorf("%s was read", line)
		}
	}
	s, _, err := ParseLine([]byte(`{"time":"2026-09-27T00:00:00Z","client":"10.0.0.1","name":"a","type":"65","status":"nxdomain","reason":"deny","answer":"::1"}`))
	if err != nil || s.Type != 65 || s.Status != StatusNXDomain || s.Reason != ReasonDeny || s.Answer != netip.IPv6Loopback() {
		t.Errorf("read %+v, %v", s, err)
	}
}
