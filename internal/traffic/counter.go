// Package traffic counts what crosses the router: every link from the
// kernel's counters each second, and while the configuration says so every
// device, from the bytes each tracked connection carried. Nothing is
// captured and nothing leaves the router.
package traffic

import (
	"context"
	"log/slog"
	"net/netip"
	"slices"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/network"
)

const (
	// followEvery is how often the configuration is read again.
	followEvery = 5 * time.Second
	// DumpEvery is how often the connection table is read, unless reading
	// it takes long enough to stretch that.
	DumpEvery = 5 * time.Second
	// stretch is how many times the last dump's length the next one
	// waits, so dumping takes at most 2% of a core however big the table.
	stretch = 50
	// MaxDevices bounds the devices kept; past it the longest idle goes.
	MaxDevices = 1000
	// macKept is how long an address is tied to the hardware address the
	// neighbour table last gave it, after the entry itself has gone.
	macKept = 24 * time.Hour
	// RouterID names the router's own row.
	RouterID = "router"
	// ErrorsKept is how long a link's errors are shown.
	ErrorsKept = 72 * time.Hour
)

// FlowEnds is what the kernel announces connections' ends through.
type FlowEnds interface {
	Read(ctx context.Context, fn func(netlink.Flow), warn func(error)) error
	Close() error
}

// Counter counts what crosses the router. The kernel is reached through
// the functions below, which tests replace.
type Counter struct {
	// Source is the configuration the router runs.
	Source func() *model.Config
	Log    *slog.Logger
	// Links reads the links and their counters; nil is network.Discover.
	Links func() ([]network.Link, error)
	// Flows reads one family's connection table; nil is netlink.Flows.
	Flows func(fam int, fn func(netlink.Flow)) error
	// Ends opens the announcements of connections' ends; nil joins the
	// kernel's.
	Ends func() (FlowEnds, error)
	// Neighbours reads the ARP and NDP tables; nil asks the kernel.
	Neighbours func() ([]netlink.Neighbour, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu    sync.Mutex
	loc   *time.Location
	links map[string]*link
	// seen is the last read of the links, which the next dump works out
	// the router's addresses and the inside networks from.
	seen []network.Link

	followed time.Time
	counting bool
	since    time.Time
	baseline bool
	interval time.Duration
	next     time.Time
	last     time.Time
	gen      uint32
	flows    map[uint32]*flowState
	ended    map[uint32]bool
	pending  map[string]*delta
	devices  map[string]*device
	view     view
	macs     map[netip.Addr]macSeen

	// names is what the router's DNS answered, while destinations are
	// recorded; open holds this hour's destination rows and closed the
	// hours before, oldest first.
	names    Names
	destOn   bool
	destSize int
	destKeep time.Duration
	open     map[destKey]*destRow
	openHour int64
	closed   []destRow

	// What waits to be written to the files: closed minutes of the links
	// and the devices, and closed hours of the destinations.
	linkRecs, deviceRecs records[MinuteLine]
	destRecs             records[HourLine]

	subs map[chan Event]struct{}
	// said is the failure logged for each thing that failed, so one that
	// repeats every dump is logged once.
	said map[string]string
	// failed is why the last dump could not read the table.
	failed string
}

// link is one link's counters and what they moved.
type link struct {
	rx, tx   uint64
	at       time.Time
	ok       bool
	down, up float64
	series   *series
	// rxErr and txErr are the kernel's error counters at the last read.
	// errs holds the errors counted since, an hour a bucket for
	// ErrorsKept, received as down and sent as up; errMinutes holds those
	// of the minutes not yet handed to the files.
	rxErr, txErr uint64
	errs         []bucket
	errMinutes   []bucket
}

// countErrors counts the errors the kernel found since the last read:
// into their hour, which the pages read, and their minute, which the
// files get. The minute gets a bucket in the series even when nothing
// moved, so closeMinutes writes its line.
func (l *link) countErrors(now time.Time, rx, tx uint64) {
	if rx == 0 && tx == 0 {
		return
	}
	l.errs = addBucket(l.errs, now.Truncate(time.Hour).Unix(), rx, tx, errorsCut(now))
	minute := now.Truncate(time.Minute).Unix()
	l.errMinutes = addBucket(l.errMinutes, minute, rx, tx, 0)
	l.series.touch(minute)
}

// errorsCut is the start of the oldest hour whose errors still show.
func errorsCut(now time.Time) int64 { return now.Add(-ErrorsKept).Truncate(time.Hour).Unix() }

// flowState is a connection's counters at the last dump, and the dump
// that last saw it. dest is the destination row each end that is a
// device counts into, fixed when it is first counted, and hour the hour
// it was last counted in.
type flowState struct {
	fwd, rev uint64
	gen      uint32
	dest     [2]*destKey
	hour     [2]int64
}

// delta is what a device moved since the last dump.
type delta struct {
	down, up uint64
	addrs    []netip.Addr
	mac      string
	link     string
}

// device is one row of the Devices tab.
type device struct {
	id       string
	mac      string
	router   bool
	addrs    map[netip.Addr]time.Time
	link     string
	last     time.Time
	down, up float64
	series   *series
}

// macSeen is the hardware address an address last had, and on which link.
type macSeen struct {
	mac  string
	link string
	at   time.Time
}

// view is what a dump attributes connections with: the router's own
// addresses and the networks that are inside.
type view struct {
	router map[netip.Addr]bool
	inside []insideNet
}

type insideNet struct {
	prefix netip.Prefix
	link   string
}

func (c *Counter) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Counter) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// ends is the reader of connections' ends while devices are counted.
type ends struct{ stop context.CancelFunc }

