package traffic

import (
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"
)

// Rate is a link's or a device's rate now, in bits per second.
type Rate struct {
	ID   string  `json:"id"`
	Down float64 `json:"down"`
	Up   float64 `json:"up"`
}

// Event is what the stream sends: every link's rate each second, and
// every device's that moved in the last five minutes after each read of
// the connection table.
type Event struct {
	Kind string    `json:"kind"` // links or devices
	Time time.Time `json:"time"`
	// Interval is how often devices are read now, in seconds.
	Interval float64 `json:"interval,omitempty"`
	Links    []Rate  `json:"links,omitempty"`
	Devices  []Rate  `json:"devices,omitempty"`
}

// Subscribe returns a channel of events and a cancel function. A
// subscriber too slow to take one loses it.
func (c *Counter) Subscribe(n int) (<-chan Event, func()) {
	ch := make(chan Event, n)
	c.mu.Lock()
	if c.subs == nil {
		c.subs = map[chan Event]struct{}{}
	}
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

// publish hands an event to every subscriber. The caller holds c.mu.
func (c *Counter) publish(ev Event) {
	for ch := range c.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// LinkReport is one link over a window.
type LinkReport struct {
	Name string `json:"name"`
	// Down and Up are what it is receiving and sending now.
	Down   float64 `json:"down"`
	Up     float64 `json:"up"`
	Totals Totals  `json:"totals"`
	// Since is when counting began, which a longer window reaches
	// back past.
	Since  time.Time `json:"since"`
	Points []Point   `json:"points"`
}

// LinkReports is every link over a window, by name.
func (c *Counter) LinkReports(window string) []LinkReport {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]LinkReport, 0, len(c.links))
	for name, l := range c.links {
		pts, tot := l.series.read(window, now, c.loc)
		out = append(out, LinkReport{Name: name, Down: l.down, Up: l.up, Totals: tot, Since: l.series.since, Points: pts})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Errors are what the kernel counted as errors on a link, each way.
type Errors struct{ RX, TX uint64 }

// LinkErrors is every link with errors in the last ErrorsKept, by name.
// An idle link's old hours are only let go of at its next error, so they
// are left out here.
func (c *Counter) LinkErrors() map[string]Errors {
	cut := errorsCut(c.now())
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]Errors{}
	for name, l := range c.links {
		var e Errors
		for _, b := range l.errs {
			if b.start >= cut {
				e.RX += b.down
				e.TX += b.up
			}
		}
		if e != (Errors{}) {
			out[name] = e
		}
	}
	return out
}

// DeviceReport is one device over a window.
type DeviceReport struct {
	// ID is its hardware address, its address where there is none, or
	// RouterID.
	ID  string `json:"id"`
	MAC string `json:"mac,omitempty"`
	// Router marks the router's own row.
	Router bool `json:"router,omitempty"`
	// Addresses are those it used in the last month, the latest first.
	Addresses []string `json:"addresses"`
	Interface string   `json:"interface,omitempty"`
	// Down and Up are what it received and sent over the last read.
	Down     float64   `json:"down"`
	Up       float64   `json:"up"`
	Totals   Totals    `json:"totals"`
	Since    time.Time `json:"since"`
	LastSeen time.Time `json:"lastSeen"`
	Points   []Point   `json:"points,omitempty"`
}

// Status is whether devices are counted, since when, and how often.
type Status struct {
	Counting bool       `json:"counting"`
	Since    *time.Time `json:"since,omitempty"`
	// Interval is how often the connection table is read, in seconds.
	Interval float64 `json:"interval,omitempty"`
	// Error is why the table could not be read last time.
	Error string `json:"error,omitempty"`
}

// DestinationsOn reports whether destinations are recorded.
func (c *Counter) DestinationsOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.destOn
}

// Status says whether devices are counted.
func (c *Counter) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.counting {
		return Status{}
	}
	since := c.since
	st := Status{Counting: true, Since: &since, Interval: c.interval.Seconds()}
	if c.failed != "" {
		st.Error = "Could not read the connection table: " + c.failed
		if strings.Contains(c.failed, "operation not permitted") {
			st.Error += " (the daemon is not running as root)"
		}
	}
	return st
}

// DeviceReports is every device that moved something in the window, with
// what it moved, the busiest first.
func (c *Counter) DeviceReports(window string) []DeviceReport {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]DeviceReport, 0, len(c.devices))
	for _, d := range c.devices {
		_, tot := d.series.read(window, now, c.loc)
		if tot.Down == 0 && tot.Up == 0 && d.down == 0 && d.up == 0 {
			continue
		}
		r := d.report()
		r.Totals = tot
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Totals.Down+out[i].Totals.Up, out[j].Totals.Down+out[j].Totals.Up
		if a != b {
			return a > b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// DeviceReport is one device over a window, with its points.
func (c *Counter) DeviceReport(id, window string) (DeviceReport, bool) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.devices[id]
	if !ok {
		return DeviceReport{}, false
	}
	r := d.report()
	r.Points, r.Totals = d.series.read(window, now, c.loc)
	return r, true
}

// report is the device without its window's figures. The caller holds
// c.mu.
func (d *device) report() DeviceReport {
	r := DeviceReport{
		ID: d.id, MAC: d.mac, Router: d.router, Interface: d.link,
		Down: d.down, Up: d.up, Since: d.series.since, LastSeen: d.last, Addresses: []string{},
	}
	addrs := make([]netip.Addr, 0, len(d.addrs))
	for a := range d.addrs {
		addrs = append(addrs, a)
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int {
		if c := d.addrs[b].Compare(d.addrs[a]); c != 0 {
			return c
		}
		return a.Compare(b)
	})
	for _, a := range addrs {
		r.Addresses = append(r.Addresses, a.String())
	}
	return r
}

// Clear forgets every device and what it moved, and every destination.
// Counting carries on from where each connection stands.
func (c *Counter) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.counting {
		return
	}
	c.devices, c.pending = map[string]*device{}, map[string]*delta{}
	c.forgetDestinations()
	c.since = c.now()
}

// ClearLinks forgets what every link moved and its errors. Counting
// carries on from the counters as they are.
func (c *Counter) ClearLinks() {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.links {
		l.series = newSeries(now)
		l.errs, l.errMinutes = nil, nil
	}
}

// ClearDestinations forgets every destination and leaves the devices.
func (c *Counter) ClearDestinations() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgetDestinations()
}

// forgetDestinations drops every destination row. A connection still open
// counts once in the row it moves into next, as in a new hour. The caller
// holds c.mu.
func (c *Counter) forgetDestinations() {
	c.open, c.closed = nil, nil
	for _, s := range c.flows {
		s.hour = [2]int64{}
	}
}
