package gateway

import (
	"cmp"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"ostiole/internal/logfile"
	"ostiole/internal/logring"
	"ostiole/internal/logsearch"
	"ostiole/internal/model"
)

// The families a gateway is probed in.
const (
	FamilyIPv4 = "IPv4"
	FamilyIPv6 = "IPv6"
)

// The states a gateway's minutes record, worst first.
const (
	StateDown  = "down"
	StateNever = "never"
	StateUp    = "up"
)

// The windows a history is read over.
const (
	Window5m  = "5m"
	Window24h = "24h"
	Window30d = "30d"
)

// Windows are the windows a page may ask for, shortest first.
var Windows = []string{Window5m, Window24h, Window30d}

const (
	fineKept   = 5 * time.Minute
	minuteKept = 24 * time.Hour
	hourKept   = 30 * 24 * time.Hour
	// minuteLines bounds the minute lines waiting for the files.
	minuteLines = 8192
)

// The history's logs under logfile.Dir and the format of their lines.
const (
	HistoryFileName = "gateways"
	EventsFileName  = "gateway-events"
	FileVersion     = 1
	// EventsKept and EventDays bound the events.
	EventsKept = 1000
	EventDays  = 30
)

// What an event says happened.
const (
	EventDown        = "down"
	EventUp          = "up"
	EventNever       = "never"
	EventAnswered    = "answered"
	EventFamilyDown  = "family-down"
	EventFamilyUp    = "family-up"
	EventFamilyNever = "family-never"
	EventMonitor     = "monitor"
	EventSlow        = "slow"
	EventSlowEnd     = "slow-end"
	EventLossy       = "lossy"
	EventLossyEnd    = "lossy-end"
)

// Event is something that changed on a gateway.
type Event struct {
	logring.Stamp
	Gateway string `json:"gateway"`
	Family  string `json:"family,omitempty"`
	Kind    string `json:"kind"`
	// For is how long the state that ended lasted, in seconds.
	For   int64  `json:"for,omitempty"`
	Error string `json:"error,omitempty"`
	// Monitor is the address probed after a change, Was the one before;
	// empty is the next hop.
	Monitor string `json:"monitor,omitempty"`
	Was     string `json:"was,omitempty"`
	// LatencyMS and LossPercent are the figures that crossed Limit, over the
	// last Span seconds of probes.
	LatencyMS   float64 `json:"latencyMs,omitempty"`
	LossPercent float64 `json:"lossPercent,omitempty"`
	Limit       int     `json:"limit,omitempty"`
	Span        int64   `json:"span,omitempty"`
}

// EventLog is the gateways' events.
type EventLog = logring.Ring[Event, *Event]

var eventWords = map[string]string{
	EventDown:        "down",
	EventUp:          "up again",
	EventNever:       "never answered",
	EventAnswered:    "answered",
	EventFamilyDown:  "stopped answering",
	EventFamilyUp:    "answering again",
	EventFamilyNever: "never answered",
	EventMonitor:     "monitor changed",
	EventSlow:        "slow",
	EventSlowEnd:     "no longer slow",
	EventLossy:       "losing packets",
	EventLossyEnd:    "no longer losing packets",
}

// Search hands a the values the Events card shows for an event.
func (e *Event) Search(a logsearch.Adder) {
	a.Add(e.Gateway)
	a.Add(e.Family)
	a.Add(eventWords[e.Kind])
	a.Add(e.Error)
	a.Add(e.Monitor)
	a.Add(e.Was)
}

// EventMatcher is a search's test of an event.
func EventMatcher(q logsearch.Query) func(*Event) bool {
	row := q.Row()
	return func(e *Event) bool {
		if q.Empty() {
			return true
		}
		row.Reset()
		e.Search(&row)
		return row.Match()
	}
}

// MinuteLine is a minute of a gateway as its files keep it.
type MinuteLine struct {
	logring.Stamp
	Gateway string `json:"gateway"`
	State   string `json:"state,omitempty"`
	// Monitor is the address probed, empty for the next hop.
	Monitor  string         `json:"monitor,omitempty"`
	Families []FamilyMinute `json:"families,omitempty"`
}

