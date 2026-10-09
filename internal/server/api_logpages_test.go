package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ostiole/internal/auth"
	"ostiole/internal/dhcplog"
	"ostiole/internal/dnsblock"
	"ostiole/internal/dnslog"
	"ostiole/internal/engine"
	"ostiole/internal/fwlog"
	"ostiole/internal/logfile"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/peerlog"
	"ostiole/internal/requestlog"
	"ostiole/internal/smart"
	"ostiole/internal/store"
	"ostiole/internal/wafevent"
	"ostiole/internal/waflog"
	"ostiole/internal/wirelesslog"
)

// filesServer is a signed-in server running cfg, with a writer keeping the
// logs adjust gives it in files.
func filesServer(t *testing.T, cfg *model.Config, adjust func(*Deps)) (*httptest.Server, *logfile.Writer) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	if _, err := eng.Store().Save(cfg, ""); err != nil {
		t.Fatal(err)
	}
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := &logfile.Writer{
		Dir: filepath.Join(dir, "logs"), Source: eng.Effective, Log: slog.New(slog.DiscardHandler),
		Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil },
	}
	deps := Deps{Engine: eng, Auth: as, LogFiles: files}
	adjust(&deps)
	srv := httptest.NewServer(Handler(deps))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, files
}

// flush writes what every registered log holds to its files, as a daemon
// that stops does.
func flush(t *testing.T, files *logfile.Writer) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files.Run(ctx)
}

// readOf is a read of a log as the pages get it.
type readOf[T any] struct {
	Entries []T    `json:"entries"`
	Next    uint64 `json:"next"`
	More    bool   `json:"more"`
}

