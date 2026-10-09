package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ostiole/internal/gateway"
	"ostiole/internal/logring"
)

type fakeStatuses []gateway.Status

func (f fakeStatuses) Statuses() []gateway.Status { return f }

func gatewayHistoryAt(now time.Time) *gateway.History {
	h := gateway.NewHistory()
	start := now.Add(-3 * time.Minute).Truncate(time.Minute)
	for i := range 12 {
		h.Probe("gw_eth0", gateway.FamilyIPv4, "", start.Add(time.Duration(i*5)*time.Second), 4*time.Millisecond, i != 0)
	}
	h.Advance(now)
	h.Note(gateway.Event{Stamp: logring.Stamp{Time: now.Add(-48 * time.Hour)}, Gateway: "gw_eth0", Kind: gateway.EventDown})
	h.Note(gateway.Event{Gateway: "gw_eth0", Kind: gateway.EventUp, For: 30})
	h.Note(gateway.Event{Gateway: "lte", Kind: gateway.EventNever, Error: "timeout"})
	return h
}

func TestAGatewaysHistoryIsReadOverAWindow(t *testing.T) {
	t.Parallel()
	h := gatewayHistoryAt(time.Now())
	srv, _ := filesServer(t, starter(), func(d *Deps) { d.GatewayHistory = h })
	read := func(window string) gatewayHistory {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/gateways/gw_eth0/history?window="+window, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", window, resp.StatusCode, raw)
		}
		var out gatewayHistory
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	day := read("24h")
	if len(day.Families) != 1 || day.Families[0].Sent != 12 || day.Families[0].Points[0].Answered != true {
		t.Errorf("a day = %+v", day)
	}
	if len(day.Events) != 1 || day.Events[0].Kind != gateway.EventUp {
		t.Errorf("a day's events = %+v, want the one in the day", day.Events)
	}
	if month := read("30d"); len(month.Events) != 2 {
		t.Errorf("a month's events = %+v", month.Events)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/gateways/gw_eth0/history?window=1y", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a year: %d", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/gateways/nobody/history", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown gateway: %d", resp.StatusCode)
	}
}

func TestTheGatewaysEventsArePagedAndSearched(t *testing.T) {
	t.Parallel()
	h := gatewayHistoryAt(time.Now())
	srv, _ := filesServer(t, starter(), func(d *Deps) { d.GatewayHistory = h })
	if p := readPage[gateway.Event](t, srv, "/api/v1/gateways/events"); len(p.Entries) != 3 || p.Entries[0].Gateway != "lte" {
		t.Errorf("every event = %+v", p.Entries)
	}
	if p := readPage[gateway.Event](t, srv, "/api/v1/gateways/events?gateway=gw_eth0"); len(p.Entries) != 2 {
		t.Errorf("one gateway's = %+v", p.Entries)
	}
	if p := readPage[gateway.Event](t, srv, "/api/v1/gateways/events?q=up+again"); len(p.Entries) != 1 {
		t.Errorf("searched = %+v", p.Entries)
	}
}

