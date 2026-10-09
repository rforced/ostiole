package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

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
		Time: now.Add(-2 * time.Hour), Gateway: "wan", State: StateUp,
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

	month := h.Read("wan", Window30d, at(3, 0))
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
	for _, w := range []string{Window24h, Window30d} {
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
	if p := h.Read("wan", Window30d, end).Families[0].Points; len(p) != 2 {
		t.Errorf("a month = %+v, want both hours", p)
	}
	h.Probe("wan", FamilyIPv4, "", start.Add(60*24*time.Hour), time.Millisecond, true)
	end = start.Add(60*24*time.Hour + time.Minute)
	h.Advance(end)
	if p := h.Read("wan", Window30d, end).Families[0].Points; len(p) != 1 {
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

func judging(t *testing.T, gws ...model.Gateway) (*Monitor, *fakeProber, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	p := &fakeProber{fail: map[string]bool{}}
	r := &fakeRouter{resolveTo: map[string]string{}, resolve6: map[string]string{}}
	for _, g := range gws {
		if g.Address == "" {
			r.resolveTo[g.Name], r.resolve6[g.Name] = "192.0.2.1", "fe80::1"
		}
	}
	m := watched(t, p, r, &log, gws...)
	// An hour before the minutes a test plays, so the ticks' own minute is
	// never in their window.
	m.Now = func() time.Time { return at(0, 0).Add(-time.Hour) }
	return m, p, &log
}

// play puts one of wan's minutes in the history as the monitor's probes and
// marks would, each family's probes spread over it with the lost ones
// first, and judges it once it is over.
func play(m *Monitor, minute int, state string, families ...FamilyMinute) {
	m.mu.Lock()
	monitor := m.states["wan"].gw.Monitor
	m.mu.Unlock()
	for _, f := range families {
		for i := range f.Sent {
			m.History.Probe("wan", f.Family, monitor, at(minute, i*60/f.Sent),
				time.Duration(f.Mean*float64(time.Millisecond)), i >= f.Lost)
		}
	}
	m.History.Mark("wan", state, monitor, at(minute, 0))
	m.judge(m.History.Advance(at(minute+1, 0)))
}

// v4 is a minute of twelve IPv4 probes, one every 5 s.
func v4(lost int, mean float64) FamilyMinute {
	return FamilyMinute{Family: FamilyIPv4, Sent: 12, Lost: lost, Mean: mean}
}

// pair is a minute of two IPv4 probes, one every 30 s.
func pair(lost int, mean float64) FamilyMinute {
	return FamilyMinute{Family: FamilyIPv4, Sent: 2, Lost: lost, Mean: mean}
}

// wan is probed every 5 s, so a minute's twelve probes are judged alone;
// wan30 every 30 s, the default, so over five minutes.
var (
	wan   = model.Gateway{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1", ProbeEverySeconds: 5}
	wan30 = model.Gateway{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"}
)

// logged reports whether the journal has a line saying msg with attr.
func logged(journal, msg, attr string) bool {
	for line := range strings.Lines(journal) {
		if strings.Contains(line, `msg="`+msg+`"`) && strings.Contains(line, " "+attr+" ") {
			return true
		}
	}
	return false
}

func TestASlowMinuteWarnsUntilAMinuteIsNot(t *testing.T) {
	t.Parallel()
	m, _, log := judging(t, wan)
	tick(m, RiseAfter)
	play(m, 0, StateUp, v4(0, 250))
	s := m.Statuses()[0]
	if len(s.Slow) != 1 || s.Slow[0].LatencyMS != 250 || s.Slow[0].Limit != model.DefaultSlowAboveMS ||
		s.Slow[0].Span != 60 || !s.Slow[0].Since.Equal(at(0, 0)) || len(s.Lossy) != 0 {
		t.Fatalf("after a slow minute: %+v", s)
	}
	play(m, 1, StateUp, v4(0, 260))
	if s := m.Statuses()[0]; len(s.Slow) != 1 || s.Slow[0].LatencyMS != 260 || !s.Slow[0].Since.Equal(at(0, 0)) {
		t.Fatalf("after a second: %+v", s.Slow)
	}
	play(m, 2, StateUp, v4(0, 150))
	if s := m.Statuses()[0]; len(s.Slow) != 0 {
		t.Fatalf("after a quick minute: %+v", s.Slow)
	}
	events := m.History.Events.Recent(0)
	if len(events) != 2 || events[1].Kind != EventSlow || events[1].LatencyMS != 250 || events[1].Limit != 200 ||
		events[1].Span != 60 || events[0].Kind != EventSlowEnd || events[0].For != 180 {
		t.Errorf("events = %+v", events)
	}
	if got := log.String(); !logged(got, "gateway is slow", "over=1m0s") ||
		!logged(got, "gateway is no longer slow", "over=1m0s") {
		t.Errorf("journal:\n%s", got)
	}
}

func TestOneLostProbeInAMinuteIsNotLossy(t *testing.T) {
	t.Parallel()
	m, _, _ := judging(t, wan)
	tick(m, RiseAfter)
	play(m, 0, StateUp, v4(1, 3))
	if s := m.Statuses()[0]; len(s.Lossy) != 0 {
		t.Fatalf("one in twelve: %+v", s.Lossy)
	}
	play(m, 1, StateUp, v4(2, 3))
	if s := m.Statuses()[0]; len(s.Lossy) != 1 || s.Lossy[0].LossPercent != 16.667 || s.Lossy[0].Limit != 10 {
		t.Errorf("two in twelve: %+v", s.Lossy)
	}
}

func TestAThresholdOfZeroIsOff(t *testing.T) {
	t.Parallel()
	off, five := 0, 5
	g := wan
	g.SlowAboveMS, g.LossyAbovePercent = &off, &five
	m, _, _ := judging(t, g)
	tick(m, RiseAfter)
	play(m, 0, StateUp, v4(1, 1500))
	if s := m.Statuses()[0]; len(s.Slow) != 0 || len(s.Lossy) != 1 {
		t.Errorf("status = %+v", s)
	}
}

func TestDownEndsSlowAndLossyWithoutAWord(t *testing.T) {
	t.Parallel()
	m, _, _ := judging(t, wan)
	tick(m, RiseAfter)
	play(m, 0, StateUp, v4(3, 400))
	play(m, 1, StateDown, v4(12, 0))
	play(m, 2, StateUp, v4(0, 3))
	if got := kinds(m.History); !slices.Equal(got, []string{EventSlow + " IPv4", EventLossy + " IPv4"}) {
		t.Errorf("events = %v", got)
	}
}

func TestAFamilyThatNeverAnsweredIsNotJudged(t *testing.T) {
	t.Parallel()
	m, p, _ := judging(t, model.Gateway{Name: "wan", Enabled: true, Interface: "eth0", ProbeEverySeconds: 5})
	p.setFail("fe80::1", true)
	tick(m, FailAfter)
	play(m, 0, StateUp, v4(0, 3), FamilyMinute{Family: FamilyIPv6, Sent: 12, Lost: 12})
	if s := m.Statuses()[0]; !s.Online || len(s.Lossy) != 0 {
		t.Errorf("status = %+v", s)
	}
}

// Slow and lossy are judged over the whole minutes that hold ten probes.
func TestTheWindowHoldsTenProbes(t *testing.T) {
	t.Parallel()
	for every, want := range map[time.Duration]int{
		5 * time.Second: 1, 6 * time.Second: 1, 10 * time.Second: 2, 30 * time.Second: 5,
		time.Minute: 10, 5 * time.Minute: 50,
	} {
		if got := judgedMinutes(every); got != want {
			t.Errorf("every %s: %d minutes, want %d", every, got, want)
		}
	}
}

// A status says how many seconds of probes it is judged over, the browser
// tests' override included.
func TestAStatusSaysWhatItIsJudgedOver(t *testing.T) {
	t.Parallel()
	m := New(&fakeProber{fail: map[string]bool{}}, &fakeRouter{resolveTo: map[string]string{}}, slog.New(slog.DiscardHandler))
	m.Configure(&model.Config{Gateways: []model.Gateway{
		wan30, {Name: "lte", Enabled: true, Interface: "eth1", Address: "198.51.100.1", Priority: 1, ProbeEverySeconds: 10},
	}})
	if s := m.Statuses(); s[0].Span != 300 || s[1].Span != 120 {
		t.Errorf("spans = %d and %d, want 300 and 120", s[0].Span, s[1].Span)
	}
	m.ProbeEvery = time.Second
	if s := m.Statuses(); s[0].Span != 60 || s[1].Span != 60 {
		t.Errorf("overridden = %d and %d, want a minute each", s[0].Span, s[1].Span)
	}
}

// At 30 s a minute holds two probes, so one lost is half of it; over the
// five minutes that hold ten, one lost is not over 10 % and two are.
func TestOneLostProbeInFiveMinutesIsNotLossy(t *testing.T) {
	t.Parallel()
	m, _, log := judging(t, wan30)
	tick(m, RiseAfter)
	for i, lost := range []int{0, 0, 1, 0, 0} {
		play(m, i, StateUp, pair(lost, 3))
	}
	if s := m.Statuses()[0]; len(s.Lossy) != 0 {
		t.Fatalf("one in ten: %+v", s.Lossy)
	}
	play(m, 5, StateUp, pair(1, 3))
	if s := m.Statuses()[0]; len(s.Lossy) != 1 || s.Lossy[0].LossPercent != 20 || s.Lossy[0].Span != 300 ||
		!s.Lossy[0].Since.Equal(at(5, 0)) {
		t.Fatalf("two in ten: %+v", s.Lossy)
	}
	play(m, 6, StateUp, pair(0, 3))
	play(m, 7, StateUp, pair(0, 3))
	if s := m.Statuses()[0]; len(s.Lossy) != 0 {
		t.Fatalf("once the first has left the window: %+v", s.Lossy)
	}
	events := m.History.Events.Recent(0)
	if len(events) != 2 || events[1].Kind != EventLossy || events[1].LossPercent != 20 || events[1].Limit != 10 ||
		events[1].Span != 300 || events[0].Kind != EventLossyEnd || events[0].For != 180 {
		t.Errorf("events = %+v", events)
	}
	if got := log.String(); !logged(got, "gateway is losing packets", "over=5m0s") ||
		!logged(got, "gateway no longer loses packets", "over=5m0s") {
		t.Errorf("journal:\n%s", got)
	}
}

// A window is judged once it is full: a gateway's first minutes in the
// history say nothing however they went, and nor does a window with a
// minute missing.
func TestNothingIsJudgedUntilTheWindowIsFull(t *testing.T) {
	t.Parallel()
	m, _, _ := judging(t, wan30)
	tick(m, RiseAfter)
	m.History.ClearHistory()
	for i, lost := range []int{1, 0, 0, 0} {
		play(m, i, StateUp, pair(lost, 3))
		if s := m.Statuses()[0]; len(s.Lossy) != 0 {
			t.Fatalf("after minute %d: %+v", i, s.Lossy)
		}
	}
	play(m, 4, StateUp, pair(1, 3))
	if s := m.Statuses()[0]; len(s.Lossy) != 1 || s.Lossy[0].LossPercent != 20 || !s.Lossy[0].Since.Equal(at(4, 0)) {
		t.Fatalf("after the fifth: %+v", s.Lossy)
	}
	for i := 6; i < 10; i++ {
		play(m, i, StateUp, pair(0, 3))
		if s := m.Statuses()[0]; len(s.Lossy) != 1 {
			t.Fatalf("minute %d, with minute 5 missing from its window: %+v", i, s.Lossy)
		}
	}
	play(m, 10, StateUp, pair(0, 3))
	if s := m.Statuses()[0]; len(s.Lossy) != 0 {
		t.Errorf("after five minutes in a row: %+v", s.Lossy)
	}
}

// A minute the gateway was down or never answered in ends slow and lossy,
// and nothing is judged again until a full window of minutes up follows.
func TestAMinuteNotUpKeepsItsWindowsUnjudged(t *testing.T) {
	t.Parallel()
	for _, state := range []string{StateDown, StateNever} {
		m, _, _ := judging(t, wan30)
		tick(m, RiseAfter)
		for i := range 5 {
			play(m, i, StateUp, pair(1, 3))
		}
		play(m, 5, state, pair(2, 0))
		if s := m.Statuses()[0]; len(s.Lossy) != 0 {
			t.Fatalf("%s: after the minute %s: %+v", state, state, s.Lossy)
		}
		for i := 6; i < 10; i++ {
			play(m, i, StateUp, pair(1, 3))
			if s := m.Statuses()[0]; len(s.Lossy) != 0 {
				t.Fatalf("%s: minute %d, with minute 5 in its window: %+v", state, i, s.Lossy)
			}
		}
		play(m, 10, StateUp, pair(1, 3))
		if s := m.Statuses()[0]; len(s.Lossy) != 1 || !s.Lossy[0].Since.Equal(at(10, 0)) {
			t.Errorf("%s: after five minutes up: %+v", state, s.Lossy)
		}
		if got := kinds(m.History); !slices.Equal(got, []string{EventLossy + " IPv4", EventLossy + " IPv4"}) {
			t.Errorf("%s: events = %v", state, got)
		}
	}
}

// Minutes probed at another address are not in the window: after the
// monitor changes, it fills again from the first minute at the new one.
func TestAChangedMonitorFillsTheWindowAgain(t *testing.T) {
	t.Parallel()
	m, _, _ := judging(t, wan30)
	tick(m, RiseAfter)
	for i := range 5 {
		play(m, i, StateUp, pair(0, 3))
	}
	g := wan30
	g.Monitor = "192.0.2.53"
	m.Configure(&model.Config{Gateways: []model.Gateway{g}})
	m.Now = func() time.Time { return at(5, 0) }
	tick(m, RiseAfter)
	for i := 6; i < 9; i++ {
		play(m, i, StateUp, pair(1, 3))
		if s := m.Statuses()[0]; len(s.Lossy) != 0 {
			t.Fatalf("minute %d, with minutes at the old monitor in its window: %+v", i, s.Lossy)
		}
	}
	play(m, 9, StateUp, pair(1, 3))
	if s := m.Statuses()[0]; len(s.Lossy) != 1 || s.Lossy[0].LossPercent != 40 || !s.Lossy[0].Since.Equal(at(9, 0)) {
		t.Errorf("after five minutes at the new monitor: %+v", s.Lossy)
	}
}

// A cell is its worst minute: down over losing packets over slow over up,
// with how far past its threshold a slow or lossy one went.
func TestTheStripDrawsTheWorstMinuteOfEachCell(t *testing.T) {
	t.Parallel()
	h := NewHistory()
	probe := func(m, n, lost int, ms float64, state string) {
		for i := range n {
			at := at(m, i*5)
			h.Probe("wan", FamilyIPv4, "", at, time.Duration(ms*float64(time.Millisecond)), i >= lost)
			h.Mark("wan", state, "", at)
		}
	}
	probe(0, 12, 0, 3, StateUp)
	probe(10, 12, 0, 300, StateUp)
	probe(20, 12, 2, 3, StateUp)
	probe(21, 12, 0, 300, StateUp)
	probe(30, 12, 12, 0, StateDown)
	probe(31, 12, 0, 3, StateUp)
	h.Advance(at(40, 0))
	s := h.Strip("wan", at(40, 0), 200, 10, 1)
	if len(s.Cells) != StripCells {
		t.Fatalf("%d cells", len(s.Cells))
	}
	cells := map[int64]Cell{}
	for _, c := range s.Cells {
		if c.Kind != "" {
			cells[c.Start] = c
		}
	}
	want := map[int64]Cell{
		at(0, 0).Unix():  {Start: at(0, 0).Unix(), Kind: CellUp, LatencyMS: 3},
		at(10, 0).Unix(): {Start: at(10, 0).Unix(), Kind: CellSlow, Level: 0.5, LatencyMS: 300},
		at(20, 0).Unix(): {Start: at(20, 0).Unix(), Kind: CellLossy, Level: 0.07, LatencyMS: 300, LossPercent: 16.67},
		at(30, 0).Unix(): {Start: at(30, 0).Unix(), Kind: CellDown, Down: 1, LatencyMS: 3, LossPercent: 100},
	}
	if !maps.Equal(cells, want) {
		t.Errorf("cells = %+v\nwant %+v", cells, want)
	}
	if s.Down != 60 || s.WorstLatencyMS != 300 || s.WorstLatencyFamily != FamilyIPv4 || s.WorstLossPercent != 100 {
		t.Errorf("strip = %+v", s)
	}
	if off := h.Strip("wan", at(40, 0), 0, 0, 1); off.Cells[len(off.Cells)-2].Kind != CellDown {
		t.Errorf("without thresholds: %+v", off.Cells[len(off.Cells)-3:])
	}
	if empty := h.Strip("nobody", at(40, 0), 200, 10, 1); len(empty.Cells) != StripCells || empty.Cells[0].Kind != "" {
		t.Errorf("an unknown gateway: %+v", empty.Cells[0])
	}
	probe(41, 6, 0, 300, StateUp)
	if now := h.Strip("wan", at(41, 30), 200, 10, 1); now.Cells[len(now.Cells)-1].Kind != CellUp {
		t.Errorf("the minute under way: %+v, want up until it is judged", now.Cells[len(now.Cells)-1])
	}
}

// At 30 s a strip's minute is the window ending with it, as the monitor
// judged it: a minute half lost among good ones draws no amber cell, a
// window over the threshold does.
func TestTheStripJudgesAsTheMonitorDoes(t *testing.T) {
	t.Parallel()
	m, _, _ := judging(t, wan30)
	tick(m, RiseAfter)
	m.History.ClearHistory()
	lost := map[int]int{3: 1, 12: 1, 14: 1}
	judged := map[int64]string{}
	for i := range 40 {
		ms := 3.0
		if i >= 30 && i < 35 {
			ms = 250
		}
		play(m, i, StateUp, pair(lost[i], ms))
		kind, s := CellUp, m.Statuses()[0]
		switch {
		case len(s.Lossy) > 0:
			kind = CellLossy
		case len(s.Slow) > 0:
			kind = CellSlow
		}
		if cell := at(i-i%CellMinutes, 0).Unix(); cellRank(kind) > cellRank(judged[cell]) {
			judged[cell] = kind
		}
	}
	strip := m.History.Strip("wan", at(40, 0), model.DefaultSlowAboveMS, model.DefaultLossyAbovePercent,
		int(m.Statuses()[0].Span/60))
	drawn, cells := map[int64]string{}, map[int64]Cell{}
	for _, c := range strip.Cells {
		if c.Kind != "" {
			drawn[c.Start], cells[c.Start] = c.Kind, c
		}
	}
	want := map[int64]string{
		at(0, 0).Unix(): CellUp, at(10, 0).Unix(): CellLossy, at(20, 0).Unix(): CellUp, at(30, 0).Unix(): CellSlow,
	}
	if !maps.Equal(drawn, want) || !maps.Equal(judged, want) {
		t.Errorf("drawn %v, judged %v, want %v", drawn, judged, want)
	}
	if c := cells[at(10, 0).Unix()]; c.Level != 0.11 || c.LossPercent != 50 {
		t.Errorf("the lossy cell = %+v, want the window's level and the worst minute's loss", c)
	}
	if c := cells[at(30, 0).Unix()]; c.Level != 0.25 || c.LatencyMS != 250 {
		t.Errorf("the slow cell = %+v, want the window's level and the worst minute's latency", c)
	}
}

// The strip judges a family once it has answered at the monitor, as the
// monitor does: an IPv6 next hop that never answers paints nothing.
func TestTheStripJudgesAFamilyOnceItHasAnswered(t *testing.T) {
	t.Parallel()
	h := NewHistory()
	probe := func(m, answers int) {
		for i := range 12 {
			at := at(m, i*5)
			h.Probe("wan", FamilyIPv4, "", at, 3*time.Millisecond, true)
			h.Probe("wan", FamilyIPv6, "", at, 4*time.Millisecond, i < answers)
			h.Mark("wan", StateUp, "", at)
		}
	}
	probe(0, 0)
	h.Advance(at(1, 0))
	if s := h.Strip("wan", at(1, 0), 200, 10, 1); s.Cells[len(s.Cells)-1].Kind != CellUp {
		t.Fatalf("IPv6 never answered: %+v, want up", s.Cells[len(s.Cells)-1])
	}
	probe(10, 1)
	probe(20, 0)
	h.Advance(at(21, 0))
	if s := h.Strip("wan", at(21, 0), 200, 10, 1); s.Cells[len(s.Cells)-1].Kind != CellLossy ||
		s.Cells[len(s.Cells)-1].Level != 1 {
		t.Errorf("IPv6 lost again after answering once: %+v, want lossy", s.Cells[len(s.Cells)-1])
	}
}

// Each gateway is probed as often as it says, 30 s unless told, and the
// browser tests' override probes every one each second.
func TestGatewaysAreProbedAsOftenAsTheySay(t *testing.T) {
	t.Parallel()
	p := &fakeProber{fail: map[string]bool{}}
	m := New(p, &fakeRouter{resolveTo: map[string]string{}}, slog.New(slog.DiscardHandler))
	clock := at(0, 0)
	m.Now = func() time.Time { return clock }
	cfg := &model.Config{Gateways: []model.Gateway{
		{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1"},
		{Name: "lte", Enabled: true, Interface: "eth1", Address: "198.51.100.1", ProbeEverySeconds: 5},
	}}
	m.Configure(cfg)
	probes := func() int {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.probes
	}
	ctx := context.Background()
	m.tick(ctx, false)
	if n := probes(); n != 2 {
		t.Fatalf("first look probed %d, want both", n)
	}
	clock = at(0, 1)
	m.tick(ctx, false)
	if n := probes(); n != 2 {
		t.Fatalf("a second later probed %d more", n-2)
	}
	clock = at(0, 5)
	m.tick(ctx, false)
	if n := probes(); n != 3 {
		t.Fatalf("at five seconds probed %d, want lte alone", n-2)
	}
	clock = at(0, 30)
	m.tick(ctx, false)
	if n := probes(); n != 5 {
		t.Fatalf("at thirty seconds probed %d, want both", n-3)
	}
	cfg.Gateways[0].ProbeEverySeconds = 5
	clock = at(0, 31)
	m.Configure(cfg)
	m.tick(ctx, false)
	if n := probes(); n != 6 {
		t.Fatalf("after shortening wan's interval probed %d, want wan at once", n-5)
	}
	m.ProbeEvery = time.Second
	clock = at(0, 36)
	m.tick(ctx, false)
	clock = at(0, 37)
	m.tick(ctx, false)
	if n := probes(); n != 10 {
		t.Fatalf("overridden, probed %d over two seconds, want both twice", n-6)
	}
}

// A gateway says how many lost probes take it down and how many answers
// bring it back.
func TestAGatewaySaysHowManyProbesDecideIt(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	p := &fakeProber{fail: map[string]bool{}}
	m := watched(t, p, &fakeRouter{resolveTo: map[string]string{}}, &log,
		model.Gateway{Name: "wan", Enabled: true, Interface: "eth0", Address: "203.0.113.1", DownAfterProbes: 1, UpAfterProbes: 1},
		model.Gateway{Name: "lte", Enabled: true, Interface: "eth1", Address: "198.51.100.1", Priority: 1, DownAfterProbes: 5})
	tick(m, 1)
	if s := m.Statuses(); !s[0].Online || !s[1].Unknown {
		t.Fatalf("after one answer: %+v", s)
	}
	p.setFail("203.0.113.1", true)
	p.setFail("198.51.100.1", true)
	tick(m, 1)
	s := m.Statuses()
	if s[0].Online || s[0].NeverAnswered {
		t.Errorf("wan after one loss: %+v, want down", s[0])
	}
	tick(m, 3)
	if s := m.Statuses()[1]; !s.Unknown {
		t.Errorf("lte after four losses: %+v, want still unknown", s)
	}
	tick(m, 1)
	if s := m.Statuses()[1]; s.Unknown || s.Online || s.NeverAnswered {
		t.Errorf("lte after five losses: %+v, want down, since it answered once", s)
	}
	p.setFail("203.0.113.1", false)
	tick(m, 1)
	if s := m.Statuses()[0]; !s.Online {
		t.Errorf("wan after one answer: %+v, want up again", s)
	}
}
