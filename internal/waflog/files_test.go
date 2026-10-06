package waflog

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/model"
)

// Written by the writer and read back, an event is what it was, its number
// included. It is filed under the day it was logged, not the day its request
// opened, and the files say it is the oldest from then.
func TestEventsComeBackFromTheFiles(t *testing.T) {
	t.Parallel()
	l := New()
	// Yesterday, as the log ages events out by the real clock.
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	logged := day.Add(30 * time.Minute)
	opened := logged.Add(-time.Hour)
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	w := &logfile.Writer{Dir: t.TempDir(), Source: func() *model.Config { return cfg }, Log: slog.New(slog.DiscardHandler),
		Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil }}
	// Registered before anything arrives, as serve does.
	w.Add(l.Files(), logfile.ReadStats{})
	l.Add(logged, event("socket", opened))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w.Run(ctx)
	if _, err := os.Stat(filepath.Join(w.Dir, FileName, day.Format(time.DateOnly)+".jsonl.gz")); err != nil {
		t.Fatalf("not filed under the day it was logged: %v", err)
	}
	if st := w.Status(); len(st.Logs) != 1 || st.Logs[0].Oldest == nil || !st.Logs[0].Oldest.Equal(logged) {
		t.Errorf("status %+v", st.Logs)
	}
	got, st, err := logfile.Read(w.Dir, FileName, FileVersion, 10, time.Time{}, ParseLine)
	if err != nil || st.Skipped != 0 || len(got) != 1 {
		t.Fatalf("read %+v, %+v, %v", got, st, err)
	}
	if g := got[0]; g.Seq != 1 || !g.Logged.Equal(logged) || !g.Time.Equal(opened) || g.ID != "socket" {
		t.Errorf("read back %+v", g)
	}
}

// The files keep the shorter of their days and the events' own.
func TestTheFilesKeepTheEventsDays(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	cfg.System.Logging.Files = model.LogFiles{Enabled: true, RetentionDays: 30}
	cfg.Services.Proxy.Events.Days = 3
	if got := New().Files().Kept(cfg); got != 72*time.Hour {
		t.Errorf("kept %v", got)
	}
}

// An event a file kept from before events were redacted comes back
// without its token.
func TestAFilesTokenIsRedactedOnTheWayBack(t *testing.T) {
	t.Parallel()
	const token = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
	line := `{"seq":7,"logged":"2026-09-28T16:00:00Z","time":"2026-09-28T16:00:00Z","id":"a","verdict":"matched",` +
		`"uri":"/notifications/hub?access_token=` + token + `","rules":[{"id":942432,` +
		`"data":"Matched Data: -UCNif found within ARGS:access_token: ` + token + `"}]}`
	e, _, err := ParseLine([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if e.Seq != 7 || e.URI != "/notifications/hub?access_token=REDACTED" ||
		e.Rules[0].Data != "Matched Data: REDACTED found within ARGS:access_token: REDACTED" {
		t.Errorf("read back %+v", e)
	}
}