// FamilyMinute is what a family's probes found in a minute, in ms.
type FamilyMinute struct {
	Family string  `json:"family"`
	Sent   int     `json:"sent"`
	Lost   int     `json:"lost,omitempty"`
	Mean   float64 `json:"mean,omitempty"`
	Low    float64 `json:"low,omitempty"`
	High   float64 `json:"high,omitempty"`
}

// ParseMinute reads a minute line.
func ParseMinute(line []byte) (MinuteLine, time.Time, error) {
	var m MinuteLine
	if err := json.Unmarshal(line, &m); err != nil {
		return MinuteLine{}, time.Time{}, err
	}
	if m.Gateway == "" || m.Time.IsZero() {
		return MinuteLine{}, time.Time{}, errors.New("a minute line without its gateway")
	}
	return m, m.Time, nil
}

// Probe is one probe as the stream sends it.
type Probe struct {
	Gateway string    `json:"gateway"`
	Family  string    `json:"family"`
	Time    time.Time `json:"time"`
	// LatencyMS is the round trip; a lost probe has none.
	LatencyMS float64 `json:"latencyMs,omitempty"`
	Lost      bool    `json:"lost,omitempty"`
}

// Minute is a gateway's minute once it is over.
type Minute struct {
	Gateway  string
	Start    time.Time
	State    string
	Monitor  string
	Families []FamilyMinute
}

type figures struct {
	sent, lost     int
	sum, low, high float64
}

func (f *figures) add(ms float64, ok bool) {
	f.sent++
	if !ok {
		f.lost++
		return
	}
	if f.sent-f.lost == 1 || ms < f.low {
		f.low = ms
	}
	if f.sent-f.lost == 1 || ms > f.high {
		f.high = ms
	}
	f.sum += ms
}

func (f *figures) merge(o figures) {
	if o.sent == 0 {
		return
	}
	if o.sent > o.lost {
		if f.sent == f.lost || o.low < f.low {
			f.low = o.low
		}
		if f.sent == f.lost || o.high > f.high {
			f.high = o.high
		}
	}
	f.sent += o.sent
	f.lost += o.lost
	f.sum += o.sum
}

func (f figures) answered() int { return f.sent - f.lost }

func (f figures) mean() float64 {
	if n := f.answered(); n > 0 {
		return f.sum / float64(n)
	}
	return 0
}

func (f figures) loss() float64 {
	if f.sent == 0 {
		return 0
	}
	return float64(f.lost) / float64(f.sent) * 100
}

// slow is how far past slowAt the mean round trip went, from 0 to 1, and
// whether it did.
func (f figures) slow(slowAt int) (float64, bool) {
	m := f.mean()
	if slowAt <= 0 || f.answered() == 0 || m <= float64(slowAt) {
		return 0, false
	}
	return min(1, (m-float64(slowAt))/float64(slowAt)), true
}

// lossy is how far past lossyAt percent the share lost went, from 0 to 1,
// and whether it did.
func (f figures) lossy(lossyAt int) (float64, bool) {
	l := f.loss()
	if lossyAt <= 0 || l <= float64(lossyAt) {
		return 0, false
	}
	return min(1, (l-float64(lossyAt))/float64(100-lossyAt)), true
}

// worst is the worst minute a bucket holds in a family.
type worst struct {
	mean, loss float64
}

type bucket struct {
	start   int64
	monitor string
	fam     [2]figures
	worst   [2]worst
	state   string
	// up, down and never count the bucket's minutes in each state.
	up, down, never int
}

func familyIndex(family string) int {
	if family == FamilyIPv6 {
		return 1
	}
	return 0
}

func familyName(i int) string {
	if i == 1 {
		return FamilyIPv6
	}
	return FamilyIPv4
}

func rank(state string) int {
	switch state {
	case StateDown:
		return 3
	case StateNever:
		return 2
	case StateUp:
		return 1
	}
	return 0
}

func (b *bucket) finish() {
	for i := range b.fam {
		if f := b.fam[i]; f.sent > 0 {
			b.worst[i] = worst{mean: f.mean(), loss: f.loss()}
		}
	}
	switch b.state {
	case StateUp:
		b.up = 1
	case StateDown:
		b.down = 1
	case StateNever:
		b.never = 1
	}
}

