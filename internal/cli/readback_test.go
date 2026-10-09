package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ostiole/internal/dnsblock"
	"ostiole/internal/dnslog"
	"ostiole/internal/fwlog"
	"ostiole/internal/gateway"
	"ostiole/internal/journalfeed"
	"ostiole/internal/logfile"
	"ostiole/internal/model"
	"ostiole/internal/wafevent"
	"ostiole/internal/waflog"
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
	readFirewallLog(cfg, ring, files, false, log)

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
	readWAFEvents(cfg, after, &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: log}, false, log)
	got := after.Recent(0)
	if len(got) != 3 || got[0].ID != "2" || got[0].Seq != 3 {
		t.Fatalf("read back %+v", got)
	}
	after.Add(now, wafevent.Event{Time: now, ID: "new", Rules: []wafevent.Hit{}})
	if after.Newest() != 4 {
		t.Errorf("newest %d", after.Newest())
	}
}

// The query log's answers stream back from their files with their lists,
// numbered as they were, and numbering carries on after them.
func TestTheQueryLogComesBackFromItsFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	cfg.Services.DNS.QueryLog = model.QueryLog{Enabled: true, Entries: 2}
	log := slog.New(slog.DiscardHandler)
	files := &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: log,
		Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil }}
	before := dnslog.New()
	before.Slog = log
	before.Configure(model.QueryLog{Enabled: true}, dnsblock.Options{}, nil)
	files.Add(before.Files(), logfile.ReadStats{})
	now := time.Now()
	for i := range 3 {
		before.Add(dnslog.Entry{Time: now.Add(time.Duration(i-3) * time.Minute), Name: fmt.Sprintf("n%d.example.test", i),
			Type: 1, Status: dnslog.StatusBlocked, Client: netip.MustParseAddr("192.0.2.10")}, []string{"made-up-list-1"})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files.Run(ctx)

	after := dnslog.New()
	after.Slog = log
	readQueryLog(cfg, after, &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: log}, false, log)
	total, blocked, oldest := after.Totals()
	counts, _ := after.ListCounts()
	if total != 2 || blocked != 2 || !oldest.Equal(now.Add(-2*time.Minute)) ||
		counts["made-up-list-1"] != (dnslog.ListCount{Blocked: 2, Alone: 2}) {
		t.Fatalf("read back %d/%d from %v, counts %v", total, blocked, oldest, counts)
	}
	after.Add(dnslog.Entry{Time: now, Name: "new.example.test", Type: 1, Client: netip.MustParseAddr("192.0.2.11")}, nil)
	if after.Newest() != 4 {
		t.Errorf("newest %d", after.Newest())
	}
}

// proxyJournal holds a WAF event the proxy logged an hour ago, and says
// when the feed starts following it.
type proxyJournal struct {
	line      string
	following chan struct{}
	once      sync.Once
}

func (j *proxyJournal) Back(_ context.Context, since time.Time, fn func(journalfeed.Record) bool) error {
	if at := time.Now().Add(-time.Hour); at.After(since) {
		fn(journalfeed.Record{Cursor: "c1", Time: at, Message: j.line})
	}
	return nil
}

func (j *proxyJournal) Holds(context.Context, string) (bool, error) { return true, nil }

func (j *proxyJournal) Follow(ctx context.Context, _ string, _ time.Time, _ func(journalfeed.Record)) error {
	j.once.Do(func() { close(j.following) })
	<-ctx.Done()
	return nil
}

// A Clear leaves the WAF events' files empty while the journal still has
// the proxy's lines. The next start reads none of them back.
func TestClearedWAFEventsStayClearedAfterARestart(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	log := slog.New(slog.DiscardHandler)
	files := &logfile.Writer{Dir: t.TempDir(), Source: func() *model.Config { return cfg }, Log: log}
	line, err := wafevent.Line(wafevent.Event{Time: time.Now().Add(-time.Hour), ID: "cleared",
		Verdict: wafevent.VerdictBlocked, Rules: []wafevent.Hit{}})
	if _, ok := wafevent.Parse(string(line)); err != nil || !ok {
		t.Fatalf("the proxy's line does not parse: %s (%v)", line, err)
	}
	journal := &proxyJournal{line: string(line), following: make(chan struct{})}
	events := waflog.New()
	feed := &journalfeed.Feed{
		Journal: journal, Slog: log,
		Taps: []journalfeed.AnyTap{&journalfeed.Tap[wafevent.Event]{
			Log: events, Parse: wafevent.Parse, Name: "the WAF events",
			Settings: func(c *model.Config) (int, time.Duration) {
				return c.Services.Proxy.Events.Size(), c.Services.Proxy.Events.Retention()
			},
			On: func(*model.Config) bool { return true },
		}},
		Source:    func() *model.Config { return cfg },
		Installed: func(context.Context) bool { return true },
	}
	readWAFEvents(cfg, events, files, false, log)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		feed.Run(ctx)
		close(done)
	}()
	select {
	case <-journal.following:
	case <-time.After(5 * time.Second):
		t.Error("the feed never followed the journal")
	}
	cancel()
	<-done
	if n, _ := events.Held(); n != 0 {
		t.Errorf("the start read back %d events the Clear took", n)
	}
}

// The gateways' minutes and events come back from their files after a
// restart, as they were before it, and say the gateway answered.
func TestTheGatewaysHistoryComesBackFromItsFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	log := slog.New(slog.DiscardHandler)
	writer := func() *logfile.Writer {
		return &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: log,
			Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil }}
	}
	files := writer()
	before := gateway.NewHistory()
	readGateways(cfg, before, files, false, log)
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Minute)
	for m := range 3 {
		for i := range 12 {
			at := start.Add(time.Duration(m)*time.Minute + time.Duration(i*5)*time.Second)
			before.Probe("wan", gateway.FamilyIPv4, "", at, time.Duration(3+m)*time.Millisecond, i != 0)
			before.Mark("wan", gateway.StateUp, "", at)
		}
	}
	before.Advance(time.Now())
	before.Note(gateway.Event{Gateway: "wan", Kind: gateway.EventDown, Error: "timeout"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files.Run(ctx)

	after := gateway.NewHistory()
	readGateways(cfg, after, writer(), false, log)
	now := time.Now()
	for _, w := range []string{gateway.Window24h, gateway.Window31d} {
		want, got := before.Read("wan", w, now), after.Read("wan", w, now)
		if len(got.Families) != 1 || len(got.Families[0].Points) != len(want.Families[0].Points) ||
			got.Families[0].Sent != want.Families[0].Sent || got.Families[0].Mean != want.Families[0].Mean {
			t.Errorf("%s read back %+v, want %+v", w, got, want)
		}
	}
	if !after.Answered("wan", gateway.FamilyIPv4, "") {
		t.Error("the files did not say the gateway answered")
	}
	if events := after.Events.Recent(0); len(events) != 1 || events[0].Kind != gateway.EventDown || events[0].Seq != 1 {
		t.Errorf("events read back %+v", events)
	}
}
