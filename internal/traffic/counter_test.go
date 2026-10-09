package traffic

import (
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/network"
)

// router is a test's router: eth0 on the internet at 203.0.113.2, eth1 a
// LAN at 10.0.0.1/24 and 2001:db8:1::1/64, eth2 a second LAN at
// 10.0.2.1/24, and what the kernel says about them.
type router struct {
	mu    sync.Mutex
	cfg   *model.Config
	now   time.Time
	links []network.Link
	flows []netlink.Flow
	neigh []netlink.Neighbour
	slow  time.Duration
	c     *Counter
}

func newRouter(t *testing.T) *router {
	t.Helper()
	r := &router{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	r.cfg = &model.Config{
		Zones: []model.Zone{{Name: "wan", External: true}, {Name: "lan"}},
		Interfaces: []model.Interface{
			{Name: "eth0", Zone: "wan", Enabled: true},
			{Name: "eth1", Zone: "lan", Enabled: true},
			{Name: "eth2", Zone: "lan", Enabled: true},
		},
		Traffic: model.Traffic{Devices: true},
	}
	r.links = []network.Link{
		{Name: "lo", Index: 1, Kind: "loopback", Addresses: []string{"127.0.0.1/8", "::1/128"}},
		{Name: "eth0", Index: 2, Kind: "ethernet", Addresses: []string{"203.0.113.2/24"}},
		{Name: "eth1", Index: 3, Kind: "ethernet", Addresses: []string{"10.0.0.1/24", "2001:db8:1::1/64", "fe80::1/64"}},
		{Name: "eth2", Index: 4, Kind: "ethernet", Addresses: []string{"10.0.2.1/24"}},
	}
	r.c = &Counter{
		Source: func() *model.Config { return r.cfg },
		Log:    slog.New(slog.DiscardHandler),
		Links: func() ([]network.Link, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return slices.Clone(r.links), nil
		},
		Flows: func(fam int, fn func(netlink.Flow)) error {
			// Reading the table takes slow on the router's clock, which
			// is the one the counter times a dump by.
			r.mu.Lock()
			flows := slices.Clone(r.flows)
			r.now = r.now.Add(r.slow)
			r.mu.Unlock()
			for _, f := range flows {
				if (fam == unix.AF_INET) == (f.Forward.SrcIP.To4() != nil) {
					fn(f)
				}
			}
			return nil
		},
		Neighbours: func() ([]netlink.Neighbour, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return slices.Clone(r.neigh), nil
		},
		Now: func() time.Time { return r.now },
	}
	return r
}

// flow is a connection from src to dst, answered by from, with what went
// each way so far.
func flow(id uint32, src, dst, from string, fwd, rev uint64) netlink.Flow {
	return netlink.Flow{
		ID:      id,
		Forward: netlink.Tuple{SrcIP: net.ParseIP(src), DstIP: net.ParseIP(dst), Bytes: fwd},
		Reverse: netlink.Tuple{SrcIP: net.ParseIP(from), DstIP: net.ParseIP(src), Bytes: rev},
	}
}

// tcp is a TCP connection to a port, answered by the far end.
func tcp(id uint32, src, dst string, port uint16, fwd, rev uint64) netlink.Flow {
	f := flow(id, src, dst, dst, fwd, rev)
	f.Forward.Protocol, f.Reverse.Protocol = 6, 6
	f.Forward.DstPort, f.Reverse.SrcPort = port, port
	return f
}

func neighbour(ip, mac string, link int) netlink.Neighbour {
	hw, _ := net.ParseMAC(mac)
	return netlink.Neighbour{LinkIndex: link, IP: net.ParseIP(ip), HardwareAddr: hw, State: unix.NUD_REACHABLE}
}

// step moves the clock and does what the second's tick would: read the
// links, and the connection table when it is due.
func (r *router) step(d time.Duration, flows ...netlink.Flow) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.flows = flows
	now := r.now
	r.mu.Unlock()
	r.c.sampleLinks(now)
	if r.c.due(now) {
		r.c.dump(now)
	}
}

// totals is what each device moved in the last five minutes.
func (r *router) totals() map[string]Totals {
	out := map[string]Totals{}
	for _, d := range r.c.DeviceReports(Window5m) {
		out[d.ID] = d.Totals
	}
	return out
}