func (e *ends) start(ctx context.Context, c *Counter) {
	if e.stop != nil {
		return
	}
	ectx, cancel := context.WithCancel(ctx)
	e.stop = cancel
	go c.readEnds(ectx)
}

func (e *ends) end() {
	if e.stop != nil {
		e.stop()
		e.stop = nil
	}
}

// Run counts until ctx is done.
func (c *Counter) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var reader ends
	defer reader.end()
	for {
		if c.Tick(c.now()) {
			reader.start(ctx, c)
		} else {
			reader.end()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Tick does a second's work: the configuration every five seconds, the
// links, and the connection table when it is due. It reports whether
// devices are counted.
func (c *Counter) Tick(now time.Time) bool {
	c.mu.Lock()
	follow := now.Sub(c.followed) >= followEvery || now.Before(c.followed)
	on := c.counting
	c.mu.Unlock()
	if follow {
		on = c.follow(now)
		c.mu.Lock()
		c.followed = now
		c.mu.Unlock()
	}
	c.sampleLinks(now)
	if c.due(now) {
		c.dump(now)
	}
	c.mu.Lock()
	c.closeMinutes(now)
	c.mu.Unlock()
	return on
}

// follow reads the configuration again, starting or stopping the count
// per device, and reports whether it runs. Switched off, what it counted
// goes.
func (c *Counter) follow(now time.Time) bool {
	cfg := c.Source()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loc = time.UTC
	if cfg != nil {
		c.loc = cfg.System.Location()
	}
	on := cfg != nil && cfg.Traffic.Devices
	switch {
	case on && !c.counting:
		c.counting, c.since, c.baseline = true, now, true
		c.interval, c.next, c.last = DumpEvery, now, now
		c.flows, c.ended, c.pending = map[uint32]*flowState{}, map[uint32]bool{}, map[string]*delta{}
		c.devices, c.macs = map[string]*device{}, map[netip.Addr]macSeen{}
	case !on && c.counting:
		c.counting = false
		c.flows, c.ended, c.pending, c.devices, c.macs = nil, nil, nil, nil, nil
	}
	dest := on && cfg.Traffic.DestinationsOn()
	if dest {
		c.destSize = cfg.Traffic.Destinations.Size()
		c.destKeep = cfg.System.Logging.MemoryKeep()
	}
	if !dest {
		c.open, c.closed = nil, nil
	}
	c.destOn = dest
	c.names.set(dest)
	return on
}

// readEnds follows the ends of connections while ctx lasts, so the last
// bytes of one that ends between two dumps are counted.
func (c *Counter) readEnds(ctx context.Context) {
	open := c.Ends
	if open == nil {
		open = func() (FlowEnds, error) { return netlink.OpenFlowEnds() }
	}
	for ctx.Err() == nil {
		r, err := open()
		c.once(err, "could not follow the ends of connections; one that ends between two reads of the table loses its last bytes")
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
			continue
		}
		err = r.Read(ctx, c.end, func(err error) {
			c.log().Debug("connection ends", "err", err)
		})
		_ = r.Close()
		if err != nil {
			c.log().Warn("stopped following the ends of connections", "err", err)
		}
	}
}

// once logs a failure the first time it is seen, and nothing while it
// repeats.
func (c *Counter) once(err error, what string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.said == nil {
		c.said = map[string]string{}
	}
	if err == nil {
		delete(c.said, what)
		return
	}
	if c.said[what] != err.Error() {
		c.said[what] = err.Error()
		c.log().Warn(what, "err", err)
	}
}

// sampleLinks reads every link's counters and counts what moved since the
// last read. A counter that went back belongs to a link made again, and
// starts over.
func (c *Counter) sampleLinks(now time.Time) {
	read := c.Links
	if read == nil {
		read = network.Discover
	}
	links, err := read()
	if err != nil {
		c.log().Debug("could not read the links", "err", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.links == nil {
		c.links = map[string]*link{}
	}
	c.seen = links
	ev := Event{Kind: "links", Time: now, Links: []Rate{}}
	present := map[string]bool{}
	for _, l := range links {
		if l.Kind == "loopback" {
			continue
		}
		present[l.Name] = true
		s := c.links[l.Name]
		if s == nil {
			s = &link{series: newSeries(now)}
			c.links[l.Name] = s
		}
		if s.ok && l.RXBytes >= s.rx && l.TXBytes >= s.tx && now.After(s.at) {
			dur := now.Sub(s.at)
			rx, tx := l.RXBytes-s.rx, l.TXBytes-s.tx
			s.series.add(now, dur, rx, tx, c.loc)
			s.down, s.up = bits(rx, dur.Seconds()), bits(tx, dur.Seconds())
		} else {
			s.down, s.up = 0, 0
		}
		if s.ok && l.RXErrors >= s.rxErr && l.TXErrors >= s.txErr {
			s.countErrors(now, l.RXErrors-s.rxErr, l.TXErrors-s.txErr)
		}
		s.rxErr, s.txErr = l.RXErrors, l.TXErrors
		s.rx, s.tx, s.at, s.ok = l.RXBytes, l.TXBytes, now, true
		ev.Links = append(ev.Links, Rate{ID: l.Name, Down: s.down, Up: s.up})
	}
	// A link that went away keeps its history but no longer has a rate.
	for name, s := range c.links {
		if !present[name] {
			s.ok, s.down, s.up = false, 0, 0
		}
	}
	c.publish(ev)
}

// due reports whether the table is to be read now.
func (c *Counter) due(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counting && !now.Before(c.next)
}

// dump reads the connection table, counts what each connection carried
// since the last read, and hands it to the devices at either end. The
// first read after counting is switched on only notes where each
// connection stood.
func (c *Counter) dump(now time.Time) {
	cfg := c.Source()
	c.mu.Lock()
	c.gen++
	gen := c.gen
	c.view = newView(cfg, c.seen)
	c.mu.Unlock()
	c.readNeighbours(now)

	read := c.Flows
	if read == nil {
		read = netlink.Flows
	}
	started := c.now()
	var failed error
	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		if err := read(fam, func(f netlink.Flow) { c.seenFlow(f, gen) }); err != nil {
			failed = err
		}
	}
	c.once(failed, "could not read the connection table, so devices are not counted")
	c.mu.Lock()
	c.failed = ""
	if failed != nil {
		c.failed = failed.Error()
	}
	c.mu.Unlock()
	took := c.now().Sub(started)

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.counting {
		return
	}
	// Gone without an end announced: its bytes since the last dump are
	// lost, and there is nothing to add them to.
	for id, f := range c.flows {
		if f.gen != gen {
			delete(c.flows, id)
		}
	}
	// An end counted between two dumps is not counted again by the next.
	clear(c.ended)
	c.baseline = false
	c.interval = max(DumpEvery, stretch*took)
	c.next = now.Add(c.interval)
	c.settle(now)
}

// Interval is how often the connection table is read now: every five
// seconds, or longer once reading it takes long enough.
func (c *Counter) Interval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interval
}

// seenFlow counts what one connection carried since the last dump.
func (c *Counter) seenFlow(f netlink.Flow, gen uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.counting || c.ended[f.ID] {
		return
	}
	s := c.flows[f.ID]
	var fwd, rev uint64
	if s == nil {
		s = &flowState{}
		c.flows[f.ID] = s
		if !c.baseline {
			// New since the last dump: all of it is new.
			fwd, rev = f.Forward.Bytes, f.Reverse.Bytes
		}
	} else {
		fwd, rev = since(f.Forward.Bytes, s.fwd), since(f.Reverse.Bytes, s.rev)
	}
	s.fwd, s.rev, s.gen = f.Forward.Bytes, f.Reverse.Bytes, gen
	c.attribute(f, s, fwd, rev)
}

// end counts the last bytes of a connection the kernel stopped tracking.
func (c *Counter) end(f netlink.Flow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.counting || c.baseline {
		return
	}
	var fwd, rev uint64
	s := c.flows[f.ID]
	if s != nil {
		fwd, rev = since(f.Forward.Bytes, s.fwd), since(f.Reverse.Bytes, s.rev)
		delete(c.flows, f.ID)
	} else {
		// Opened and closed between two dumps.
		fwd, rev = f.Forward.Bytes, f.Reverse.Bytes
	}
	c.ended[f.ID] = true
	c.attribute(f, s, fwd, rev)
}

// since is how far a counter moved, or all of it when it went back.
func since(now, then uint64) uint64 {
	if now < then {
		return now
	}
	return now - then
}

// attribute hands a connection's bytes to the devices at its ends: the
// one that opened it, and the one that answered, which behind a port
// forward is the device and not the router. What the opener sent is its
// up and the answerer's down. While destinations are recorded, each
// device's row for the other end takes them as well. s is the
// connection's state, nil for one that opened and closed between two
// dumps. The caller holds c.mu.
func (c *Counter) attribute(f netlink.Flow, s *flowState, fwd, rev uint64) {
	if fwd == 0 && rev == 0 {
		return
	}
	a, _ := netip.AddrFromSlice(f.Forward.SrcIP)
	b, _ := netip.AddrFromSlice(f.Reverse.SrcIP)
	a, b = a.Unmap(), b.Unmap()
	if a.IsLoopback() || b.IsLoopback() {
		return
	}
	ka, kb := c.key(a), c.key(b)
	if ka != "" && ka == kb {
		// Both ends are the router, or one device talking to itself.
		return
	}
	if ka != "" {
		c.count(ka, a, fwd, rev)
	}
	if kb != "" {
		c.count(kb, b, rev, fwd)
	}
	if !c.destOn {
		return
	}
	now := c.now()
	proto := f.Forward.Protocol
	if ka != "" {
		dst, _ := netip.AddrFromSlice(f.Forward.DstIP)
		c.toDest(s, 0, ka, a, dst.Unmap(), proto, f.Forward.DstPort, fwd, rev, now)
	}
	if kb != "" {
		c.toDest(s, 1, kb, b, a, proto, f.Reverse.SrcPort, rev, fwd, now)
	}
}

// Names is what the DNS listener tells the addresses each client was
// given, for the destinations.
func (c *Counter) Names() *Names { return &c.names }

// key is the row an address counts in: the router's own, a device by its
// hardware address, one by its address where the neighbour table has none,
// or none for an address outside. The caller holds c.mu.
func (c *Counter) key(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	if c.view.router[addr] {
		return RouterID
	}
	if _, ok := c.view.insideOf(addr); !ok {
		return ""
	}
	if m, ok := c.macs[addr]; ok {
		return m.mac
	}
	return addr.String()
}

// count adds what a device sent and received. The caller holds c.mu.
func (c *Counter) count(key string, addr netip.Addr, up, down uint64) {
	d := c.pending[key]
	if d == nil {
		d = &delta{}
		c.pending[key] = d
	}
	d.up += up
	d.down += down
	if !slices.Contains(d.addrs, addr) {
		d.addrs = append(d.addrs, addr)
	}
	if m, ok := c.macs[addr]; ok && key == m.mac {
		d.mac, d.link = m.mac, m.link
	} else if n, ok := c.view.insideOf(addr); ok {
		d.link = n.link
	}
}

// settle turns what each device moved since the last dump into a sample,
// and keeps the devices to their bound. The caller holds c.mu.
func (c *Counter) settle(now time.Time) {
	dur := now.Sub(c.last)
	if dur <= 0 {
		dur = c.interval
	}
	c.last = now
	ev := Event{Kind: "devices", Time: now, Interval: c.interval.Seconds(), Devices: []Rate{}}
	for key, d := range c.pending {
		dev := c.devices[key]
		if dev == nil {
			dev = &device{id: key, router: key == RouterID, addrs: map[netip.Addr]time.Time{}, series: newSeries(now)}
			c.devices[key] = dev
		}
		if d.mac != "" {
			dev.mac = d.mac
		}
		if d.link != "" {
			dev.link = d.link
		}
		for _, a := range d.addrs {
			dev.addrs[a] = now
		}
		dev.last = now
		dev.series.add(now, dur, d.down, d.up, c.loc)
		dev.down, dev.up = bits(d.down, dur.Seconds()), bits(d.up, dur.Seconds())
	}
	for key, dev := range c.devices {
		if _, moved := c.pending[key]; !moved {
			dev.down, dev.up = 0, 0
			// A device that moved something in the last five minutes
			// draws its quiet as a zero; one asleep for longer adds
			// nothing.
			if now.Sub(dev.last) <= fineKept {
				dev.series.add(now, dur, 0, 0, c.loc)
			}
		}
		for a, at := range dev.addrs {
			if now.Sub(at) > hourKept {
				delete(dev.addrs, a)
			}
		}
		if now.Sub(dev.last) <= fineKept {
			ev.Devices = append(ev.Devices, Rate{ID: key, Down: dev.down, Up: dev.up})
		}
		if now.Sub(dev.last) > hourKept {
			delete(c.devices, key)
		}
	}
	clear(c.pending)
	c.bound()
	c.publish(ev)
}

// bound lets go of the longest idle devices past MaxDevices. The caller
// holds c.mu.
func (c *Counter) bound() {
	if len(c.devices) <= MaxDevices {
		return
	}
	all := make([]*device, 0, len(c.devices))
	for _, d := range c.devices {
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].last.Before(all[j].last) })
	for _, d := range all[:len(all)-MaxDevices] {
		delete(c.devices, d.id)
	}
}

