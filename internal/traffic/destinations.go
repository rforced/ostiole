package traffic

import (
	"net/netip"
	"sort"
	"strconv"
	"time"
)

// destKey is what a destination row is for: a device, what it talked to,
// and on which protocol and port.
type destKey struct {
	device string
	dest   string
	proto  uint8
	port   uint16
}

// destRow is one hour of a device's traffic with one destination.
type destRow struct {
	destKey
	// addr is the address it was reached at, last.
	addr     netip.Addr
	hour     int64 // unix seconds
	down, up uint64
	conns    uint32
	last     int64 // unix seconds
}

// toDest adds what a connection moved to its destination's row for this
// hour. The destination is fixed when the connection is first counted:
// the name the device asked the router for, else the address. The caller
// holds c.mu.
func (c *Counter) toDest(s *flowState, end int, device string, from, to netip.Addr, proto uint8, port uint16, up, down uint64, now time.Time) {
	var key destKey
	if s != nil && s.dest[end] != nil {
		key = *s.dest[end]
	} else {
		name := c.names.lookup(from, to)
		if name == "" {
			name = to.String()
		}
		key = destKey{device: device, dest: name, proto: proto, port: port}
		if s != nil {
			k := key
			s.dest[end] = &k
		}
	}
	hour := hourStart(now, c.loc).Unix()
	c.rollHour(hour, now)
	row := c.open[key]
	if row == nil {
		// An hour with more destinations than rows kept, a scan say,
		// records the first of them.
		if len(c.open) >= c.destSize {
			return
		}
		row = &destRow{destKey: key, hour: hour}
		c.open[key] = row
	}
	if s == nil || s.hour[end] != hour {
		row.conns++
		if s != nil {
			s.hour[end] = hour
		}
	}
	row.up += up
	row.down += down
	row.addr = to
	row.last = now.Unix()
}

// rollHour closes this hour's rows once the hour is over, and keeps the
// rows to their bounds: the oldest go past the most rows, and any older
// than the days kept. The caller holds c.mu.
func (c *Counter) rollHour(hour int64, now time.Time) {
	if c.open == nil {
		c.open = map[destKey]*destRow{}
	}
	if hour != c.openHour && len(c.open) > 0 {
		closing := make([]destRow, 0, len(c.open))
		for _, r := range c.open {
			closing = append(closing, *r)
		}
		sort.Slice(closing, func(i, j int) bool {
			if closing[i].device != closing[j].device {
				return closing[i].device < closing[j].device
			}
			return closing[i].dest < closing[j].dest
		})
		c.closed = append(c.closed, closing...)
		for _, r := range closing {
			t := time.Unix(r.hour, 0).UTC()
			c.destRecs.push(t, HourLine{Time: t, DestinationRow: r.row()})
		}
		clear(c.open)
	}
	c.openHour = hour
	cut := now.Add(-c.destKeep).Unix()
	drop := 0
	for drop < len(c.closed) && (len(c.closed)-drop+len(c.open) > c.destSize || c.closed[drop].hour+3600 <= cut) {
		drop++
	}
	if drop > 0 {
		c.closed = append(c.closed[:0], c.closed[drop:]...)
	}
}

// DestinationRow is what one device moved with one destination over a
// window.
type DestinationRow struct {
	Device      string    `json:"device"`
	Destination string    `json:"destination"`
	Address     string    `json:"address,omitempty"`
	Protocol    string    `json:"protocol"`
	Port        uint16    `json:"port,omitempty"`
	Down        uint64    `json:"down"`
	Up          uint64    `json:"up"`
	Connections int       `json:"connections"`
	LastSeen    time.Time `json:"lastSeen"`
}

// Destinations is what each device moved with each destination over the
// last span, one row for each, and how many hourly rows are held and from
// when. device narrows it to one.
func (c *Counter) Destinations(span time.Duration, device string) ([]DestinationRow, int, time.Time) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	from := hourStart(now.Add(-span), c.loc).Unix()
	sum := map[destKey]*DestinationRow{}
	add := func(r *destRow) {
		if r.hour < from || (device != "" && r.device != device) {
			return
		}
		out := sum[r.destKey]
		if out == nil {
			row := r.row()
			row.Down, row.Up, row.Connections, row.LastSeen = 0, 0, 0, time.Time{}
			out = &row
			sum[r.destKey] = out
		}
		out.Down += r.down
		out.Up += r.up
		out.Connections += int(r.conns)
		if last := time.Unix(r.last, 0); last.After(out.LastSeen) {
			out.LastSeen = last
			if r.addr.IsValid() {
				out.Address = r.addr.String()
			}
		}
	}
	for i := range c.closed {
		add(&c.closed[i])
	}
	for _, r := range c.open {
		add(r)
	}
	rows := make([]DestinationRow, 0, len(sum))
	for _, r := range sum {
		rows = append(rows, *r)
	}
	var oldest time.Time
	if len(c.closed) > 0 {
		oldest = time.Unix(c.closed[0].hour, 0)
	} else if len(c.open) > 0 {
		oldest = time.Unix(c.openHour, 0)
	}
	return rows, len(c.closed) + len(c.open), oldest
}

// row is the hour as the API reads a destination.
func (r *destRow) row() DestinationRow {
	out := DestinationRow{
		Device: r.device, Destination: r.dest, Protocol: protocolName(r.proto), Port: r.port,
		Down: r.down, Up: r.up, Connections: int(r.conns), LastSeen: time.Unix(r.last, 0).UTC(),
	}
	if r.addr.IsValid() {
		out.Address = r.addr.String()
	}
	return out
}

// protocolName is how a row names its protocol.
func protocolName(p uint8) string {
	switch p {
	case 6:
		return "tcp"
	case 17:
		return "udp"
	case 1:
		return "icmp"
	case 58:
		return "icmpv6"
	}
	return strconv.Itoa(int(p))
}