// The first dump after counting starts only notes where each connection
// stands; after it, a connection counts what it moved since the last, a
// new one all of it, an ended one its last bytes once, and one gone
// without an end nothing more.
func TestEachConnectionCountsWhatItMovedSinceTheLastDump(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.neigh = []netlink.Neighbour{neighbour("10.0.0.5", "aa:bb:cc:00:00:05", 3)}
	const dev = "aa:bb:cc:00:00:05"
	r.c.follow(r.now)
	r.step(0, flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 100, 1000))
	if got := r.totals(); len(got) != 0 {
		t.Fatalf("the baseline counted %v", got)
	}

	r.step(5*time.Second,
		flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 300, 5000),
		flow(2, "10.0.0.5", "198.51.100.8", "198.51.100.8", 50, 60))
	if got := r.totals()[dev]; got != (Totals{Up: 250, Down: 4060}) {
		t.Fatalf("second dump = %+v", got)
	}

	// 1 ends with its last bytes, 3 opens and ends between the dumps, 2's
	// counters go back as a reused entry's would, and 4 goes unseen.
	r.c.end(flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 400, 6000))
	r.c.end(flow(3, "10.0.0.5", "198.51.100.9", "198.51.100.9", 7, 9))
	r.step(5*time.Second,
		flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 400, 6000),
		flow(2, "10.0.0.5", "198.51.100.8", "198.51.100.8", 10, 10),
		flow(4, "10.0.0.5", "198.51.100.10", "198.51.100.10", 1, 1))
	if got := r.totals()[dev]; got != (Totals{Up: 250 + 100 + 7 + 10 + 1, Down: 4060 + 1000 + 9 + 10 + 1}) {
		t.Fatalf("third dump = %+v", got)
	}
	// Gone unannounced, 4 is forgotten: were it to come back it would
	// count in full as a new one.
	r.step(5*time.Second, flow(2, "10.0.0.5", "198.51.100.8", "198.51.100.8", 10, 10))
	r.step(5*time.Second,
		flow(2, "10.0.0.5", "198.51.100.8", "198.51.100.8", 10, 10),
		flow(4, "10.0.0.5", "198.51.100.10", "198.51.100.10", 1, 1))
	if got := r.totals()[dev]; got.Up != 250+100+7+10+1+1 {
		t.Errorf("after 4 came back = %+v", got)
	}
	// An end announced before the first dump belongs to the baseline.
	fresh := newRouter(t)
	fresh.c.follow(fresh.now)
	fresh.c.end(flow(9, "10.0.0.5", "198.51.100.7", "198.51.100.7", 5, 5))
	fresh.step(0)
	if got := fresh.totals(); len(got) != 0 {
		t.Errorf("an end before the baseline counted %v", got)
	}
}

// A device is its hardware address, both families under it; one the
// neighbour table does not know is its address. Behind a port forward the
// answering end is the device; between two devices both count; the
// router's own traffic is one row, and what never touches an inside
// address counts for nobody.
func TestConnectionsCountForTheDevicesAtTheirEnds(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.neigh = []netlink.Neighbour{
		neighbour("10.0.0.5", "aa:bb:cc:00:00:05", 3),
		neighbour("2001:db8:1::5", "aa:bb:cc:00:00:05", 3),
		// The ISP's router is a neighbour too, outside.
		neighbour("203.0.113.1", "aa:bb:cc:00:00:01", 2),
	}
	r.c.follow(r.now)
	r.step(0)
	r.step(5*time.Second,
		flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 10, 100),
		flow(2, "2001:db8:1::5", "2001:db8:ffff::7", "2001:db8:ffff::7", 20, 200),
		flow(3, "10.0.0.9", "198.51.100.7", "198.51.100.7", 30, 300),
		// A port forward: in from outside to the WAN address, answered
		// by 10.0.0.9.
		flow(4, "198.51.100.20", "203.0.113.2", "10.0.0.9", 40, 400),
		// One LAN to the other.
		flow(5, "10.0.0.5", "10.0.2.7", "10.0.2.7", 50, 500),
		// The router's own lookup, and a device asking the router.
		flow(6, "203.0.113.2", "9.9.9.9", "9.9.9.9", 60, 600),
		flow(7, "10.0.0.9", "10.0.0.1", "10.0.0.1", 70, 700),
		// The ISP's router asking the router's own address.
		flow(8, "203.0.113.1", "203.0.113.2", "203.0.113.2", 80, 800),
		// Nobody's: outside to outside, and the loopback.
		flow(10, "198.51.100.1", "198.51.100.2", "198.51.100.2", 85, 850),
		flow(9, "127.0.0.1", "127.0.0.53", "127.0.0.53", 90, 900),
	)
	want := map[string]Totals{
		"aa:bb:cc:00:00:05": {Up: 10 + 20 + 50, Down: 100 + 200 + 500},
		"10.0.0.9":          {Up: 30 + 400 + 70, Down: 300 + 40 + 700},
		"10.0.2.7":          {Up: 500, Down: 50},
		RouterID:            {Up: 60 + 700 + 800, Down: 600 + 70 + 80},
	}
	got := r.totals()
	if len(got) != len(want) {
		t.Errorf("rows = %v", got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %+v, want %+v", id, got[id], w)
		}
	}
	d, ok := r.c.DeviceReport("aa:bb:cc:00:00:05", Window5m)
	if !ok || d.MAC != "aa:bb:cc:00:00:05" || d.Interface != "eth1" || len(d.Addresses) != 2 {
		t.Errorf("device = %+v", d)
	}
	if d, _ := r.c.DeviceReport("10.0.2.7", Window5m); d.MAC != "" || d.Interface != "eth2" {
		t.Errorf("device by address = %+v", d)
	}
	if d, _ := r.c.DeviceReport(RouterID, Window5m); !d.Router {
		t.Errorf("router = %+v", d)
	}
}

