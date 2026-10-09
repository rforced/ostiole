package fwlog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"ostiole/internal/logfile"
	"ostiole/internal/model"
)

// The writer reads on from the last entry it wrote, across a ring that has
// wrapped round.
func TestAfterReadsOnAcrossAWrappedRing(t *testing.T) {
	t.Parallel()
	r := NewRing(4)
	now := time.Now()
	for i := range 6 {
		r.Add(Entry{Time: now, Prefix: string(rune('a' + i))})
	}
	buf := make([]Entry, 8)
	n, more := r.After(0, buf)
	if n != 4 || more || buf[0].Seq != 3 || buf[3].Seq != 6 {
		t.Errorf("after 0: %d, more %v, first %d", n, more, buf[0].Seq)
	}
	n, more = r.After(4, buf[:1])
	if n != 1 || !more || buf[0].Seq != 5 {
		t.Errorf("after 4, one: %d, more %v, %d", n, more, buf[0].Seq)
	}
	if n, _ := r.After(6, buf); n != 0 || r.Newest() != 6 {
		t.Errorf("after the newest: %d, newest %d", n, r.Newest())
	}
}

// What comes back from the files goes in oldest first with the numbers it
// was given, as much as the ring's size and age allow, and only into a
// ring that has taken nothing yet. Numbering goes on after the highest
// number in the files, even one that did not come back.
func TestRestoreKeepsTheNumbersAndTheNewest(t *testing.T) {
	t.Parallel()
	r := NewRing(3)
	r.Configure(3, 24*time.Hour)
	now := time.Now()
	entries := []Entry{
		{Seq: 7, Time: now.Add(-48 * time.Hour), Src: "old"},
		{Seq: 8, Time: now.Add(-3 * time.Minute), Src: "a"},
		{Seq: 8, Time: now.Add(-3 * time.Minute), Src: "again"},
		{Seq: 9, Time: now.Add(-2 * time.Minute), Src: "b"},
		{Seq: 10, Time: now.Add(-time.Minute), Src: "c"},
	}
	if err := r.Restore(entries, 12); err != nil {
		t.Fatal(err)
	}
	got := r.Recent(10)
	if len(got) != 3 || got[0].Src != "c" || got[0].Seq != 10 || got[2].Src != "a" || got[2].Seq != 8 {
		t.Errorf("restored = %+v", got)
	}
	r.Add(Entry{Time: now, Src: "new"})
	if got := r.Recent(1); got[0].Seq != 13 {
		t.Errorf("next = %+v", got[0])
	}
	if err := r.Restore(entries, 0); err == nil {
		t.Error("restored over entries already taken")
	}
	// Clearing keeps the numbers going.
	r.Clear()
	if n, _ := r.Held(); n != 0 {
		t.Errorf("held %d after clear", n)
	}
	r.Add(Entry{Time: now})
	if got := r.Recent(1); got[0].Seq != 14 {
		t.Errorf("after clear = %+v", got[0])
	}
	// Nothing back, but a number in the files still counts.
	empty := NewRing(3)
	if err := empty.Restore(nil, 40); err != nil {
		t.Fatal(err)
	}
	if empty.Add(Entry{Time: now}); empty.Newest() != 41 {
		t.Errorf("after an empty restore: %d", empty.Newest())
	}
}

func seqs(es []Entry) []uint64 {
	out := make([]uint64, len(es))
	for i, e := range es {
		out[i] = e.Seq
	}
	return out
}

// restore pushes entries with these numbers into a restorer of r sized for count.
func restore(t *testing.T, r *Ring, count int, newest uint64, numbers ...uint64) int {
	t.Helper()
	rs, err := r.Restorer(count)
	if err != nil {
		t.Fatal(err)
	}
	for i, seq := range numbers {
		rs.Push(Entry{Seq: seq, Time: time.Now(), RuleID: "lab-ssh", Zone: "lab", Src: fmt.Sprintf("192.0.2.%d", i%250+1)})
	}
	n, err := rs.Done(newest)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A number that does not rise above the last one pushed is dropped.
func TestARestorerDropsNumbersThatDoNotRise(t *testing.T) {
	t.Parallel()
	r := NewRing(10)
	if n := restore(t, r, 5, 0, 4, 5, 5, 3, 6); n != 3 {
		t.Errorf("kept %d", n)
	}
	got := r.Recent(0)
	if fmt.Sprint(seqs(got)) != "[6 5 4]" || got[0].Src != "192.0.2.5" || got[1].Src != "192.0.2.2" {
		t.Errorf("restored %+v", got)
	}
}

// Past the ring's size the oldest give way, and numbering goes on after the
// last pushed when it is above the newest the files gave.
func TestARestorerOverwritesTheOldestBeyondTheSize(t *testing.T) {
	t.Parallel()
	r := NewRing(3)
	if n := restore(t, r, 10, 2, 1, 2, 3, 4, 5, 6, 7); n != 3 {
		t.Errorf("kept %d", n)
	}
	if got := seqs(r.Recent(0)); fmt.Sprint(got) != "[7 6 5]" {
		t.Errorf("restored %v", got)
	}
	if r.Add(Entry{Time: time.Now()}); r.Newest() != 8 {
		t.Errorf("next %d", r.Newest())
	}
}

// A count short of what comes back grows the ring up to its size.
func TestARestorerGrowsPastAShortCount(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 10} {
		r := NewRing(2000)
		all := make([]uint64, 2500)
		for i := range all {
			all[i] = uint64(i + 1)
		}
		if n := restore(t, r, count, 0, all...); n != 2000 {
			t.Errorf("count %d: kept %d", count, n)
		}
		got := r.Recent(0)
		for i, e := range got {
			if e.Seq != uint64(2500-i) {
				t.Fatalf("count %d: place %d holds %d", count, i, e.Seq)
			}
		}
	}
}