func readPage[T any](t *testing.T, srv *httptest.Server, path string) readOf[T] {
	t.Helper()
	resp, raw := do(t, srv, http.MethodGet, path, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", path, resp.StatusCode, raw)
	}
	var p readOf[T]
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func filesOnConfig() *model.Config {
	cfg := starter()
	cfg.System.Logging.Files.Enabled = true
	return cfg
}

// Once memory runs out, a page carries on into the files with the same
// numbers, and so does a search.
func TestTheFirewallLogCarriesOnIntoItsFiles(t *testing.T) {
	t.Parallel()
	ring := fwlog.NewRing(100)
	srv, files := filesServer(t, filesOnConfig(), func(d *Deps) { d.Log = ring })
	files.Add(ring.Files(), logfile.ReadStats{})
	now := time.Now()
	for i := range 10 {
		ring.Add(fwlog.Entry{Time: now.Add(time.Duration(i-10) * time.Minute), Kind: "rule", RuleID: "web",
			Action: "drop", Src: fmt.Sprintf("192.0.2.%d", i)})
	}
	flush(t, files)
	// Memory keeps the newest three; the rest are in the files alone.
	ring.Configure(3, 0)
	srcs := func(p readOf[fwlog.Entry]) string {
		var out []string
		for _, e := range p.Entries {
			out = append(out, e.Src[len("192.0.2."):])
		}
		return fmt.Sprint(out)
	}
	p := readPage[fwlog.Entry](t, srv, "/api/v1/log/entries?limit=5")
	if srcs(p) != "[9 8 7 6 5]" || p.Next != 6 || !p.More {
		t.Fatalf("first page %s, next %d, more %v", srcs(p), p.Next, p.More)
	}
	p = readPage[fwlog.Entry](t, srv, "/api/v1/log/entries?limit=5&before=6")
	if srcs(p) != "[4 3 2 1 0]" || p.More || p.Entries[4].Seq != 1 {
		t.Errorf("second page %s, more %v", srcs(p), p.More)
	}
	p = readPage[fwlog.Entry](t, srv, "/api/v1/log/entries?q=192.0.2.1")
	if srcs(p) != "[1]" || p.More {
		t.Errorf("search %s, more %v", srcs(p), p.More)
	}
}

// While the files are off, a page ends where memory does, whatever is
// left in a directory.
func TestAPageEndsWithMemoryWhileTheFilesAreOff(t *testing.T) {
	t.Parallel()
	ring := fwlog.NewRing(100)
	cfg := filesOnConfig()
	srv, files := filesServer(t, cfg, func(d *Deps) { d.Log = ring })
	files.Add(ring.Files(), logfile.ReadStats{})
	for i := range 5 {
		ring.Add(fwlog.Entry{Time: time.Now(), Kind: "rule", RuleID: "web", Action: "drop", Src: fmt.Sprint(i)})
	}
	flush(t, files)
	ring.Configure(2, 0)
	// A router rolled back to a configuration with the files off, before
	// the writer has deleted them.
	cfg.System.Logging.Files.Enabled = false
	if _, err := store.New(filepath.Dir(files.Dir)).Save(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if p := readPage[fwlog.Entry](t, srv, "/api/v1/log/entries"); len(p.Entries) != 2 || p.More {
		t.Errorf("read %d, more %v", len(p.Entries), p.More)
	}
}

// Answers from the files come back with their lists by name and the
// device's name, and the list filter and a search read them too.
func TestTheQueryLogCarriesOnIntoItsFiles(t *testing.T) {
	t.Parallel()
	cfg := filesOnConfig()
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Upstreams = []string{"1.1.1.1"}
	cfg.Services.DNS.QueryLog = model.QueryLog{Enabled: true}
	cfg.Services.DHCP.StaticLeases = []model.StaticLease{{MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.50", Hostname: "switch"}}
	qlog := dnslog.New()
	qlog.Slog = slog.New(slog.DiscardHandler)
	qlog.Configure(model.QueryLog{Enabled: true}, model.Logging{}.MemoryKeep(), dnsblock.Options{}, nil)
	srv, files := filesServer(t, cfg, func(d *Deps) { d.QueryLog = qlog })
	files.Add(qlog.Files(), logfile.ReadStats{})
	qlog.Add(dnslog.Entry{Time: time.Now(), Name: "ads.example", Type: 1, Status: dnslog.StatusBlocked,
		Reason: dnslog.ReasonList, Client: netip.MustParseAddr("10.0.0.50")}, []string{"hagezi"})
	for _, name := range []string{"a.example", "b.example", "c.example"} {
		add(qlog, name, dnslog.StatusOK, "10.0.0.9")
	}
	flush(t, files)
	qlog.Configure(model.QueryLog{Enabled: true, Entries: 2}, model.Logging{}.MemoryKeep(), dnsblock.Options{}, nil)

	p := readPage[queryRow](t, srv, "/api/v1/dns/queries?limit=10")
	if len(p.Entries) != 4 || p.More {
		t.Fatalf("read %+v, more %v", p.Entries, p.More)
	}
	ads := p.Entries[3]
	if ads.Name != "ads.example" || ads.Seq != 1 || ads.Device != "switch" || fmt.Sprint(ads.Lists) != "[hagezi]" {
		t.Errorf("from the files: %+v", ads)
	}
	for _, path := range []string{"/api/v1/dns/queries?q=hagezi", "/api/v1/dns/queries?list=hagezi", "/api/v1/dns/queries?q=switch"} {
		if p := readPage[queryRow](t, srv, path); len(p.Entries) != 1 || p.Entries[0].Name != "ads.example" {
			t.Errorf("%s: %+v", path, p.Entries)
		}
	}
}

func TestWAFEventsCarryOnIntoTheirFiles(t *testing.T) {
	t.Parallel()
	events := waflog.New()
	srv, files := filesServer(t, filesOnConfig(), func(d *Deps) { d.WAFLog = events })
	files.Add(events.Files(), logfile.ReadStats{})
	now := time.Now()
	for i := range 5 {
		at := now.Add(time.Duration(i-5) * time.Minute)
		events.Add(at, wafevent.Event{Time: at, ID: fmt.Sprint(i), Site: "shop", Verdict: wafevent.VerdictBlocked, Rules: []wafevent.Hit{}})
	}
	flush(t, files)
	events.Configure(2, 0)
	p := readPage[waflog.Entry](t, srv, "/api/v1/proxy/events")
	var ids []string
	for _, e := range p.Entries {
		ids = append(ids, e.ID)
	}
	if fmt.Sprint(ids) != "[4 3 2 1 0]" || p.More {
		t.Errorf("read %v, more %v", ids, p.More)
	}
}

// The proxy's requests read on into their files, and the page says whether
// the level the router runs at keeps them.
func TestProxyRequestsCarryOnIntoTheirFiles(t *testing.T) {
	t.Parallel()
	requests := requestlog.New()
	cfg := filesOnConfig()
	cfg.System.Logging.Level = model.LogInfo
	srv, files := filesServer(t, cfg, func(d *Deps) { d.Requests = requests })
	files.Add(requestlog.Files(requests), logfile.ReadStats{})
	now := time.Now()
	for i := range 5 {
		requests.Add(requestlog.Request{Time: now.Add(time.Duration(i-5) * time.Minute),
			Site: "vault", Client: "203.0.113.9", Method: "GET", Host: "vault.example.com", Path: fmt.Sprintf("/p%d", i), Status: 200})
	}
	flush(t, files)
	requests.Configure(2, 0)
	paths := func(p readOf[requestlog.Request]) string {
		var out []string
		for _, r := range p.Entries {
			out = append(out, r.Path)
		}
		return fmt.Sprint(out)
	}
	if p := readPage[requestlog.Request](t, srv, "/api/v1/proxy/requests"); paths(p) != "[/p4 /p3 /p2 /p1 /p0]" || p.More {
		t.Errorf("read %s, more %v", paths(p), p.More)
	}
	if p := readPage[requestlog.Request](t, srv, "/api/v1/proxy/requests?q=p1"); paths(p) != "[/p1]" {
		t.Errorf("searched %s", paths(p))
	}
	_, raw := do(t, srv, http.MethodGet, "/api/v1/proxy/requests", nil)
	if !strings.Contains(string(raw), `"kept":true`) {
		t.Errorf("page %s", raw)
	}
}

// The DHCP log reads on into its files, and names a client by its hardware
// address, a DHCPv6 one's included.
func TestTheDHCPLogCarriesOnIntoItsFilesAndNamesClients(t *testing.T) {
	t.Parallel()
	dhcp := dhcplog.New()
	cfg := filesOnConfig()
	cfg.System.Logging.Level = model.LogInfo
	cfg.Services.DHCP.StaticLeases = []model.StaticLease{{MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.50", Hostname: "switch"}}
	srv, files := filesServer(t, cfg, func(d *Deps) { d.DHCPLog = dhcp })
	files.Add(dhcplog.Files(dhcp), logfile.ReadStats{})
	for _, line := range []string{
		"DHCPDISCOVER(eth1) aa:bb:cc:dd:ee:ff ",
		"DHCPOFFER(eth1) 10.0.0.50 aa:bb:cc:dd:ee:ff ",
		"DHCPACK(eth1) 10.0.0.50 aa:bb:cc:dd:ee:ff switch",
		"DHCPREPLY(eth1) fd00::50 00:03:00:01:aa:bb:cc:dd:ee:ff ",
	} {
		e, ok := dhcplog.Parse(line)
		if !ok {
			t.Fatalf("%s did not parse", line)
		}
		dhcp.Add(e)
	}
	flush(t, files)
	dhcp.Configure(1, 0)
	p := readPage[dhcpRow](t, srv, "/api/v1/dhcp/log")
	var got []string
	for _, r := range p.Entries {
		got = append(got, r.Message+" "+r.Device)
	}
	if fmt.Sprint(got) != "[REPLY switch ACK switch OFFER switch DISCOVER switch]" || p.More {
		t.Errorf("read %v, more %v", got, p.More)
	}
	if p := readPage[dhcpRow](t, srv, "/api/v1/dhcp/log?q=offer"); len(p.Entries) != 1 || p.Entries[0].Address != "10.0.0.50" {
		t.Errorf("searched %+v", p.Entries)
	}
	_, raw := do(t, srv, http.MethodGet, "/api/v1/dhcp/log", nil)
	if !strings.Contains(string(raw), `"kept":true`) {
		t.Errorf("page %s", raw)
	}
}

// The wireless log names each event's network and client, and reads on
// into its files.
func TestTheWirelessLogNamesNetworksAndClients(t *testing.T) {
	t.Parallel()
	clients := wirelesslog.New()
	cfg := wirelessAPIConfig()
	cfg.System.Logging.Files.Enabled = true
	cfg.System.Logging.Level = model.LogInfo
	cfg.Services.DHCP.StaticLeases = []model.StaticLease{{MAC: "12:34:56:78:9a:bc", IP: "192.168.1.60", Hostname: "phone"}}
	srv, files := filesServer(t, cfg, func(d *Deps) { d.WirelessLog = clients })
	files.Add(wirelesslog.Files(clients), logfile.ReadStats{})
	for _, line := range []string{"ap0: AP-STA-CONNECTED 12:34:56:78:9a:bc", "ap0: AP-STA-DISCONNECTED 12:34:56:78:9a:bc"} {
		e, _ := wirelesslog.Parse(line)
		clients.Add(e)
	}
	flush(t, files)
	clients.Configure(1, 0)
	p := readPage[wirelessRow](t, srv, "/api/v1/wireless/log")
	var got []string
	for _, r := range p.Entries {
		got = append(got, r.Event.Event+" "+r.Network+" "+r.Device)
	}
	if fmt.Sprint(got) != "[disconnected ostiole-lan phone connected ostiole-lan phone]" {
		t.Errorf("read %v", got)
	}
	if p := readPage[wirelessRow](t, srv, "/api/v1/wireless/log?q=ostiole-lan+disconnected"); len(p.Entries) != 1 {
		t.Errorf("searched %+v", p.Entries)
	}
}

// Each VPN's peer log is its own page, read on into its files.
func TestThePeerLogsAreServed(t *testing.T) {
	t.Parallel()
	wg, ts := peerlog.New(), peerlog.New()
	cfg := filesOnConfig()
	cfg.System.Logging.Level = model.LogInfo
	srv, files := filesServer(t, cfg, func(d *Deps) { d.WireGuardLog, d.TailscaleLog = wg, ts })
	files.Add(peerlog.WireGuard.Files(wg), logfile.ReadStats{})
	wg.Add(peerlog.Event{Event: peerlog.Connected, Tunnel: "wg0", Peer: "phone", Endpoint: "203.0.113.5:51820"})
	wg.Add(peerlog.Event{Event: peerlog.Quiet, Tunnel: "wg0", Peer: "phone"})
	flush(t, files)
	wg.Configure(1, 0)
	ts.Add(peerlog.Event{Event: peerlog.Online, Peer: "laptop"})
	if p := readPage[peerlog.Event](t, srv, "/api/v1/wireguard/log"); len(p.Entries) != 2 || p.Entries[1].Event != peerlog.Connected {
		t.Errorf("WireGuard: %+v", p.Entries)
	}
	if p := readPage[peerlog.Event](t, srv, "/api/v1/tailscale/log?q=laptop"); len(p.Entries) != 1 {
		t.Errorf("Tailscale: %+v", p.Entries)
	}
}

// The drives' history stops at what memory holds, never reading its files.
func TestTheDriveHistoryStopsAtWhatItKeeps(t *testing.T) {
	t.Parallel()
	history := smart.NewHistory()
	srv, files := filesServer(t, filesOnConfig(), func(d *Deps) { d.DriveHistory = history })
	files.Add(smart.HistoryFiles(history), logfile.ReadStats{})
	now := time.Now()
	for i := range 3 {
		temp := 40 + i
		history.Add(smart.Reading{Time: now.Add(time.Duration(i-3) * time.Hour),
			Drive: "sda", Health: "passed", Temperature: &temp})
	}
	flush(t, files)
	history.Configure(2, 0)
	p := readPage[smart.Reading](t, srv, "/api/v1/diagnostics/drives/history")
	if len(p.Entries) != 2 || p.More || *p.Entries[0].Temperature != 42 || *p.Entries[1].Temperature != 41 {
		t.Errorf("read %+v", p)
	}
	if p := readPage[smart.Reading](t, srv, "/api/v1/diagnostics/drives/history?q=41"); len(p.Entries) != 1 {
		t.Errorf("searched %+v", p.Entries)
	}
	if p := readPage[smart.Reading](t, srv, "/api/v1/diagnostics/drives/history?q=40"); len(p.Entries) != 0 || p.More {
		t.Errorf("searched the files: %+v", p)
	}
}