// A network routed to a tunnel's peer is inside, and its devices count by
// address.
func TestAPeersNetworkIsInside(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.cfg.Zones = append(r.cfg.Zones, model.Zone{Name: "wg0"})
	r.cfg.Interfaces = append(r.cfg.Interfaces, model.Interface{
		Name: "wg0", Zone: "wg0", Enabled: true,
		IPv4:      model.IPv4{Mode: model.AddrStatic, Address: "10.66.0.1/24"},
		WireGuard: &model.WireGuard{Peers: []model.WireGuardPeer{{Name: "site", Enabled: true, AllowedIPs: []string{"10.66.0.2/32", "192.168.99.0/24"}}}},
	})
	r.links = append(r.links, network.Link{Name: "wg0", Index: 5, Kind: "wireguard", Addresses: []string{"10.66.0.1/24"}})
	r.c.follow(r.now)
	r.step(0)
	r.step(5*time.Second, flow(1, "192.168.99.4", "198.51.100.7", "198.51.100.7", 1, 2))
	if d, ok := r.c.DeviceReport("192.168.99.4", Window5m); !ok || d.Interface != "wg0" || d.Totals != (Totals{Up: 1, Down: 2}) {
		t.Errorf("peer's device = %+v, %v", d, ok)
	}
}

// A dump that takes long waits fifty times as long before the next, so
// reading a big table never takes more than 2% of a core.
func TestABigTableStretchesTheInterval(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.c.follow(r.now)
	if r.c.Interval() != DumpEvery {
		t.Errorf("interval = %v", r.c.Interval())
	}
	r.slow = 100 * time.Millisecond
	r.step(0)
	if got := r.c.Interval(); got < 50*2*r.slow {
		t.Fatalf("interval = %v after a dump of two families of %v each", got, r.slow)
	}
	r.slow = 0
	r.step(5 * time.Second)
	if r.c.due(r.now) {
		t.Error("read again after five seconds")
	}
	if st := r.c.Status(); !st.Counting || st.Interval < 10 || st.Error != "" {
		t.Errorf("status = %+v", st)
	}
	// A table it may not read is said, and why.
	r.c.Flows = func(int, func(netlink.Flow)) error { return unix.EPERM }
	r.c.dump(r.now)
	if st := r.c.Status(); !strings.Contains(st.Error, "not running as root") {
		t.Errorf("status = %+v", st)
	}
}

// Links are read every second whatever the configuration says. A counter
// that goes back belongs to a link made again, and starts over.
func TestLinksCountEverySecond(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.cfg.Traffic.Devices = false
	ch, cancel := r.c.Subscribe(8)
	defer cancel()
	set := func(rx, tx uint64) {
		r.mu.Lock()
		r.links[1].RXBytes, r.links[1].TXBytes = rx, tx
		r.mu.Unlock()
	}
	set(1000, 2000)
	r.step(0)
	set(126000, 2000+12500)
	r.step(time.Second)
	<-ch
	ev := <-ch
	if ev.Kind != "links" || len(ev.Links) != 3 {
		t.Fatalf("event = %+v", ev)
	}
	for _, l := range ev.Links {
		if l.ID == "eth0" && (l.Down != 1_000_000 || l.Up != 100_000) {
			t.Errorf("eth0 = %+v", l)
		}
	}
	set(10, 10)
	r.step(time.Second)
	reports := r.c.LinkReports(Window5m)
	if len(reports) != 3 || reports[0].Name != "eth0" {
		t.Fatalf("reports = %+v", reports)
	}
	if eth0 := reports[0]; eth0.Down != 0 || eth0.Totals != (Totals{Down: 125000, Up: 12500}) || len(eth0.Points) != 1 {
		t.Errorf("eth0 after it was made again = %+v", eth0)
	}
	if devices := r.c.DeviceReports(Window5m); len(devices) != 0 {
		t.Errorf("devices counted while off: %+v", devices)
	}
}

