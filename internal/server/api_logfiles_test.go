package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ostiole/internal/auth"
	"ostiole/internal/dhcplog"
	"ostiole/internal/discoverylog"
	"ostiole/internal/dnsblock"
	"ostiole/internal/dnslog"
	"ostiole/internal/fwlog"
	"ostiole/internal/gateway"
	"ostiole/internal/logfile"
	"ostiole/internal/logging"
	"ostiole/internal/logring"
	"ostiole/internal/model"
	"ostiole/internal/peerlog"
	"ostiole/internal/requestlog"
	"ostiole/internal/smart"
	"ostiole/internal/traffic"
	"ostiole/internal/wafevent"
	"ostiole/internal/waflog"
	"ostiole/internal/wirelesslog"
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
// firewall log's as well as the query log's. A viewer may read how the
// files do, and only an admin may clear.
func TestClearingALogDeletesItsFiles(t *testing.T) {
	t.Parallel()
	cfg := filesOn()
	ring := fwlog.NewRing(16)
	qlog := dnslog.New()
	qlog.Slog = slog.New(slog.DiscardHandler)
	qlog.Configure(cfg.Services.DNS.QueryLog, cfg.System.Logging.MemoryKeep(), dnsblock.Options{}, nil)
	files := &logfile.Writer{
		Dir: t.TempDir(), Source: func() *model.Config { return cfg }, Log: slog.New(slog.DiscardHandler),
		Statfs: func(string) (uint64, uint64, error) { return 50, 100, nil },
	}
	files.Add(fwlog.Files(ring), logfile.ReadStats{})
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
	operator := mintToken(t, srv, "hand", string(auth.RoleOperator))
	for role, token := range map[string]string{"viewer": viewer, "operator": operator} {
		if resp, raw := withToken(t, srv, http.MethodDelete, "/api/v1/log", token); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s cleared the log: %d %s", role, resp.StatusCode, raw)
		}
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
	files.Add(fwlog.Files(ring), logfile.ReadStats{})
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
		wirelesslog.FileName, discoverylog.FileName, peerlog.WireGuard.Name, peerlog.Tailscale.Name, smart.HistoryFileName,
		traffic.LinksFile, traffic.DevicesFile, traffic.DestinationsFile} {
		if logFileNames[name] == "" {
			t.Errorf("no name for %s", name)
		}
	}
}

// clearPaths are where each log a page clears on its own is cleared.
var clearPaths = map[string]string{
	fwlog.FileName:           "/api/v1/log",
	dnslog.FileName:          "/api/v1/dns/queries",
	waflog.FileName:          "/api/v1/proxy/events",
	requestlog.FileName:      "/api/v1/proxy/requests",
	dhcplog.FileName:         "/api/v1/dhcp/log",
	wirelesslog.FileName:     "/api/v1/wireless/log",
	discoverylog.FileName:    "/api/v1/discovery/log",
	peerlog.WireGuard.Name:   "/api/v1/wireguard/log",
	peerlog.Tailscale.Name:   "/api/v1/tailscale/log",
	smart.HistoryFileName:    "/api/v1/diagnostics/drives/history",
	traffic.DestinationsFile: "/api/v1/traffic/destinations",
	traffic.LinksFile:        "/api/v1/traffic/interfaces",
	gateway.HistoryFileName:  "/api/v1/gateways/history",
	gateway.EventsFileName:   "/api/v1/gateways/events",
}