func (b *bucket) fold(m bucket) {
	for i := range b.fam {
		b.fam[i].merge(m.fam[i])
		if m.fam[i].sent > 0 {
			b.worst[i].mean = max(b.worst[i].mean, m.worst[i].mean)
			b.worst[i].loss = max(b.worst[i].loss, m.worst[i].loss)
		}
	}
	b.up += m.up
	b.down += m.down
	b.never += m.never
	b.monitor = m.monitor
	if rank(m.state) > rank(b.state) {
		b.state = m.state
	}
}

func (b *bucket) families() []FamilyMinute {
	var out []FamilyMinute
	for i, f := range b.fam {
		if f.sent == 0 {
			continue
		}
		fm := FamilyMinute{Family: familyName(i), Sent: f.sent, Lost: f.lost}
		if f.answered() > 0 {
			fm.Mean, fm.Low, fm.High = round(f.mean()), round(f.low), round(f.high)
		}
		out = append(out, fm)
	}
	return out
}

func round(ms float64) float64 { return math.Round(ms*1000) / 1000 }

type probe struct {
	at     int64
	family int
	ms     float64
	ok     bool
}

type series struct {
	fine    []probe
	cur     *bucket
	minutes []bucket
	hours   []bucket
}

// History keeps what the monitor measured of each gateway and what
// changed: every probe for five minutes, a minute for a day, an hour for
// a month, and the events.
type History struct {
	mu     sync.Mutex
	series map[string]*series
	closed []Minute
	subs   map[chan Probe]struct{}
	// answered is the monitor each gateway's family last answered at.
	answered map[string]map[string]string
	// totals count each family's probes answered and lost since the start.
	totals  map[string]map[string]*[2]uint64
	minutes *logring.Ring[MinuteLine, *MinuteLine]
	// Events is what changed, kept EventDays.
	Events *EventLog
}

// NewHistory returns an empty history.
func NewHistory() *History {
	return &History{
		series:   map[string]*series{},
		subs:     map[chan Probe]struct{}{},
		answered: map[string]map[string]string{},
		totals:   map[string]map[string]*[2]uint64{},
		minutes:  logring.New[MinuteLine, *MinuteLine](minuteLines, 0),
		Events:   logring.New[Event, *Event](EventsKept, EventDays*24*time.Hour),
	}
}

func (h *History) seriesOf(name string) *series {
	s := h.series[name]
	if s == nil {
		s = &series{}
		h.series[name] = s
	}
	return s
}