// Switched off, what was counted per device goes; on again, it starts
// from a new baseline.
func TestSwitchingOffForgetsTheDevices(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.c.follow(r.now)
	r.step(0)
	r.step(5*time.Second, flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 10, 10))
	if len(r.totals()) != 1 {
		t.Fatal("nothing counted")
	}
	r.cfg.Traffic.Devices = false
	r.c.follow(r.now)
	if len(r.totals()) != 0 || r.c.Status().Counting {
		t.Error("kept what it counted")
	}
	r.cfg.Traffic.Devices = true
	r.c.follow(r.now)
	r.step(5*time.Second, flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 20, 20))
	if len(r.totals()) != 0 {
		t.Error("counted before a new baseline")
	}
	// Clear forgets the devices and keeps counting from where each
	// connection stands.
	r.step(5*time.Second, flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 30, 30))
	r.c.Clear()
	if len(r.totals()) != 0 {
		t.Error("clear kept devices")
	}
	r.step(5*time.Second, flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 31, 30))
	if got := r.totals()["10.0.0.5"]; got != (Totals{Up: 1}) {
		t.Errorf("after clear = %+v", got)
	}
}

// Past the bound the longest idle devices go first.
func TestTheLongestIdleDevicesGoFirst(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.cfg.Interfaces[1].Name = "eth1"
	r.links[2].Addresses = []string{"10.0.0.1/16"}
	r.c.follow(r.now)
	r.step(0)
	var flows []netlink.Flow
	for i := range MaxDevices {
		flows = append(flows, flow(uint32(i+1), fmt.Sprintf("10.0.%d.%d", 10+i/200, 1+i%200), "198.51.100.7", "198.51.100.7", 1, 1))
	}
	r.step(5*time.Second, flows...)
	r.step(5*time.Second, flow(5000, "10.0.200.1", "198.51.100.7", "198.51.100.7", 1, 1))
	all := r.c.DeviceReports(Window5m)
	if len(all) != MaxDevices {
		t.Fatalf("%d devices", len(all))
	}
	if _, ok := r.c.DeviceReport("10.0.200.1", Window5m); !ok {
		t.Error("the newest device went")
	}
}

// The stream carries every device that moved in the last five minutes
// after each dump.
func TestDevicesAreStreamedAfterEachDump(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.c.follow(r.now)
	r.step(0)
	ch, cancel := r.c.Subscribe(16)
	defer cancel()
	r.step(5*time.Second, flow(1, "10.0.0.5", "198.51.100.7", "198.51.100.7", 1000, 5000))
	var got Event
	for ev := range len(ch) {
		_ = ev
		if e := <-ch; e.Kind == "devices" {
			got = e
		}
	}
	if len(got.Devices) != 1 || got.Devices[0].ID != "10.0.0.5" || got.Devices[0].Up != 1600 || got.Interval != 5 {
		t.Errorf("event = %+v", got)
	}
}

// A minute and an hour hold what moved in them, a day of minutes and a
// month of hours are kept, and a window's points are dense from its start
// or the series', whichever is later.
func TestBucketsRollOverAndAgeOut(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("UTC+5:30", 5*3600+1800)
	start := time.Date(2026, 9, 1, 0, 0, 30, 0, time.UTC)
	s := newSeries(start)
	s.add(start, 30*time.Second, 600, 60, loc)
	s.add(start.Add(40*time.Second), 40*time.Second, 1200, 0, loc)
	s.add(start.Add(40*time.Second+time.Hour), time.Minute, 0, 0, loc)
	if len(s.minutes) != 2 || s.minutes[0].down != 600 || s.minutes[1].down != 1200 {
		t.Fatalf("minutes = %+v", s.minutes)
	}
	// The hour starts at the zone's half past.
	if len(s.hours) != 1 || s.hours[0].start != time.Date(2026, 9, 1, 5, 0, 0, 0, loc).Unix() {
		t.Fatalf("hours = %+v", s.hours)
	}
	now := start.Add(2 * time.Minute)
	pts, tot := s.read(Window24h, now, loc)
	if len(pts) != 3 || pts[1][1] != 1200*8/60.0 || tot != (Totals{Down: 1800, Up: 60}) {
		t.Errorf("a day = %v, %+v", pts, tot)
	}
	// Later on, the minutes are gone and the hours stay a month.
	later := start.Add(25 * time.Hour)
	s.add(later, time.Minute, 1, 1, loc)
	if len(s.minutes) != 1 || len(s.hours) != 2 {
		t.Errorf("after a day: %d minutes, %d hours", len(s.minutes), len(s.hours))
	}
	pts, tot = s.read(Window30d, later, loc)
	if tot != (Totals{Down: 1801, Up: 61}) || len(pts) != 26 {
		t.Errorf("a month = %d points, %+v", len(pts), tot)
	}
	s.add(start.Add(33*24*time.Hour), time.Minute, 1, 1, loc)
	if len(s.hours) != 1 {
		t.Errorf("hours after a month = %+v", s.hours)
	}
	// Five minutes of samples, as they were taken.
	if pts, _ := s.read(Window5m, start.Add(33*24*time.Hour), loc); len(pts) != 1 {
		t.Errorf("five minutes = %v", pts)
	}
}

