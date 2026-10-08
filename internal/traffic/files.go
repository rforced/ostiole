package traffic

import (
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"ostiole/internal/logfile"
	"ostiole/internal/model"
)

// What traffic keeps in files while System, General writes the logs to
// them: a line for each minute a link or a device moved something or a
// link had errors, and a line for each hour of a destination. At start the
// minutes of a day, the hours of a month and the links' errors of
// ErrorsKept are rebuilt from them.
const (
	LinksFile        = "links"
	DevicesFile      = "devices"
	DestinationsFile = "destinations"
	FileVersion      = 1
	// MinuteDays is the most days minute lines are kept: the longest
	// window.
	MinuteDays = 31
	// recordsKept bounds what waits to be written, per log; the writer
	// takes it once half is waiting.
	recordsKept = 65536
)

// record is one line waiting to be written, numbered as it came.
type record[T any] struct {
	seq uint64
	at  time.Time
	v   T
}

// records is a ring of the lines waiting to be written.
type records[T any] struct {
	mu    sync.Mutex
	buf   []record[T]
	start int
	n     int
	seq   uint64
}

// push adds a line. The ring doubles towards recordsKept as lines come,
// so a router with the files off or few devices pays for what it has.
func (r *records[T]) push(at time.Time, v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	rec := record[T]{seq: r.seq, at: at, v: v}
	if r.n == len(r.buf) && len(r.buf) < recordsKept {
		next := make([]record[T], min(recordsKept, max(2*len(r.buf), 256)))
		for i := range r.n {
			next[i] = r.buf[(r.start+i)%len(r.buf)]
		}
		r.buf, r.start = next, 0
	}
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = rec
		r.n++
		return
	}
	r.buf[r.start] = rec
	r.start = (r.start + 1) % len(r.buf)
}

func (r *records[T]) after(seq uint64, out []record[T]) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := 0
	for i < r.n && r.buf[(r.start+i)%len(r.buf)].seq <= seq {
		i++
	}
	n := 0
	for ; i < r.n && n < len(out); i++ {
		out[n] = r.buf[(r.start+i)%len(r.buf)]
		n++
	}
	return n, i < r.n
}

func (r *records[T]) newest() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// MinuteLine is a minute of a link or a device as its files keep it. A
// link's minute carries the errors the kernel counted in it.
type MinuteLine struct {
	Time     time.Time `json:"time"`
	ID       string    `json:"id"`
	Down     uint64    `json:"down"`
	Up       uint64    `json:"up"`
	RXErrors uint64    `json:"rxErrors,omitempty"`
	TXErrors uint64    `json:"txErrors,omitempty"`
}

// HourLine is an hour of a destination as its files keep it: the row as
// the API reads it, and the hour.
type HourLine struct {
	Time time.Time `json:"time"`
	DestinationRow
}

// ParseMinute reads a minute line.
func ParseMinute(line []byte) (MinuteLine, time.Time, error) {
	var m MinuteLine
	if err := json.Unmarshal(line, &m); err != nil {
		return MinuteLine{}, time.Time{}, err
	}
	if m.ID == "" || m.Time.IsZero() {
		return MinuteLine{}, time.Time{}, errors.New("a minute line without its link or device")
	}
	return m, m.Time, nil
}

// ParseHour reads a destination's hour line.
func ParseHour(line []byte) (HourLine, time.Time, error) {
	var h HourLine
	if err := json.Unmarshal(line, &h); err != nil {
		return HourLine{}, time.Time{}, err
	}
	if h.Device == "" || h.Destination == "" || h.Time.IsZero() {
		return HourLine{}, time.Time{}, errors.New("an hour line without its device or destination")
	}
	return h, h.Time, nil
}

// FileLogs describe what traffic keeps in files to the writer: the links
// always, the devices while they are counted, and the destinations while
// they are recorded. The minute lines keep the files' days, at most a
// month; the destinations keep theirs as the other logs do.
func (c *Counter) FileLogs() []logfile.Log {
	minutes := func(r *records[MinuteLine]) func(uint64, int, func(uint64, time.Time, []byte)) bool {
		return logfile.Lines(r.after, recordSeq[MinuteLine], recordTime[MinuteLine],
			func() func([]byte, *record[MinuteLine]) []byte { return appendJSON[MinuteLine] })
	}
	return []logfile.Log{
		{
			Name: LinksFile, Version: FileVersion, MaxDays: MinuteDays,
			On:     func(*model.Config) bool { return true },
			Days:   func(*model.Config) int { return 0 },
			Newest: c.linkRecs.newest, Size: recordsSize,
			Lines: minutes(&c.linkRecs),
		},
		{
			Name: DevicesFile, Version: FileVersion, MaxDays: MinuteDays,
			On:     func(cfg *model.Config) bool { return cfg.Traffic.Devices },
			Days:   func(*model.Config) int { return 0 },
			Newest: c.deviceRecs.newest, Size: recordsSize,
			Lines: minutes(&c.deviceRecs),
		},
		{
			Name: DestinationsFile, Version: FileVersion,
			On: func(cfg *model.Config) bool { return cfg.Traffic.DestinationsOn() },
			Days: func(cfg *model.Config) int {
				return int(cfg.Traffic.Destinations.Retention() / (24 * time.Hour))
			},
			Newest: c.destRecs.newest, Size: recordsSize,
			Lines: logfile.Lines(c.destRecs.after, recordSeq[HourLine], recordTime[HourLine],
				func() func([]byte, *record[HourLine]) []byte { return appendJSON[HourLine] }),
		},
	}
}