// keepEveryLog puts an entry in every log a Clear takes, and gives each a
// directory of files under dir. What each log holds is counted by its
// directory's name.
func keepEveryLog(t *testing.T, d *Deps, dir string) map[string]func() int {
	t.Helper()
	now := time.Now()
	stamp := logring.Stamp{Time: now}
	d.Log = fwlog.NewRing(16)
	d.Log.Add(fwlog.Entry{Time: now, Src: "192.0.2.1"})
	d.QueryLog = dnslog.New()
	d.QueryLog.Slog = slog.New(slog.DiscardHandler)
	d.QueryLog.Configure(filesOn().Services.DNS.QueryLog, model.Logging{}.MemoryKeep(), dnsblock.Options{}, nil)
	d.QueryLog.Add(dnslog.Entry{Time: now, Name: "a.example", Type: 1, Status: dnslog.StatusOK,
		Client: netip.MustParseAddr("10.0.0.2")}, nil)
	d.WAFLog = waflog.New()
	d.WAFLog.Add(now, wafevent.Event{Time: now, ID: "one", Verdict: wafevent.VerdictBlocked, Rules: []wafevent.Hit{}})
	d.Requests = requestlog.New()
	d.Requests.Add(requestlog.Request{Stamp: stamp, Method: "GET", Host: "app.example", Path: "/"})
	d.DHCPLog = dhcplog.New()
	d.DHCPLog.Add(dhcplog.Event{Stamp: stamp, Message: "ACK", Interface: "eth1", Address: "10.0.0.5"})
	d.WirelessLog = wirelesslog.New()
	d.WirelessLog.Add(wirelesslog.Event{Stamp: stamp, Event: "joined", Interface: "wlan0"})
	d.DiscoveryLog = discoverylog.New()
	d.DiscoveryLog.Add(discoverylog.Event{Stamp: stamp, Protocol: "mdns", From: "eth1", Source: "192.0.2.7"})
	d.WireGuardLog, d.TailscaleLog = peerlog.New(), peerlog.New()
	d.WireGuardLog.Add(peerlog.Event{Stamp: stamp, Event: "connected", Tunnel: "wg0", Peer: "phone"})
	d.TailscaleLog.Add(peerlog.Event{Stamp: stamp, Event: "online", Peer: "laptop"})
	d.DriveHistory = smart.NewHistory()
	d.DriveHistory.Add(smart.Reading{Stamp: stamp, Drive: "sda", Health: "passed"})
	c, step := countingRouter(t, func(cfg *model.Config) {
		cfg.Traffic.Destinations = model.TrafficDestinations{Enabled: true}
	})
	step(0, 1000)
	step(5*time.Second, 51000)
	d.Traffic = c
	d.GatewayHistory = gateway.NewHistory()
	d.GatewayHistory.Probe("wan", gateway.FamilyIPv4, "", now.Add(-2*time.Minute), time.Millisecond, true)
	d.GatewayHistory.Advance(now)
	d.GatewayHistory.Note(gateway.Event{Stamp: stamp, Gateway: "wan", Kind: gateway.EventDown})
	d.LogFiles = &logfile.Writer{Dir: dir, Source: filesOn, Log: slog.New(slog.DiscardHandler)}
	held := func(of func() (int, time.Time)) func() int {
		return func() int {
			n, _ := of()
			return n
		}
	}
	logs := map[string]func() int{
		fwlog.FileName:         held(d.Log.Held),
		dnslog.FileName:        held(d.QueryLog.Held),
		waflog.FileName:        held(d.WAFLog.Held),
		requestlog.FileName:    held(d.Requests.Held),
		dhcplog.FileName:       held(d.DHCPLog.Held),
		wirelesslog.FileName:   held(d.WirelessLog.Held),
		discoverylog.FileName:  held(d.DiscoveryLog.Held),
		peerlog.WireGuard.Name: held(d.WireGuardLog.Held),
		peerlog.Tailscale.Name: held(d.TailscaleLog.Held),
		smart.HistoryFileName:  held(d.DriveHistory.Held),
		traffic.DevicesFile:    func() int { return len(c.DeviceReports(traffic.Window24h)) },
		traffic.DestinationsFile: func() int {
			_, n, _ := c.Destinations(time.Hour, "")
			return n
		},
		traffic.LinksFile: func() int {
			n := 0
			for _, l := range c.LinkReports(traffic.Window24h) {
				n += int(l.Totals.Down + l.Totals.Up)
			}
			return n
		},
		gateway.HistoryFileName: func() int {
			return len(d.GatewayHistory.Read("wan", gateway.Window24h, time.Now()).Families)
		},
		gateway.EventsFileName: held(d.GatewayHistory.Events.Held),
	}
	for name := range logs {
		if logs[name]() == 0 {
			t.Fatalf("the %s holds nothing to clear", name)
		}
	}
	for name := range logs {
		day := filepath.Join(dir, name, "2026-09-29.jsonl.gz")
		if err := os.MkdirAll(filepath.Dir(day), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(day, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return logs
}

// adminOnly mints a viewer's and an operator's token, and gives back a
// check that neither may make a request.
func adminOnly(t *testing.T, srv *httptest.Server) func(method, path string) {
	t.Helper()
	tokens := map[auth.Role]string{}
	for _, role := range []auth.Role{auth.RoleViewer, auth.RoleOperator} {
		tokens[role] = mintToken(t, srv, string(role), string(role))
	}
	return func(method, path string) {
		t.Helper()
		for role, token := range tokens {
			if resp, raw := withToken(t, srv, method, path, token); resp.StatusCode != http.StatusForbidden {
				t.Errorf("a %s's %s %s: %d %s", role, method, path, resp.StatusCode, raw)
			}
		}
	}
}

// withTokens is a test server that takes tokens, with d adjusted.
func withTokens(t *testing.T, adjust func(*Deps)) *httptest.Server {
	t.Helper()
	return newTestServerWith(t, func(d *Deps) {
		tokens, err := auth.NewTokens(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d.Tokens = tokens
		adjust(d)
	})
}

// Each page's Clear takes its own log, memory and files, and nothing of
// any other log's.
func TestEveryPageClearsItsOwnLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var logs map[string]func() int
	srv := withTokens(t, func(d *Deps) { logs = keepEveryLog(t, d, dir) })
	refused := adminOnly(t, srv)
	names := slices.Sorted(maps.Keys(clearPaths))
	for i, name := range names {
		path := clearPaths[name]
		refused(http.MethodDelete, path)
		if resp, raw := do(t, srv, http.MethodDelete, path, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("clear %s: %d %s", path, resp.StatusCode, raw)
		}
		if n := logs[name](); n != 0 {
			t.Errorf("the %s holds %d after its Clear", name, n)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("the %s's files outlived its Clear", name)
		}
		for _, other := range append(names[i+1:], traffic.DevicesFile) {
			if logs[other]() == 0 {
				t.Errorf("clearing the %s took the %s", name, other)
			}
			if _, err := os.Stat(filepath.Join(dir, other)); err != nil {
				t.Errorf("clearing the %s took the %s's files", name, other)
			}
		}
	}
}

// A log this daemon does not keep has nothing to clear.
func TestClearingALogNotKeptIsUnavailable(t *testing.T) {
	t.Parallel()
	srv := newTestServerWith(t, func(*Deps) {})
	resp, raw := do(t, srv, http.MethodDelete, "/api/v1/dhcp/log", nil)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "the DHCP log is not kept") {
		t.Errorf("clear: %d %s", resp.StatusCode, raw)
	}
}

// Clear every log takes every log a page can clear. Only an admin may.
func TestClearingEveryLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var logs map[string]func() int
	srv := withTokens(t, func(d *Deps) { logs = keepEveryLog(t, d, dir) })
	adminOnly(t, srv)(http.MethodDelete, "/api/v1/system/logs")
	if resp, raw := do(t, srv, http.MethodDelete, "/api/v1/system/logs", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: %d %s", resp.StatusCode, raw)
	}
	for name, held := range logs {
		if n := held(); n != 0 {
			t.Errorf("the %s holds %d", name, n)
		}
	}
	left, err := os.ReadDir(dir)
	if err != nil || len(left) != 0 {
		t.Errorf("left %v (%v), want nothing", left, err)
	}
}

// A log whose files cannot be deleted is named, and the rest are cleared
// all the same.
func TestClearingEveryLogCarriesOnPastFilesThatStay(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root deletes files whatever their directory's mode")
	}
	dir := t.TempDir()
	var logs map[string]func() int
	srv := newTestServerWith(t, func(d *Deps) { logs = keepEveryLog(t, d, dir) })
	stuck := filepath.Join(dir, dhcplog.FileName, "stuck")
	if err := os.Mkdir(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "day"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	resp, raw := do(t, srv, http.MethodDelete, "/api/v1/system/logs", nil)
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(raw), "the files of the DHCP log could not be deleted") {
		t.Errorf("clear: %d %s", resp.StatusCode, raw)
	}
	for name, held := range logs {
		if n := held(); n != 0 {
			t.Errorf("the %s holds %d", name, n)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != (name == dhcplog.FileName) {
			t.Errorf("the %s's files left = %v", name, err == nil)
		}
	}
}

// A Clear cannot be undone, so it is written whatever the level, with who
// asked for it. Not parallel: it swaps the default logger.
func TestClearsAreLoggedAtEveryLevel(t *testing.T) {
	var buf safeBuffer
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelError)
	was := slog.Default()
	slog.SetDefault(slog.New(logging.Handler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: lvl}))))
	t.Cleanup(func() { slog.SetDefault(was) })
	srv := newTestServerWith(t, func(d *Deps) { d.Log = fwlog.NewRing(16) })
	for _, path := range []string{"/api/v1/log", "/api/v1/system/logs"} {
		if resp, raw := do(t, srv, http.MethodDelete, path, nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("clear %s: %d %s", path, resp.StatusCode, raw)
		}
	}
	got := buf.String()
	if !strings.Contains(got, `msg="cleared a log" log=firewall user=admin address=127.0.0.1`) ||
		!strings.Contains(got, `msg="cleared every log" user=admin`) {
		t.Errorf("at the error level the log holds:\n%s", got)
	}
}