// readNeighbours ties addresses to hardware addresses as the neighbour
// table has them now, keeping what it had a while after an entry goes.
func (c *Counter) readNeighbours(now time.Time) {
	read := c.Neighbours
	if read == nil {
		read = func() ([]netlink.Neighbour, error) { return netlink.Neighbours(unix.AF_UNSPEC) }
	}
	list, err := read()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.macs == nil {
		return
	}
	if err == nil {
		byIndex := map[int]string{}
		for _, l := range c.seen {
			byIndex[l.Index] = l.Name
		}
		for _, n := range list {
			addr, ok := netip.AddrFromSlice(n.IP)
			if !ok || len(n.HardwareAddr) != 6 || n.State&(unix.NUD_FAILED|unix.NUD_INCOMPLETE|unix.NUD_NOARP) != 0 {
				continue
			}
			c.macs[addr.Unmap()] = macSeen{mac: n.HardwareAddr.String(), link: byIndex[n.LinkIndex], at: now}
		}
	}
	for a, m := range c.macs {
		if now.Sub(m.at) > macKept {
			delete(c.macs, a)
		}
	}
}

// newView works out the router's addresses and the inside networks: those
// of every interface whose zone is not external, and the networks routed
// to a tunnel's peers.
func newView(cfg *model.Config, links []network.Link) view {
	v := view{router: map[netip.Addr]bool{}}
	outside := map[string]bool{}
	if cfg != nil {
		for _, in := range cfg.Interfaces {
			z, ok := cfg.Zone(in.Zone)
			outside[in.Name] = !ok || z.External
			for _, r := range in.TunnelRoutes() {
				// A network shown under another prefix is known here by that.
				if !r.Mapped {
					v.inside = append(v.inside, insideNet{prefix: r.Prefix, link: in.Name})
				}
			}
			if in.WireGuard != nil {
				for _, p := range in.WireGuard.Peers {
					for _, m := range model.ParseNetMaps(p.Theirs) {
						if p.Enabled {
							v.inside = append(v.inside, insideNet{prefix: m.Shown, link: in.Name})
						}
					}
				}
			}
		}
	}
	for _, l := range links {
		for _, a := range l.Addresses {
			p, err := netip.ParsePrefix(a)
			if err != nil {
				continue
			}
			v.router[p.Addr().Unmap()] = true
			if cfg == nil || l.Kind == "loopback" || p.Addr().IsLinkLocalUnicast() {
				continue
			}
			if isOut, configured := outside[l.Name]; configured && !isOut {
				v.inside = append(v.inside, insideNet{prefix: p.Masked(), link: l.Name})
			}
		}
	}
	// The most specific network wins, so a peer's network inside a LAN's
	// is the peer's.
	sort.SliceStable(v.inside, func(i, j int) bool { return v.inside[i].prefix.Bits() > v.inside[j].prefix.Bits() })
	return v
}

// insideOf is the inside network holding addr.
func (v view) insideOf(addr netip.Addr) (insideNet, bool) {
	for _, n := range v.inside {
		if n.prefix.Contains(addr) {
			return n, true
		}
	}
	return insideNet{}, false
}