func TestWithoutAHistoryTheGatewaysPagesAreUnavailable(t *testing.T) {
	t.Parallel()
	srv := newTestServerWith(t, func(*Deps) {})
	for _, path := range []string{"/api/v1/gateways/gw_eth0/history", "/api/v1/gateways/events"} {
		if resp, _ := do(t, srv, http.MethodGet, path, nil); resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
}

func TestGatewayMetricsCarryEachFamily(t *testing.T) {
	t.Parallel()
	h := gateway.NewHistory()
	now := time.Now()
	h.Probe("gw_eth0", gateway.FamilyIPv4, "", now, 4*time.Millisecond, true)
	h.Probe("gw_eth0", gateway.FamilyIPv4, "", now, 0, false)
	h.Probe("gw_eth0", gateway.FamilyIPv6, "", now, 6*time.Millisecond, true)
	statuses := fakeStatuses{{Name: "gw_eth0", Interface: "eth0", Online: true, Families: []gateway.FamilyStatus{
		{Family: gateway.FamilyIPv4, LatencyMS: 4, LossPercent: 50}, {Family: gateway.FamilyIPv6, LatencyMS: 6},
	}}}
	srv := withTokens(t, func(d *Deps) { d.Gateways, d.GatewayHistory = statuses, h })
	secret := mintToken(t, srv, "prometheus", "viewer")
	resp, raw := withToken(t, srv, http.MethodGet, "/metrics", secret)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics: %d %s", resp.StatusCode, raw)
	}
	body := string(raw)
	for _, want := range []string{
		`ostiole_gateway_up{gateway="gw_eth0",interface="eth0"} 1`,
		`ostiole_gateway_latency_seconds{gateway="gw_eth0",interface="eth0",family="IPv6"} 0.006`,
		`ostiole_gateway_loss_ratio{gateway="gw_eth0",interface="eth0",family="IPv4"} 0.5`,
		"# TYPE ostiole_gateway_probes_total counter",
		`ostiole_gateway_probes_total{gateway="gw_eth0",interface="eth0",family="IPv4",result="lost"} 1`,
		`ostiole_gateway_probes_total{gateway="gw_eth0",interface="eth0",family="IPv6",result="answered"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics are missing %s", want)
		}
	}
}

// A gateway that never answered says so on its card and warns nobody: its
// next hop may just not answer pings.
func TestOnlyAGatewayThatAnsweredBeforeWarnsWhenDown(t *testing.T) {
	t.Parallel()
	statuses := fakeStatuses{
		{Name: "wan", Interface: "eth0", Monitor: "203.0.113.1", LastError: "timeout"},
		{Name: "lte", Interface: "eth2", Monitor: "198.51.100.1", NeverAnswered: true, LastError: "timeout"},
	}
	srv := newTestServerWith(t, func(d *Deps) { d.Gateways = statuses })
	var titles []string
	for _, w := range getOverview(t, srv).Warnings {
		if w.Kind == "gateway-down" {
			titles = append(titles, w.Title)
		}
	}
	if len(titles) != 1 || titles[0] != "Gateway wan is down" {
		t.Errorf("warnings = %v", titles)
	}
}

// A gateway over its thresholds warns, a family a sentence that says how long
// it was judged over, and those warnings become notices as the others do.
func TestASlowOrLossyGatewayWarns(t *testing.T) {
	t.Parallel()
	statuses := fakeStatuses{
		{Name: "wan", Interface: "eth0", Online: true, Span: 300,
			Slow: []gateway.Over{{Family: "IPv4", LatencyMS: 312.4, Limit: 200, Span: 300}},
			Lossy: []gateway.Over{
				{Family: "IPv4", LossPercent: 25, Limit: 10, Span: 300}, {Family: "IPv6", LossPercent: 100, Limit: 10, Span: 300},
			},
		},
		{Name: "lte", Interface: "eth1", Online: true, Span: 60,
			Slow: []gateway.Over{{Family: "IPv4", LatencyMS: 640, Limit: 500, Span: 60}}},
	}
	srv := newTestServerWith(t, func(d *Deps) { d.Gateways = statuses })
	ov := getOverview(t, srv)
	if w := warning(ov, "gateway-slow"); w == nil || w.Title != "Gateway wan is slow" ||
		w.Detail != "IPv4 took 312 ms on average over the last 5 minutes, above 200 ms." {
		t.Errorf("slow = %+v", w)
	}
	if w := warning(ov, "gateway-lossy"); w == nil || w.Title != "Gateway wan is losing packets" ||
		w.Detail != "IPv4 lost 25% of its probes over the last 5 minutes, above 10%. "+
			"IPv6 lost 100% of its probes over the last 5 minutes, above 10%." {
		t.Errorf("lossy = %+v", w)
	}
	var lte *Warning
	for i, w := range ov.Warnings {
		if w.Title == "Gateway lte is slow" {
			lte = &ov.Warnings[i]
		}
	}
	if lte == nil || lte.Detail != "IPv4 took 640 ms on average over the last minute, above 500 ms." {
		t.Errorf("slow over a minute = %+v", lte)
	}
	if w := warning(ov, "gateway-down"); w != nil {
		t.Errorf("down = %+v", w)
	}
}

// The dashboard's strips judge each gateway against its own thresholds, over
// the minutes its status says it is judged over.
func TestEachWatchedGatewayHasAStrip(t *testing.T) {
	t.Parallel()
	h := gateway.NewHistory()
	now := time.Now()
	start := now.Add(-2 * time.Minute).Truncate(time.Minute)
	for i := range 12 {
		h.Probe("gw_eth0", gateway.FamilyIPv4, "", start.Add(time.Duration(i*5)*time.Second), 300*time.Millisecond, true)
		h.Mark("gw_eth0", gateway.StateUp, "", start.Add(time.Duration(i*5)*time.Second))
	}
	// A minute that lost one of its two probes is lossy alone, and not in a
	// window of five.
	for _, name := range []string{"gw_eth0", "lte"} {
		for i := range 2 {
			at := start.Add(time.Minute + time.Duration(i*30)*time.Second)
			h.Probe(name, gateway.FamilyIPv4, "", at, 4*time.Millisecond, i == 1)
			h.Mark(name, gateway.StateUp, "", at)
		}
	}
	h.Advance(now)
	cfg := starter()
	off := 0
	cfg.Gateways[0].SlowAboveMS = &off
	statuses := fakeStatuses{
		{Name: "gw_eth0", Interface: "eth0", Online: true, Span: 300, Families: []gateway.FamilyStatus{{Family: gateway.FamilyIPv4}}},
		{Name: "lte", Interface: "eth1", Online: true, Span: 60, Families: []gateway.FamilyStatus{{Family: gateway.FamilyIPv4}}},
	}
	srv, _ := filesServer(t, cfg, func(d *Deps) { d.Gateways, d.GatewayHistory = statuses, h })
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/gateways/strips", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("strips: %d %s", resp.StatusCode, raw)
	}
	var strips []gatewayStrip
	if err := json.Unmarshal(raw, &strips); err != nil {
		t.Fatal(err)
	}
	if len(strips) != 2 || strips[0].Name != "gw_eth0" || strips[0].Families != 1 || len(strips[0].Cells) != gateway.StripCells ||
		strips[0].WorstLatencyMS != 300 {
		t.Fatalf("strips = %+v", strips)
	}
	for _, c := range strips[0].Cells {
		if c.Kind == gateway.CellSlow || c.Kind == gateway.CellLossy {
			t.Errorf("gw_eth0, slow off and judged over five minutes, drew %+v", c)
		}
	}
	lossy := 0
	for _, c := range strips[1].Cells {
		if c.Kind == gateway.CellLossy {
			lossy++
		}
	}
	if lossy != 1 {
		t.Errorf("lte, judged a minute at a time, drew %d lossy cells, want 1", lossy)
	}
}