// setErrors sets what the kernel says eth0 counted as errors.
func (r *router) setErrors(rx, tx uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.links[1].RXErrors, r.links[1].TXErrors = rx, tx
}

// Errors the kernel counted before the first read are not counted: only
// those found after it are.
func TestErrorsBeforeTheFirstReadAreNotCounted(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.setErrors(147, 0)
	r.step(0)
	if got := r.c.LinkErrors(); len(got) != 0 {
		t.Fatalf("errors from before the first read = %+v", got)
	}
	r.setErrors(150, 2)
	r.step(time.Second)
	if got := r.c.LinkErrors(); len(got) != 1 || got["eth0"] != (Errors{RX: 3, TX: 2}) {
		t.Errorf("errors = %+v", got)
	}
}

// A link's errors show for 72 hours, to the hour, then go.
func TestErrorsGoAfter72Hours(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.step(0)
	r.setErrors(0, 1)
	r.step(time.Second)
	r.step(ErrorsKept)
	if got := r.c.LinkErrors(); got["eth0"] != (Errors{TX: 1}) {
		t.Errorf("72 hours on = %+v", got)
	}
	r.step(time.Hour)
	if got := r.c.LinkErrors(); len(got) != 0 {
		t.Errorf("73 hours on = %+v", got)
	}
}

// A counter that went back belongs to a link made again, which starts
// over without counting; so does a link that went away and came back.
func TestALinkMadeAgainStartsItsErrorsOver(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.setErrors(5, 0)
	r.step(0)
	r.setErrors(0, 0)
	r.step(time.Second)
	r.setErrors(2, 0)
	r.step(time.Second)
	if got := r.c.LinkErrors(); got["eth0"] != (Errors{RX: 2}) {
		t.Fatalf("after the link was made again = %+v", got)
	}
	r.mu.Lock()
	gone := r.links[1]
	r.links = slices.Delete(slices.Clone(r.links), 1, 2)
	r.mu.Unlock()
	r.step(time.Second)
	gone.RXErrors = 9
	r.mu.Lock()
	r.links = slices.Insert(r.links, 1, gone)
	r.mu.Unlock()
	r.step(time.Second)
	if got := r.c.LinkErrors(); got["eth0"] != (Errors{RX: 2}) {
		t.Errorf("after the link came back = %+v", got)
	}
}

// Clearing traffic per link forgets what each link moved and its errors,
// and counting goes on from where each counter stands.
func TestClearingTheLinksKeepsCounting(t *testing.T) {
	t.Parallel()
	r := newRouter(t)
	r.c.follow(r.now)
	r.step(0)
	r.links[1].RXBytes, r.links[1].RXErrors = 1000, 2
	r.step(time.Second)
	if e := r.c.LinkErrors(); e["eth0"].RX != 2 {
		t.Fatalf("errors = %+v", e)
	}
	r.c.ClearLinks()
	if e := r.c.LinkErrors(); len(e) != 0 {
		t.Errorf("errors after the clear = %+v", e)
	}
	for _, l := range r.c.LinkReports(Window24h) {
		if l.Totals != (Totals{}) {
			t.Errorf("%s after the clear = %+v", l.Name, l)
		}
	}
	r.links[1].RXBytes = 1500
	r.step(time.Second)
	for _, l := range r.c.LinkReports(Window5m) {
		if l.Name == "eth0" && l.Totals.Down != 500 {
			t.Errorf("after the clear eth0 moved %+v, want 500 down", l.Totals)
		}
	}
}
