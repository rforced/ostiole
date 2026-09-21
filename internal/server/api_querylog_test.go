package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/dnslog"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// queryLogServer is a signed-in server with a query log already holding a
// few answers, and a configuration that names the client's device.
func queryLogServer(t *testing.T) (*httptest.Server, *dnslog.Log) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	cfg := starter()
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Upstreams = []string{"1.1.1.1"}
	cfg.Services.DNS.QueryLog = model.QueryLog{Enabled: true}
	cfg.Services.DHCP.StaticLeases = []model.StaticLease{
		{MAC: "aa:bb:cc:dd:ee:ff", IP: "10.0.0.50", Hostname: "switch"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(dir).Save(cfg, ""); err != nil {
		t.Fatal(err)
	}
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	qlog := dnslog.New()
	qlog.Slog = slog.New(slog.DiscardHandler)
	qlog.Configure(model.QueryLog{Enabled: true}, dnsblock.Options{}, nil)

	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, QueryLog: qlog}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, qlog
}

func add(l *dnslog.Log, name string, status dnslog.Status, client string, lists ...string) {
	l.Add(dnslog.Entry{
		Time: time.Now(), Name: name, Type: 1, Status: status,
		Client: netip.MustParseAddr(client),
	}, lists)
}

func TestQueryLogUnavailableWithoutRoot(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	for _, path := range []string{"/api/v1/dns/queries", "/api/v1/dns/queries/summary", "/api/v1/dns/queries/stream"} {
		resp, raw := do(t, srv, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", path, resp.StatusCode, raw)
		}
	}
	resp, _ := do(t, srv, http.MethodDelete, "/api/v1/dns/queries", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("clear: %d", resp.StatusCode)
	}
}

func TestQueryLogOffAnswersEmpty(t *testing.T) {
	t.Parallel()
	srv, qlog := queryLogServer(t)
	add(qlog, "ads.example.com", dnslog.StatusBlocked, "10.0.0.50", "one")
	qlog.Configure(model.QueryLog{}, dnsblock.Options{}, nil)

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/dns/queries", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var page queryLogPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if page.Enabled || len(page.Entries) != 0 {
		t.Errorf("page = %+v", page)
	}
}

func TestQueryLogListNamesTheDevice(t *testing.T) {
	t.Parallel()
	srv, qlog := queryLogServer(t)
	add(qlog, "ads.example.com", dnslog.StatusBlocked, "10.0.0.50", "hagezi_pro", "oisd")
	add(qlog, "www.example.com", dnslog.StatusOK, "10.0.0.51")

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/dns/queries", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var page queryLogPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if !page.Enabled || page.Total != 2 || len(page.Entries) != 2 {
		t.Fatalf("page = %+v", page)
	}
	// Newest first, and the type is a name rather than a number.
	if page.Entries[0].Name != "www.example.com" || page.Entries[0].Type != "A" {
		t.Errorf("first row = %+v", page.Entries[0])
	}
	blocked := page.Entries[1]
	if blocked.Device != "switch" {
		t.Errorf("device = %q, want switch", blocked.Device)
	}
	if len(blocked.Lists) != 2 || blocked.Lists[0] != "hagezi_pro" {
		t.Errorf("lists = %v", blocked.Lists)
	}
	if blocked.Status != "blocked" {
		t.Errorf("status = %q", blocked.Status)
	}
}