// A size cut while the files are read holds when the ring takes them.
func TestARestorerKeepsToASizeCutWhileReading(t *testing.T) {
	t.Parallel()
	r := NewRing(10)
	rs, err := r.Restorer(10)
	if err != nil {
		t.Fatal(err)
	}
	for seq := range uint64(8) {
		rs.Push(Entry{Seq: seq + 1, Time: time.Now(), Src: "192.0.2.9"})
	}
	r.Configure(3, 0)
	if n, err := rs.Done(0); err != nil || n != 3 {
		t.Fatalf("kept %d (%v)", n, err)
	}
	if got := seqs(r.Recent(0)); fmt.Sprint(got) != "[8 7 6]" {
		t.Errorf("restored %v", got)
	}
	if r.Add(Entry{Time: time.Now()}); fmt.Sprint(seqs(r.Recent(0))) != "[9 8 7]" {
		t.Errorf("after an add %v", seqs(r.Recent(0)))
	}
}

// Once the ring has numbered an entry, a read-back neither starts nor ends.
func TestARestorerRefusesOnceAnEntryIsAdded(t *testing.T) {
	t.Parallel()
	r := NewRing(10)
	rs, err := r.Restorer(4)
	if err != nil {
		t.Fatal(err)
	}
	rs.Push(Entry{Seq: 5, Time: time.Now(), Src: "192.0.2.1"})
	r.Add(Entry{Time: time.Now(), Src: "192.0.2.2"})
	if _, err := rs.Done(9); err == nil {
		t.Error("done after an entry was added")
	}
	if got := r.Recent(0); len(got) != 1 || got[0].Seq != 1 || r.Newest() != 1 {
		t.Errorf("ring holds %+v, newest %d", got, r.Newest())
	}
	if _, err := r.Restorer(0); err == nil {
		t.Error("a restorer for a ring that has numbered an entry")
	}
}

// Written by the writer and read back, an entry is what it was, its number
// included.
func TestEntriesComeBackFromTheFiles(t *testing.T) {
	t.Parallel()
	r := NewRing(100)
	at := time.Now().UTC().Truncate(time.Second)
	icmp := uint8(8)
	want := Entry{Time: at, Prefix: "ostiole:r:web:accept:", RuleID: "web", Kind: "rule", Action: "accept",
		InIface: "eth0", Family: "ipv4", Proto: "icmp", Src: "192.0.2.1", Dst: "198.51.100.2", ICMPType: &icmp, Length: 84}
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	w := &logfile.Writer{Dir: t.TempDir(), Source: func() *model.Config { return cfg }, Log: slog.New(slog.DiscardHandler),
		Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil }}
	// Registered before anything arrives, as serve does.
	w.Add(r.Files(), logfile.ReadStats{})
	r.Add(want)
	var line []byte
	r.Files().Lines(0, 1, func(_ uint64, _ time.Time, l []byte) { line = append(line, l...) })
	if !strings.Contains(string(line), `"seq":1,`) {
		t.Errorf("line = %s", line)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w.Run(ctx)
	var got []Entry
	st, err := logfile.Stream(w.Dir, FileName, FileVersion, 10, time.Time{}, ParseLine, func(e Entry) { got = append(got, e) })
	if err != nil || st.Skipped != 0 || len(got) != 1 {
		t.Fatalf("read %+v, %+v, %v", got, st, err)
	}
	g := got[0]
	if g.Seq != 1 || !g.Time.Equal(at) || g.RuleID != "web" || g.ICMPType == nil || *g.ICMPType != 8 || g.Src != want.Src {
		t.Errorf("read back %+v", g)
	}
}