// Probe records one probe of a gateway in a family.
func (h *History) Probe(gateway, family, monitor string, at time.Time, rtt time.Duration, ok bool) {
	ms := float64(rtt.Microseconds()) / 1000
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.seriesOf(gateway)
	i := familyIndex(family)
	s.fine = append(s.fine, probe{at: at.UnixMilli(), family: i, ms: ms, ok: ok})
	cut := at.Add(-fineKept).UnixMilli()
	n := 0
	for n < len(s.fine) && s.fine[n].at < cut {
		n++
	}
	s.fine = s.fine[n:]
	h.bucketAt(gateway, s, at, monitor).fam[i].add(ms, ok)
	if h.totals[gateway] == nil {
		h.totals[gateway] = map[string]*[2]uint64{}
	}
	t := h.totals[gateway][family]
	if t == nil {
		t = &[2]uint64{}
		h.totals[gateway][family] = t
	}
	if ok {
		t[0]++
	} else {
		t[1]++
	}
	if ok {
		if h.answered[gateway] == nil {
			h.answered[gateway] = map[string]string{}
		}
		h.answered[gateway][family] = monitor
	}
	p := Probe{Gateway: gateway, Family: family, Time: at}
	if ok {
		p.LatencyMS = round(ms)
	} else {
		p.Lost = true
	}
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

// Mark records the state a gateway was in at a tick.
func (h *History) Mark(gateway, state, monitor string, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.bucketAt(gateway, h.seriesOf(gateway), at, monitor)
	if rank(state) > rank(b.state) {
		b.state = state
	}
}

func (h *History) bucketAt(name string, s *series, at time.Time, monitor string) *bucket {
	start := at.Truncate(time.Minute).Unix()
	if s.cur != nil && start > s.cur.start {
		h.close(name, s)
	}
	if s.cur == nil {
		s.cur = &bucket{start: start}
	}
	s.cur.monitor = monitor
	return s.cur
}

func (h *History) close(name string, s *series) {
	b := *s.cur
	s.cur = nil
	b.finish()
	s.add(b)
	start := time.Unix(b.start, 0).UTC()
	line := MinuteLine{
		Stamp: logring.Stamp{Time: start}, Gateway: name, State: b.state, Monitor: b.monitor, Families: b.families(),
	}
	h.minutes.Add(line)
	h.closed = append(h.closed, Minute{
		Gateway: name, Start: start, State: b.state, Monitor: b.monitor, Families: line.Families,
	})
}

// add keeps a finished minute and folds it into its hour.
func (s *series) add(b bucket) {
	s.minutes = append(s.minutes, b)
	cut := b.start - int64(minuteKept/time.Second)
	n := 0
	for n < len(s.minutes) && s.minutes[n].start <= cut {
		n++
	}
	s.minutes = s.minutes[n:]
	hour := b.start - b.start%3600
	if k := len(s.hours); k > 0 && s.hours[k-1].start == hour {
		s.hours[k-1].fold(b)
	} else {
		hb := bucket{start: hour}
		hb.fold(b)
		s.hours = append(s.hours, hb)
	}
	cut = b.start - int64(hourKept/time.Second)
	n = 0
	for n < len(s.hours) && s.hours[n].start <= cut {
		n++
	}
	s.hours = s.hours[n:]
}

// Advance closes every minute that is over at now and returns the minutes
// closed since the last call.
func (h *History) Advance(now time.Time) []Minute {
	h.mu.Lock()
	defer h.mu.Unlock()
	start := now.Truncate(time.Minute).Unix()
	for name, s := range h.series {
		if s.cur != nil && s.cur.start < start {
			h.close(name, s)
		}
	}
	out := h.closed
	h.closed = nil
	return out
}

// window adds up a gateway's w minutes ending with the one that started at
// start, and reports whether they make a full window at monitor.
func (h *History) window(gateway string, start time.Time, w int, monitor string) ([2]figures, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.series[gateway]; s != nil {
		for i := len(s.minutes) - 1; i >= 0 && s.minutes[i].start >= start.Unix(); i-- {
			if s.minutes[i].start == start.Unix() {
				return windowAt(s.minutes, i, w, monitor)
			}
		}
	}
	return [2]figures{}, false
}

// windowAt adds up the w minutes ending with minutes[i], and reports whether
// they make a full window: w minutes in a row, the gateway up in each and
// probed at monitor.
func windowAt(minutes []bucket, i, w int, monitor string) ([2]figures, bool) {
	var sum [2]figures
	if i+1 < w {
		return sum, false
	}
	for k := i - w + 1; k <= i; k++ {
		b := &minutes[k]
		if b.start != minutes[i].start-int64(i-k)*60 || b.state != StateUp || b.monitor != monitor {
			return [2]figures{}, false
		}
		for f := range sum {
			sum[f].merge(b.fam[f])
		}
	}
	return sum, true
}

// Keep forgets the gateways not named.
func (h *History) Keep(names []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name := range h.series {
		if !slices.Contains(names, name) {
			delete(h.series, name)
			delete(h.answered, name)
			delete(h.totals, name)
		}
	}
}

// Answered reports whether a gateway's family has answered at monitor in
// what the history holds.
func (h *History) Answered(gateway, family, monitor string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	m, ok := h.answered[gateway][family]
	return ok && m == monitor
}

// Probes counts a gateway's probes in a family since the start, answered
// and lost.
func (h *History) Probes(gateway, family string) (answered, lost uint64, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.totals[gateway][family]
	if t == nil {
		return 0, 0, false
	}
	return t[0], t[1], true
}

// Note adds an event.
func (h *History) Note(e Event) { h.Events.Add(e) }

// Subscribe returns a channel of probes and a cancel function.
func (h *History) Subscribe(buffer int) (<-chan Probe, func()) {
	ch := make(chan Probe, buffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// ClearHistory forgets every gateway's figures. The events stay.
func (h *History) ClearHistory() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.series = map[string]*series{}
	h.closed = nil
	h.answered = map[string]map[string]string{}
	h.minutes.Clear()
}

// FileLogs describe the history and the events to the writer that keeps
// them in files.
func (h *History) FileLogs() []logfile.Log {
	minutes := h.minutes.Files(HistoryFileName, FileVersion, func(*model.Config) bool { return true })
	minutes.MaxDays = int(hourKept / (24 * time.Hour))
	events := h.Events.Files(EventsFileName, FileVersion, func(*model.Config) bool { return true })
	events.MaxDays = EventDays
	return []logfile.Log{minutes, events}
}

// EventSettings sizes the events.
func EventSettings(*model.Config) (int, time.Duration) {
	return EventsKept, EventDays * 24 * time.Hour
}

// RestoreMinute puts back a minute read from the files, in the order
// they hold them.
func (h *History) RestoreMinute(m MinuteLine, now time.Time) {
	start := m.Time.Truncate(time.Minute).Unix()
	if start <= now.Add(-hourKept).Unix() || start > now.Unix() {
		return
	}
	b := bucket{start: start, monitor: m.Monitor, state: m.State}
	for _, f := range m.Families {
		i := familyIndex(f.Family)
		answered := max(f.Sent-f.Lost, 0)
		b.fam[i] = figures{sent: f.Sent, lost: f.Lost, sum: f.Mean * float64(answered), low: f.Low, high: f.High}
	}
	b.finish()
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.seriesOf(m.Gateway)
	if start > now.Add(-minuteKept).Unix() {
		s.minutes = append(s.minutes, b)
	}
	hour := start - start%3600
	if k := len(s.hours); k > 0 && s.hours[k-1].start == hour {
		s.hours[k-1].fold(b)
	} else {
		hb := bucket{start: hour}
		hb.fold(b)
		s.hours = append(s.hours, hb)
	}
	for _, f := range m.Families {
		if f.Sent > f.Lost {
			if h.answered[m.Gateway] == nil {
				h.answered[m.Gateway] = map[string]string{}
			}
			h.answered[m.Gateway][f.Family] = m.Monitor
		}
	}
}

// EndRestore puts the restored minutes in order and numbers new lines
// after newest, the highest number the files hold.
func (h *History) EndRestore(newest uint64) {
	h.mu.Lock()
	for _, s := range h.series {
		slices.SortStableFunc(s.minutes, func(a, b bucket) int { return cmp.Compare(a.start, b.start) })
		slices.SortStableFunc(s.hours, func(a, b bucket) int { return cmp.Compare(a.start, b.start) })
		var hours []bucket
		for _, hb := range s.hours {
			if k := len(hours); k > 0 && hours[k-1].start == hb.start {
				hours[k-1].fold(hb)
				continue
			}
			hours = append(hours, hb)
		}
		s.hours = hours
	}
	h.mu.Unlock()
	_ = h.minutes.Restore(nil, newest)
}

// Point is a gateway's figures in a family at a moment: the mean round
// trip with its low and high, and the share lost.
type Point struct {
	T        int64
	Mean     float64
	Low      float64
	High     float64
	Loss     float64
	Answered bool
}

// MarshalJSON writes [seconds, mean, low, high, loss], the latency null
// where nothing answered.
func (p Point) MarshalJSON() ([]byte, error) {
	b := []byte{'['}
	b = strconv.AppendInt(b, p.T, 10)
	for _, v := range []float64{p.Mean, p.Low, p.High} {
		b = append(b, ',')
		if p.Answered {
			b = strconv.AppendFloat(b, round(v), 'f', -1, 64)
		} else {
			b = append(b, "null"...)
		}
	}
	b = append(b, ',')
	b = strconv.AppendFloat(b, math.Round(p.Loss*100)/100, 'f', -1, 64)
	return append(b, ']'), nil
}

// UnmarshalJSON reads what MarshalJSON writes.
func (p *Point) UnmarshalJSON(raw []byte) error {
	var v [5]*float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v[0] == nil || v[4] == nil {
		return errors.New("a point without its time or loss")
	}
	*p = Point{T: int64(*v[0]), Loss: *v[4]}
	if v[1] != nil && v[2] != nil && v[3] != nil {
		p.Mean, p.Low, p.High, p.Answered = *v[1], *v[2], *v[3], true
	}
	return nil
}

// FamilyReport is a family over a window.
type FamilyReport struct {
	Family string  `json:"family"`
	Points []Point `json:"points"`
	// Mean is over every answer, Worst the worst minute's mean (or
	// probe's, over five minutes), Loss the share lost and WorstLoss the
	// worst minute's.
	Mean      float64 `json:"mean"`
	Worst     float64 `json:"worst"`
	Loss      float64 `json:"loss"`
	WorstLoss float64 `json:"worstLoss"`
	Sent      int     `json:"sent"`
}

// Report is a gateway over a window.
type Report struct {
	Window   string         `json:"window"`
	Now      time.Time      `json:"now"`
	Families []FamilyReport `json:"families"`
	// Down and Never are how long the gateway was down and unanswered in
	// the window, in seconds.
	Down  int64 `json:"down"`
	Never int64 `json:"never"`
}

// Read reports a gateway over a window ending at now.
func (h *History) Read(gateway, window string, now time.Time) Report {
	r := Report{Window: window, Now: now, Families: []FamilyReport{}}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.series[gateway]
	if s == nil {
		return r
	}
	var fams [2]*FamilyReport
	report := func(i int) *FamilyReport {
		if fams[i] == nil {
			fams[i] = &FamilyReport{Family: familyName(i), Points: []Point{}}
		}
		return fams[i]
	}
	var total [2]figures
	switch window {
	case Window5m:
		cut := now.Add(-fineKept).UnixMilli()
		for _, p := range s.fine {
			if p.at < cut {
				continue
			}
			f := report(p.family)
			pt := Point{T: p.at / 1000, Answered: p.ok}
			if p.ok {
				pt.Mean, pt.Low, pt.High = p.ms, p.ms, p.ms
				f.Worst = max(f.Worst, p.ms)
			} else {
				pt.Loss = 100
			}
			f.Points = append(f.Points, pt)
			total[p.family].add(p.ms, p.ok)
		}
		from := now.Add(-fineKept).Truncate(time.Minute).Unix()
		for _, b := range s.minutes {
			if b.start >= from {
				r.Down += int64(b.down) * 60
				r.Never += int64(b.never) * 60
			}
		}
		if b := s.cur; b != nil {
			switch b.state {
			case StateDown:
				r.Down += 60
			case StateNever:
				r.Never += 60
			}
		}
	default:
		buckets, from := s.minutes, now.Add(-minuteKept).Unix()
		if window == Window30d {
			buckets, from = s.hours, now.Add(-hourKept).Unix()
		}
		for _, b := range buckets {
			if b.start <= from {
				continue
			}
			for i, fig := range b.fam {
				if fig.sent == 0 {
					continue
				}
				f := report(i)
				f.Points = append(f.Points, Point{
					T: b.start, Mean: fig.mean(), Low: fig.low, High: fig.high, Loss: fig.loss(), Answered: fig.answered() > 0,
				})
				f.Worst = max(f.Worst, b.worst[i].mean)
				f.WorstLoss = max(f.WorstLoss, b.worst[i].loss)
				total[i].merge(fig)
			}
			r.Down += int64(b.down) * 60
			r.Never += int64(b.never) * 60
		}
	}
	for i, f := range fams {
		if f == nil {
			continue
		}
		f.Mean, f.Loss, f.Sent = round(total[i].mean()), math.Round(total[i].loss()*100)/100, total[i].sent
		f.Worst = round(f.Worst)
		if window == Window5m {
			f.WorstLoss = f.Loss
		}
		r.Families = append(r.Families, *f)
	}
	return r
}

// The kinds a strip's cell is, as the worst of its minutes, worst first.
const (
	CellDown  = "down"
	CellLossy = "lossy"
	CellSlow  = "slow"
	CellUp    = "up"
	CellNever = "never"
)

// StripCells is how many cells a day's strip has, and CellMinutes how
// many minutes each covers.
const (
	StripCells  = 144
	CellMinutes = 10
)

func cellRank(kind string) int {
	switch kind {
	case CellDown:
		return 5
	case CellLossy:
		return 4
	case CellSlow:
		return 3
	case CellUp:
		return 2
	case CellNever:
		return 1
	}
	return 0
}

// Cell is ten minutes of a gateway as the dashboard's strip draws it: the
// kind of its worst minute, how far past its threshold that went from 0 to
// 1, the minutes down, and the worst minute's mean round trip and loss.
type Cell struct {
	Start       int64   `json:"start"`
	Kind        string  `json:"kind,omitempty"`
	Level       float64 `json:"level,omitempty"`
	Down        int     `json:"down,omitempty"`
	LatencyMS   float64 `json:"latencyMs,omitempty"`
	LossPercent float64 `json:"lossPercent,omitempty"`
}

// Strip is a gateway's last day in ten-minute cells, oldest first, with the
// day's time down in seconds and its worst minutes.
type Strip struct {
	Cells              []Cell  `json:"cells"`
	Down               int64   `json:"down"`
	WorstLatencyMS     float64 `json:"worstLatencyMs"`
	WorstLatencyFamily string  `json:"worstLatencyFamily,omitempty"`
	WorstLossPercent   float64 `json:"worstLossPercent"`
	WorstLossFamily    string  `json:"worstLossFamily,omitempty"`
}

// stateKind is what a minute was by its state alone.
func stateKind(b bucket) string {
	switch b.state {
	case StateDown:
		return CellDown
	case StateNever:
		return CellNever
	}
	for _, f := range b.fam {
		if f.sent > 0 && (b.state != "" || f.answered() > 0) {
			return CellUp
		}
	}
	return ""
}

// overKind is what a full window was against the thresholds, lossy over
// slow, and how far past; empty where it was neither.
func overKind(fam [2]figures, slowAt, lossyAt int) (string, float64) {
	kind, level := "", 0.0
	for _, f := range fam {
		if lv, ok := f.lossy(lossyAt); ok && (kind != CellLossy || lv > level) {
			kind, level = CellLossy, lv
		}
		if lv, ok := f.slow(slowAt); ok && kind != CellLossy {
			kind, level = CellSlow, max(level, lv)
		}
	}
	return kind, level
}

// Strip draws a gateway's last day ending at now, judged against its
// thresholds as they are and as the monitor judges them: a closed minute
// the gateway was up in is slow or lossy when the window minutes ending
// with it are, in a family that has answered at its monitor.
func (h *History) Strip(gateway string, now time.Time, slowAt, lossyAt, window int) Strip {
	const span = CellMinutes * 60
	end := now.Unix() - now.Unix()%span + span
	start := end - StripCells*span
	out := Strip{Cells: make([]Cell, StripCells)}
	for i := range out.Cells {
		out.Cells[i].Start = start + int64(i*span)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.series[gateway]
	if s == nil {
		return out
	}
	window = max(window, 1)
	answered := h.answered[gateway]
	minutes := slices.Clone(s.minutes)
	if s.cur != nil {
		b := *s.cur
		b.finish()
		minutes = append(minutes, b)
	}
	for i, b := range minutes {
		if b.start < start || b.start >= end {
			continue
		}
		c := &out.Cells[(b.start-start)/span]
		kind, level := stateKind(b), 0.0
		if i < len(s.minutes) && b.state == StateUp {
			if fam, full := windowAt(s.minutes, i, window, b.monitor); full {
				for f := range fam {
					if monitor, ok := answered[familyName(f)]; !ok || monitor != b.monitor {
						fam[f] = figures{}
					}
				}
				if k, lv := overKind(fam, slowAt, lossyAt); k != "" {
					kind, level = k, lv
				}
			}
		}
		if r := cellRank(kind); r > cellRank(c.Kind) || (r == cellRank(c.Kind) && level > c.Level) {
			c.Kind, c.Level = kind, math.Round(level*100)/100
		}
		c.Down += b.down
		out.Down += int64(b.down) * 60
		for i, f := range b.fam {
			if f.sent == 0 {
				continue
			}
			loss := f.loss()
			c.LossPercent = max(c.LossPercent, math.Round(loss*100)/100)
			if loss > out.WorstLossPercent {
				out.WorstLossPercent, out.WorstLossFamily = math.Round(loss*100)/100, familyName(i)
			}
			if f.answered() == 0 {
				continue
			}
			m := round(f.mean())
			c.LatencyMS = max(c.LatencyMS, m)
			if m > out.WorstLatencyMS {
				out.WorstLatencyMS, out.WorstLatencyFamily = m, familyName(i)
			}
		}
	}
	return out
}
