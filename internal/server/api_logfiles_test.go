package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/dhcplog"
	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/dnslog"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/peerlog"
	"github.com/rforced/ostiole/internal/requestlog"
	"github.com/rforced/ostiole/internal/smart"
	"github.com/rforced/ostiole/internal/traffic"
	"github.com/rforced/ostiole/internal/waflog"
	"github.com/rforced/ostiole/internal/wirelesslog"
)

// filesOn is a configuration that writes the logs to files, the query log
// included.
func filesOn() *model.Config {
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	cfg.Services.DNS.QueryLog.Enabled = true
	return cfg
}

// Clear on a log's page empties its memory and deletes its files, the
// firewall log's as well as the query log's; a viewer may read how the
// files do but clear neither.
func TestClearingALogDeletesItsFiles(t *testing.T) {
	t.Parallel()
	cfg := filesOn()
	ring := fwlog.NewRing(16)
	qlog := dnslog.New()
	qlog.Slog = slog.New(slog.DiscardHandler)
	qlog.Configure(cfg.Services.DNS.QueryLog, dnsblock.Options{}, nil)
	files := &logfile.Writer{
		Dir: t.TempDir(), Source: func() *model.Config { return cfg }, Log: slog.New(slog.DiscardHandler),
		Statfs: func(string) (uint64, uint64, error) { return 50, 100, nil },
	}
	files.Add(ring.Files(), logfile.ReadStats{})
	files.Add(qlog.Files(), logfile.ReadStats{})
	ring.Add(fwlog.Entry{Time: time.Now(), Src: "192.0.2.1"})
	qlog.Add(dnslog.Entry{Time: time.Now(), Name: "a.example", Type: 1, Status: dnslog.StatusOK,
		Client: netip.MustParseAddr("10.0.0.2")}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files.Run(ctx)
	for _, name := range []string{fwlog.FileName, dnslog.FileName} {
		if _, err := os.Stat(filepath.Join(files.Dir, name)); err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
	}

	srv := newTestServerWith(t, func(d *Deps) {
		tokens, err := auth.NewTokens(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d.Tokens, d.Log, d.QueryLog, d.LogFiles = tokens, ring, qlog, files
	})
	viewer := mintToken(t, srv, "look", string(auth.RoleViewer))
	if resp, raw := withToken(t, srv, http.MethodDelete, "/api/v1/log", viewer); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer cleared the log: %d %s", resp.StatusCode, raw)
	}
	resp, raw := withToken(t, srv, http.MethodGet, "/api/v1/system/log-files", viewer)
	var st logfile.Status
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &st) != nil || len(st.Logs) != 2 || st.Logs[0].Files != 1 {
		t.Fatalf("status: %d %s", resp.StatusCode, raw)
	}

	if resp, raw := do(t, srv, http.MethodDelete, "/api/v1/log", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: %d %s", resp.StatusCode, raw)
	}
	if _, err := os.Stat(filepath.Join(files.Dir, fwlog.FileName)); err == nil {
		t.Error("the firewall log's files outlived its Clear")
	}
	if n, _ := ring.Held(); n != 0 {
		t.Errorf("the ring holds %d", n)
	}
	if resp, raw := do(t, srv, http.MethodDelete, "/api/v1/dns/queries", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear queries: %d %s", resp.StatusCode, raw)
	}
	if _, err := os.Stat(filepath.Join(files.Dir, dnslog.FileName)); err == nil {
		t.Error("the query log's files outlived its Clear")
	}
}

// Writing that waits for room, or a log that cannot be written, is a
// dashboard warning, and so a notice.
func TestLogFileTroubleIsAWarning(t *testing.T) {
	t.Parallel()
	cfg := filesOn()
	// A file where the directory should be: nothing can be written there.
	blocked := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	free := uint64(50)
	files := &logfile.Writer{
		Dir: blocked, Source: func() *model.Config { return cfg }, Log: slog.New(slog.DiscardHandler),
		Statfs: func(string) (uint64, uint64, error) { return free, 100, nil },
	}
	ring := fwlog.NewRing(16)
	files.Add(ring.Files(), logfile.ReadStats{})
	ring.Add(fwlog.Entry{Time: time.Now()})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files.Run(ctx)
	a := &api{logFiles: files}
	got := a.logFileWarnings(cfg)
	if len(got) != 1 || got[0].Kind != "log-files" || got[0].Key != fwlog.FileName ||
		!strings.Contains(got[0].Title, "firewall log") {
		t.Fatalf("warnings = %+v", got)
	}
	free = 4
	files.Run(ctx)
	if got := a.logFileWarnings(cfg); len(got) != 2 || got[0].Key != "room" {
		t.Errorf("warnings = %+v", got)
	}
	// Off, the files say nothing.
	if got := a.logFileWarnings(&model.Config{}); len(got) != 0 {
		t.Errorf("off: %+v", got)
	}
}

// A warning names the log it is about in words, for every log kept in
// files.
func TestEveryLogInFilesHasAName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{fwlog.FileName, dnslog.FileName, waflog.FileName, requestlog.FileName, dhcplog.FileName,
		wirelesslog.FileName, peerlog.WireGuard.Name, peerlog.Tailscale.Name, smart.HistoryFileName,
		traffic.LinksFile, traffic.DevicesFile, traffic.DestinationsFile} {
		if logFileNames[name] == "" {
			t.Errorf("no name for %s", name)
		}
	}
}