func TestQueryLogFiltersAndPages(t *testing.T) {
	t.Parallel()
	srv, qlog := queryLogServer(t)
	for range 3 {
		add(qlog, "ads.example.com", dnslog.StatusBlocked, "10.0.0.50", "one")
	}
	add(qlog, "www.example.com", dnslog.StatusOK, "10.0.0.51")

	page := func(t *testing.T, query string) queryLogPage {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/dns/queries"+query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", query, resp.StatusCode, raw)
		}
		var p queryLogPage
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := page(t, "?status=blocked"); p.Total != 3 {
		t.Errorf("status filter: %d", p.Total)
	}
	if p := page(t, "?name=www"); p.Total != 1 {
		t.Errorf("name filter: %d", p.Total)
	}
	if p := page(t, "?list=one"); p.Total != 3 {
		t.Errorf("list filter: %d", p.Total)
	}
	// By device name, resolved to the address the static lease pins.
	if p := page(t, "?client=switch"); p.Total != 3 {
		t.Errorf("device filter: %d", p.Total)
	}
	if p := page(t, "?client=10.0.0.51"); p.Total != 1 {
		t.Errorf("address filter: %d", p.Total)
	}
	// A device nothing answers to matches nothing rather than everything.
	if p := page(t, "?client=nosuchdevice"); p.Total != 0 || len(p.Entries) != 0 {
		t.Errorf("unknown device: %+v", p)
	}
	// The total is what matched; the page is what fits.
	first := page(t, "?limit=2")
	if first.Total != 4 || len(first.Entries) != 2 {
		t.Errorf("first page: %+v", first)
	}
	next := page(t, "?limit=2&before="+strconv.FormatUint(first.Entries[1].Seq, 10))
	if next.Total != 4 || len(next.Entries) != 2 || next.Entries[0].Seq >= first.Entries[1].Seq {
		t.Errorf("second page: %+v", next)
	}
}

func TestQueryLogRefusesNonsense(t *testing.T) {
	t.Parallel()
	srv, _ := queryLogServer(t)
	for _, query := range []string{"?status=maybe", "?type=NOTATYPE", "?since=yesterday", "?before=nine", "?limit=0", "?limit=9000"} {
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/dns/queries"+query, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", query, resp.StatusCode, raw)
		}
	}
}

func TestQueryLogSummaryAndClear(t *testing.T) {
	t.Parallel()
	srv, qlog := queryLogServer(t)
	add(qlog, "ads.example.com", dnslog.StatusBlocked, "10.0.0.50", "one")
	add(qlog, "ads.example.com", dnslog.StatusBlocked, "10.0.0.50", "one")
	add(qlog, "www.example.com", dnslog.StatusOK, "10.0.0.51")

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/dns/queries/summary", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var s querySummary
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.Total != 3 || s.Blocked != 2 || s.Clients != 2 {
		t.Errorf("summary = %+v", s)
	}
	if len(s.TopClients) == 0 || s.TopClients[0].Device != "switch" {
		t.Errorf("top clients = %+v", s.TopClients)
	}

	// The lists page reads the same counts.
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/blocking", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var st blockingState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Counts["one"].Blocked != 2 || st.Counts["one"].Alone != 2 {
		t.Errorf("counts = %+v", st.Counts)
	}
	if st.CountsSince == nil {
		t.Error("no countsSince")
	}

	resp, raw = do(t, srv, http.MethodDelete, "/api/v1/dns/queries", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: %d %s", resp.StatusCode, raw)
	}
	if _, total := qlog.Query(dnslog.Filter{}); total != 0 {
		t.Errorf("held %d after clear", total)
	}
}

// The dashboard's blocking card has a query count only while the log is
// on, because that is the only thing that counts answers.
func TestOverviewCarriesQueryTotals(t *testing.T) {
	t.Parallel()
	srv, qlog := queryLogServer(t)
	add(qlog, "ads.example.com", dnslog.StatusBlocked, "10.0.0.50", "one")
	add(qlog, "www.example.com", dnslog.StatusOK, "10.0.0.50")

	read := func(t *testing.T) BlockingSummary {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/overview", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%d %s", resp.StatusCode, raw)
		}
		var body struct {
			Blocking BlockingSummary `json:"blocking"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		return body.Blocking
	}
	got := read(t)
	if got.Queries == nil || got.Queries.Total != 2 || got.Queries.Blocked != 1 {
		t.Fatalf("queries = %+v", got.Queries)
	}
	qlog.Configure(model.QueryLog{}, dnsblock.Options{}, nil)
	if got := read(t); got.Queries != nil {
		t.Errorf("queries = %+v while the log is off", got.Queries)
	}
}
