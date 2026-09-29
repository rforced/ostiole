package fwlog

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/model"
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
	got, st, err := logfile.Read(w.Dir, FileName, FileVersion, 10, time.Time{}, ParseLine)
	if err != nil || st.Skipped != 0 || len(got) != 1 {
		t.Fatalf("read %+v, %+v, %v", got, st, err)
	}
	g := got[0]
	if g.Seq != 1 || !g.Time.Equal(at) || g.RuleID != "web" || g.ICMPType == nil || *g.ICMPType != 8 || g.Src != want.Src {
		t.Errorf("read back %+v", g)
	}
}