func recordsSize() int                     { return recordsKept }
func recordSeq[T any](r *record[T]) uint64 { return r.seq }
func recordTime[T any](r *record[T]) time.Time {
	return r.at
}

func appendJSON[T any](buf []byte, r *record[T]) []byte {
	raw, err := json.Marshal(&r.v)
	if err != nil {
		return buf
	}
	return append(buf, raw...)
}

// closeMinutes hands each link's and device's last minute to the files
// once it is over: a line for each minute something moved or a link had
// errors. The caller holds c.mu.
func (c *Counter) closeMinutes(now time.Time) {
	minute := now.Truncate(time.Minute).Unix()
	closeOf := func(id string, s *series, errs *[]bucket, into *records[MinuteLine]) {
		// Every minute over and not yet written, however many a slow
		// tick let pass.
		i := len(s.minutes)
		for i > 0 && s.minutes[i-1].start > s.written {
			i--
		}
		for _, b := range s.minutes[i:] {
			if b.start >= minute {
				break
			}
			s.written = b.start
			t := time.Unix(b.start, 0).UTC()
			line := MinuteLine{Time: t, ID: id, Down: b.down, Up: b.up}
			// A link's errors in the minute go on its line.
			for errs != nil && len(*errs) > 0 && (*errs)[0].start <= b.start {
				if e := (*errs)[0]; e.start == b.start {
					line.RXErrors, line.TXErrors = e.down, e.up
				}
				*errs = (*errs)[1:]
			}
			into.push(t, line)
		}
	}
	for name, l := range c.links {
		closeOf(name, l.series, &l.errMinutes, &c.linkRecs)
	}
	for id, d := range c.devices {
		closeOf(id, d.series, nil, &c.deviceRecs)
	}
}

// Putting back what the files held goes BeginRestore, a RestoreMinute for
// each minute line and a RestoreHour for each hour line, oldest first,
// then EndRestore, all before counting starts: the minutes rebuild a day
// of minutes and a month of hours, and the points of the last five
// minutes start empty.

// BeginRestore reads the configuration, so what is counted is known.
func (c *Counter) BeginRestore(now time.Time) { c.follow(now) }

// RestoreMinute puts back a minute of a link, from LinksFile, or of a
// device, from DevicesFile.
func (c *Counter) RestoreMinute(file string, m MinuteLine, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var s *series
	switch {
	case file == LinksFile:
		if c.links == nil {
			c.links = map[string]*link{}
		}
		l := c.links[m.ID]
		if l == nil {
			l = &link{series: newSeries(m.Time)}
			c.links[m.ID] = l
		}
		s = l.series
		hour, cut := m.Time.Truncate(time.Hour).Unix(), errorsCut(now)
		if (m.RXErrors != 0 || m.TXErrors != 0) && hour >= cut {
			l.errs = addBucket(l.errs, hour, m.RXErrors, m.TXErrors, cut)
		}
	case file == DevicesFile && c.counting:
		d := c.devices[m.ID]
		if d == nil {
			d = restoredDevice(m.ID, m.Time)
			c.devices[m.ID] = d
		}
		if end := m.Time.Add(time.Minute); end.After(d.last) {
			d.last = end
		}
		s = d.series
	default:
		return
	}
	minuteCut, hourCut := now.Add(-minuteKept).Unix(), now.Add(-hourKept).Unix()
	start := m.Time.Unix()
	if start >= minuteCut {
		s.minutes = addBucket(s.minutes, start, m.Down, m.Up, minuteCut)
	}
	if start >= hourCut {
		s.hours = addBucket(s.hours, hourStart(m.Time, c.loc).Unix(), m.Down, m.Up, hourCut)
	}
	s.written = max(s.written, start)
}

// RestoreHour puts back an hour of a destination.
func (c *Counter) RestoreHour(h HourLine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.destOn {
		return
	}
	c.closed = append(c.closed, destRow{
		destKey: destKey{device: h.Device, dest: h.Destination, proto: protocolNumber(h.Protocol), port: h.Port},
		addr:    parseAddr(h.Address), hour: h.Time.Unix(), down: h.Down, up: h.Up,
		conns: uint32(min(max(h.Connections, 0), math.MaxUint32)), last: h.LastSeen.Unix(),
	})
}

// EndRestore keeps what came back to its bounds.
func (c *Counter) EndRestore(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counting {
		c.bound()
	}
	if c.destOn {
		c.rollHour(hourStart(now, c.loc).Unix(), now)
	}
}

// restoredDevice is a device the files knew, which says what it can of
// itself until it is seen again.
func restoredDevice(id string, since time.Time) *device {
	d := &device{id: id, router: id == RouterID, addrs: map[netip.Addr]time.Time{}, series: newSeries(since)}
	if hw, err := net.ParseMAC(id); err == nil && len(hw) == 6 && strings.Count(id, ":") == 5 {
		d.mac = id
	} else if a, err := netip.ParseAddr(id); err == nil {
		d.addrs[a] = since
	}
	return d
}

func parseAddr(s string) netip.Addr {
	a, _ := netip.ParseAddr(s)
	return a
}

// protocolNumber reads a protocol as protocolName writes it.
func protocolNumber(s string) uint8 {
	switch s {
	case "tcp":
		return 6
	case "udp":
		return 17
	case "icmp":
		return 1
	case "icmpv6":
		return 58
	}
	n, _ := strconv.ParseUint(s, 10, 8)
	return uint8(n)
}
