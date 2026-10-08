package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"ostiole/internal/logring"
	"ostiole/internal/logsearch"
	"ostiole/internal/logsearch/logsearchtest"
	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
)

func kinds(h *History) []string {
	var out []string
	for _, e := range slices.Backward(h.Events.Recent(0)) {
		k := e.Kind
		if e.Family != "" {
			k += " " + e.Family
		}
		out = append(out, k)
	}
	return out
}

func watched(t *testing.T, p *fakeProber, r *fakeRouter, log *bytes.Buffer, gws ...model.Gateway) *Monitor {
	t.Helper()
	m := New(p, r, slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelWarn})))
	m.History = NewHistory()
	m.Configure(&model.Config{Gateways: gws})
	return m
}

func TestAGatewayThatNeverAnsweredIsNotDown(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	p := &fakeProber{fail: map[string]bool{"203.0.113.1": true}}
	r := &fakeRouter{resolveTo: map[string]string{}}
	m := watched(t, p, r, &log,
		model.Gateway{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"},
		model.Gateway{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.1", Priority: 1})
	tick(m, FailAfter)
	s := m.Statuses()[0]
	if s.Online || s.Unknown || !s.NeverAnswered {
		t.Fatalf("status = %+v, want never answered", s)
	}
	if demoted, _ := r.calls(); !slices.Equal(demoted, []string{"wan"}) {
		t.Errorf("demoted = %v, want wan: routing treats it as down", demoted)
	}
	if got := log.String(); !strings.Contains(got, "gateway has not answered") || strings.Contains(got, "gateway is down") {
		t.Errorf("journal:\n%s", got)
	}

	p.setFail("203.0.113.1", false)
	tick(m, RiseAfter)
	if s := m.Statuses()[0]; !s.Online || s.NeverAnswered {
		t.Fatalf("after two answers: %+v", s)
	}
	p.setFail("203.0.113.1", true)
	tick(m, FailAfter)
	if s := m.Statuses()[0]; s.Online || s.NeverAnswered {
		t.Fatalf("after answering once: %+v, want down", s)
	}
	if got := kinds(m.History); !slices.Equal(got, []string{EventNever, EventAnswered, EventDown}) {
		t.Errorf("events = %v", got)
	}
}

// A router that starts during an outage reports down when its files say
// the gateway answered before.
func TestAnAnswerInTheFilesCountsAfterAStart(t *testing.T) {
	t.Parallel()
	h := NewHistory()
	now := time.Now()
	h.RestoreMinute(MinuteLine{
		Stamp: logring.Stamp{Time: now.Add(-2 * time.Hour)}, Gateway: "wan", State: StateUp,
		Families: []FamilyMinute{{Family: FamilyIPv4, Sent: 12, Mean: 4}},
	}, now)
	h.EndRestore(0)
	p := &fakeProber{fail: map[string]bool{"203.0.113.1": true}}
	m := New(p, &fakeRouter{resolveTo: map[string]string{}}, slog.New(slog.DiscardHandler))
	m.History = h
	m.Configure(&model.Config{Gateways: []model.Gateway{{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"}}})
	tick(m, FailAfter)
	if s := m.Statuses()[0]; s.Online || s.NeverAnswered {
		t.Errorf("status = %+v, want down", s)
	}
}

func TestAChangedMonitorStartsAgain(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	p := &fakeProber{fail: map[string]bool{"192.0.2.53": true}}
	m := watched(t, p, &fakeRouter{resolveTo: map[string]string{}}, &log,
		model.Gateway{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"})
	tick(m, RiseAfter)
	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1", Monitor: "192.0.2.53"},
	}})
	tick(m, FailAfter)
	if s := m.Statuses()[0]; !s.NeverAnswered {
		t.Errorf("status = %+v, want never answered at the new monitor", s)
	}
	events := m.History.Events.Recent(0)
	if len(events) != 2 || events[1].Kind != EventMonitor || events[1].Monitor != "192.0.2.53" || events[1].Was != "" ||
		events[0].Kind != EventNever {
		t.Errorf("events = %+v", events)
	}
}

func TestADualStackGatewayIsProbedInBothFamilies(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	p := &fakeProber{fail: map[string]bool{}}
	r := &fakeRouter{resolveTo: map[string]string{"wan": "192.0.2.1"}, resolve6: map[string]string{"wan": "fe80::1"}}
	m := watched(t, p, r, &log, model.Gateway{Name: "wan", Enabled: true, Interface: "eth0"})
	tick(m, RiseAfter)
	s := m.Statuses()[0]
	if !s.Online || len(s.Families) != 2 || s.Families[0].Family != FamilyIPv4 || s.Families[1].Address != "fe80::1" {
		t.Fatalf("status = %+v", s)
	}

	p.setFail("fe80::1", true)
	tick(m, FailAfter)
	s = m.Statuses()[0]
	if !s.Online || s.Families[1].Online || s.Families[1].LossPercent == 0 {
		t.Fatalf("with IPv6 gone: %+v, want the gateway up and IPv6 down", s)
	}
	p.setFail("192.0.2.1", true)
	tick(m, FailAfter)
	if s := m.Statuses()[0]; s.Online {
		t.Fatalf("with both gone: %+v, want down", s)
	}
	p.setFail("192.0.2.1", false)
	p.setFail("fe80::1", false)
	tick(m, RiseAfter)
	if s := m.Statuses()[0]; !s.Online || !s.Families[1].Online {
		t.Fatalf("with both back: %+v", s)
	}
	want := []string{EventFamilyDown + " IPv6", EventDown, EventUp}
	if got := kinds(m.History); !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestAnIPv6OnlyGatewayIsProbed(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	p := &fakeProber{fail: map[string]bool{}}
	r := &fakeRouter{resolveTo: map[string]string{}, resolve6: map[string]string{"wan": "fe80::1"}}
	m := watched(t, p, r, &log, model.Gateway{Name: "wan", Enabled: true, Interface: "eth0"})
	tick(m, RiseAfter)
	if s := m.Statuses()[0]; !s.Online || len(s.Families) != 1 || s.Families[0].Family != FamilyIPv6 {
		t.Errorf("status = %+v", s)
	}
}

func TestAGatewayWithNoNextHopHasNotAnswered(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	m := watched(t, &fakeProber{fail: map[string]bool{}}, &fakeRouter{resolveTo: map[string]string{}}, &log,
		model.Gateway{Name: "wan", Enabled: true, Interface: "eth0"})
	tick(m, FailAfter)
	s := m.Statuses()[0]
	if !s.NeverAnswered || s.LastError != errNoAddress.Error() || len(s.Families) != 0 || s.LossPercent != 100 {
		t.Errorf("status = %+v", s)
	}
}

func at(minute, second int) time.Time {
	return time.Date(2026, 10, 8, 12, minute, second, 0, time.UTC)
}

func TestHistoryKeepsMinutesAndHours(t *testing.T) {
	t.Parallel()
	h := NewHistory()
	for i := range 12 {
		h.Probe("wan", FamilyIPv4, "", at(0, i*5), 3*time.Millisecond, true)
		h.Mark("wan", StateUp, "", at(0, i*5))
	}
	for i := range 12 {
		h.Probe("wan", FamilyIPv4, "", at(2, i*5), 5*time.Millisecond, i%2 == 0)
		h.Mark("wan", StateDown, "", at(2, i*5))
	}
	closed := h.Advance(at(3, 0))
	if len(closed) != 2 || closed[1].State != StateDown || closed[1].Families[0].Lost != 6 {
		t.Fatalf("closed = %+v", closed)
	}

	r := h.Read("wan", Window24h, at(3, 0))
	if len(r.Families) != 1 {
		t.Fatalf("report = %+v", r)
	}
	f := r.Families[0]
	if len(f.Points) != 2 || f.Points[0].T != at(0, 0).Unix() || f.Points[1].T != at(2, 0).Unix() {
		t.Fatalf("points = %+v, want the two minutes with the gap between them left out", f.Points)
	}
	if f.Points[1].Loss != 50 || f.WorstLoss != 50 || f.Worst != 5 || f.Sent != 24 || r.Down != 60 {
		t.Errorf("report = %+v", r)
	}

	month := h.Read("wan", Window31d, at(3, 0))
	if p := month.Families[0].Points; len(p) != 1 || p[0].T != at(0, 0).Truncate(time.Hour).Unix() || p[0].Loss != 25 {
		t.Errorf("month = %+v, want one hour with a quarter lost", month)
	}
	if month.Down != 60 {
		t.Errorf("month down = %d", month.Down)
	}

	five := h.Read("wan", Window5m, at(3, 0))
	if p := five.Families[0].Points; len(p) != 24 {
		t.Errorf("five minutes = %d points, want every probe", len(p))
	}
	if h.Read("nobody", Window24h, at(3, 0)).Families == nil {
		t.Error("an unknown gateway reads nil families")
	}
}

func TestHistoryReadsBackWhatItWrote(t *testing.T) {
	t.Parallel()
	h := NewHistory()
	for m := range 3 {
		for i := range 12 {
			h.Probe("wan", FamilyIPv4, "", at(m, i*5), time.Duration(m+2)*time.Millisecond, i != 3)
			h.Probe("wan", FamilyIPv6, "", at(m, i*5+1), 7*time.Millisecond, true)
			h.Mark("wan", StateUp, "", at(m, i*5))
		}
	}
	h.Advance(at(4, 0))
	lines := h.minutes.Recent(0)
	if len(lines) != 3 {
		t.Fatalf("lines = %+v", lines)
	}
	back := NewHistory()
	for _, l := range slices.Backward(lines) {
		raw, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		m, _, err := ParseMinute(raw)
		if err != nil {
			t.Fatal(err)
		}
		back.RestoreMinute(m, at(4, 0))
	}
	back.EndRestore(lines[0].Seq)
	for _, w := range []string{Window24h, Window31d} {
		want, _ := json.Marshal(h.Read("wan", w, at(4, 0)))
		got, _ := json.Marshal(back.Read("wan", w, at(4, 0)))
		if !bytes.Equal(got, want) {
			t.Errorf("%s read back:\n%s\nwant\n%s", w, got, want)
		}
	}
	if !back.Answered("wan", FamilyIPv6, "") || back.Answered("wan", FamilyIPv6, "192.0.2.53") {
		t.Error("answers read back")
	}
}

func TestHistoryLetsOldMinutesGo(t *testing.T) {
	t.Parallel()
	h := NewHistory()
	start := at(0, 0)
	h.Probe("wan", FamilyIPv4, "", start, time.Millisecond, true)
	h.Probe("wan", FamilyIPv4, "", start.Add(25*time.Hour), time.Millisecond, true)
	h.Advance(start.Add(25*time.Hour + time.Minute))
	end := start.Add(25*time.Hour + time.Minute)
	if p := h.Read("wan", Window24h, end).Families[0].Points; len(p) != 1 {
		t.Errorf("a day = %+v, want the old minute gone", p)
	}
	if p := h.Read("wan", Window31d, end).Families[0].Points; len(p) != 2 {
		t.Errorf("a month = %+v, want both hours", p)
	}
	h.Probe("wan", FamilyIPv4, "", start.Add(60*24*time.Hour), time.Millisecond, true)
	end = start.Add(60*24*time.Hour + time.Minute)
	h.Advance(end)
	if p := h.Read("wan", Window31d, end).Families[0].Points; len(p) != 1 {
		t.Errorf("a month later = %+v, want the old hours gone", p)
	}
}

func TestClearingTheHistoryKeepsTheEvents(t *testing.T) {
	t.Parallel()
	h := NewHistory()
	h.Probe("wan", FamilyIPv4, "", at(0, 0), time.Millisecond, true)
	h.Note(Event{Gateway: "wan", Kind: EventDown})
	h.Advance(at(1, 0))
	h.ClearHistory()
	if f := h.Read("wan", Window24h, at(1, 0)).Families; len(f) != 0 {
		t.Errorf("families = %+v", f)
	}
	if n, _ := h.minutes.Held(); n != 0 {
		t.Errorf("minute lines = %d", n)
	}
	if n, _ := h.Events.Held(); n != 1 {
		t.Errorf("events = %d, want them kept", n)
	}
}

func TestAMinuteWithoutAnswersHasNoLatency(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal([]Point{
		{T: 60, Mean: 3.25, Low: 3, High: 3.5, Answered: true},
		{T: 120, Loss: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != "[[60,3.25,3,3.5,0],[120,null,null,null,100]]" {
		t.Errorf("points = %s", got)
	}
}

func TestEventsAreSearchedAsTheyRead(t *testing.T) {
	t.Parallel()
	h := NewHistory()
	h.Note(Event{Gateway: "wan", Kind: EventNever, Error: "timeout"})
	h.Note(Event{Gateway: "lte", Family: FamilyIPv6, Kind: EventFamilyDown})
	h.Note(Event{Gateway: "wan", Kind: EventMonitor, Monitor: "192.0.2.53"})
	for q, want := range map[string]int{"never answered": 1, "lte": 1, "ipv6 stopped": 1, "192.0.2.53": 1, "wan": 2} {
		page, err := h.Events.Query(context.Background(), 0, 10, EventMatcher(logsearch.Parse(q)))
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != want {
			t.Errorf("%q found %d, want %d", q, len(page.Entries), want)
		}
	}
}

// TestICMPProbeReachesALinkLocalNextHop probes the address an IPv6 router
// advertisement leaves as the next hop, which only means something with
// the interface beside it.
func TestICMPProbeReachesALinkLocalNextHop(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.SetLinkUp(lo.Index); err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddAddr(lo.Index, netip.MustParsePrefix("fe80::1/64")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewICMPProber().Probe(context.Background(), "fe80::1", "lo", 2*time.Second); err != nil {
		t.Errorf("probe: %v", err)
	}
}

// The router searches what the Gateway events card shows, value for value.
func TestEventSearchValuesAreThePages(t *testing.T) {
	t.Parallel()
	for _, c := range logsearchtest.Cases(t, "gateways") {
		var e Event
		if err := json.Unmarshal(c.Entry, &e); err != nil {
			t.Fatal(err)
		}
		var got logsearchtest.Recorder
		e.Search(&got)
		if !slices.Equal(got, c.Values) {
			t.Errorf("%s: %q, want %q", c.Why, got, c.Values)
		}
	}
}
