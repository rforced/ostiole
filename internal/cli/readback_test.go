package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/wafevent"
	"github.com/rforced/ostiole/internal/waflog"
)

// A day of the firewall log as 1.8.0 wrote it, before lines kept their
// numbers, comes back numbered in order, and what arrives next is numbered
// after it and written in the new format.
func TestTheFirewallLogComesBackNumberedFromAnOlderRelease(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var file bytes.Buffer
	zw := gzip.NewWriter(&file)
	zw.Comment = "ostiole firewall 1"
	for i := range 3 {
		fmt.Fprintf(zw, `{"time":%q,"prefix":"ostiole:r:web:drop:","src":"192.0.2.%d"}`+"\n",
			at.Add(time.Duration(i)*time.Second).Format(time.RFC3339), i)
	}
	_ = zw.Close()
	day := filepath.Join(dir, fwlog.FileName, at.Format("2006-01-02")+".jsonl.gz")
	if err := os.MkdirAll(filepath.Dir(day), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(day, file.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	log := slog.New(slog.DiscardHandler)
	files := &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: log,
		Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil }}
	ring := fwlog.NewRing(100)
	readFirewallLog(cfg, ring, files, log)

	got := ring.Recent(10)
	if len(got) != 3 || got[0].Seq != 3 || got[0].Src != "192.0.2.2" || got[2].Seq != 1 {
		t.Fatalf("read back %+v", got)
	}
	ring.Add(fwlog.Entry{Time: time.Now(), Src: "198.51.100.1"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files.Run(ctx)
	back, st, err := logfile.Read(dir, fwlog.FileName, fwlog.FileVersion, 10, time.Time{}, fwlog.ParseLine)
	if err != nil || len(st.Unknown) != 0 || len(back) != 4 || back[3].Seq != 4 || back[0].Seq != 1 {
		t.Errorf("files hold %+v, %+v (%v)", back, st, err)
	}
}

// The WAF events come back from their files numbered as they were, and
// numbering carries on after them.
func TestWAFEventsComeBackFromTheirFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	log := slog.New(slog.DiscardHandler)
	files := &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: log,
		Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil }}
	before := waflog.New()
	files.Add(before.Files(), logfile.ReadStats{})
	now := time.Now()
	for i := range 3 {
		at := now.Add(time.Duration(i-3) * time.Minute)
		before.Add(at, wafevent.Event{Time: at, ID: fmt.Sprint(i), Rules: []wafevent.Hit{}})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files.Run(ctx)

	after := waflog.New()
	from := readWAFEvents(cfg, after, &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: log}, log)
	got := after.Recent(0)
	if len(got) != 3 || got[0].ID != "2" || got[0].Seq != 3 {
		t.Fatalf("read back %+v", got)
	}
	if !from.IsZero() {
		t.Errorf("the feed reads the journal back from %v, not after the files' newest event", from)
	}
	after.Add(now, wafevent.Event{Time: now, ID: "new", Rules: []wafevent.Hit{}})
	if after.Newest() != 4 {
		t.Errorf("newest %d", after.Newest())
	}
}

// Files that hold no events, as after the update that first kept them in
// files, leave the journal to fill the log over the days the files keep:
// until then it alone kept the events. With the files off the log starts
// empty.
func TestWAFEventsTheFilesNeverHeldComeFromTheJournal(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	log := slog.New(slog.DiscardHandler)
	files := &logfile.Writer{Dir: t.TempDir(), Source: func() *model.Config { return cfg }, Log: log}
	started := time.Now()
	from := readWAFEvents(cfg, waflog.New(), files, log)
	kept := waflog.New().Files().Kept(cfg)
	if want := started.Add(-kept); from.Before(want.Add(-time.Second)) || from.After(time.Now().Add(-kept)) {
		t.Errorf("reads the journal back from %v, want the start of the files' %v", from, kept)
	}
	cfg.System.Logging.Files.Enabled = false
	if from := readWAFEvents(cfg, waflog.New(), files, log); !from.IsZero() {
		t.Errorf("with the files off the journal is read back from %v", from)
	}
}
