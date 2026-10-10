package discovery

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"sync"
	"time"

	"ostiole/internal/logring"
	"ostiole/internal/panics"
)

const tickEvery = 5 * time.Second

// Link is an interface in the relay with its roles.
type Link struct {
	Name    string
	Asks    bool
	Answers bool
}

// Config is what the relay does.
type Config struct {
	MDNS, SSDP bool
	Links      []Link
	Services   []string
	ReplyPorts [2]int // inclusive; first two ports are the legacy reply sockets (v4, v6)
}

// Event is one packet the relay saw, relayed or dropped.
type Event struct {
	logring.Stamp
	Protocol string   `json:"protocol"`
	Kind     Kind     `json:"kind"`
	From     string   `json:"from"`
	To       []string `json:"to,omitempty"`
	Name     string   `json:"name,omitempty"`
	Source   string   `json:"source"`
	Dropped  string   `json:"dropped,omitempty"`
}

// Sink takes the relay's events.
type Sink interface{ Add(Event) }

// Status is the relay's state for the status page.
type Status struct {
	Running    bool     `json:"running"`
	Interfaces []string `json:"interfaces,omitempty"`
	Problem    string   `json:"problem,omitempty"`
	Overrun    []string `json:"overrun,omitempty"`
}

// Relay relays mDNS and SSDP between interfaces by role.
type Relay struct {
	// Now is the clock; tests replace it.
	Now func() time.Time

	log    *slog.Logger
	events Sink
	seen   *Seen
	wg     sync.WaitGroup

	mu      sync.Mutex
	want    Config
	wanted  bool
	running bool
	sess    *session
	overrun map[string]time.Time
}

// New makes a relay; events may be nil.
func New(log *slog.Logger, events Sink) *Relay {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Relay{Now: time.Now, log: log, events: events, overrun: map[string]time.Time{}}
	r.seen = NewSeen(func() time.Time { return r.Now() })
	return r
}

// rlink is a relay link resolved to an interface.
type rlink struct {
	Link
	index    int
	ifi      net.Interface
	prefixes []netip.Prefix
}

func (l *rlink) has(v6 bool) bool {
	return slices.ContainsFunc(l.prefixes, func(p netip.Prefix) bool { return p.Addr().Is6() == v6 })
}

func (l *rlink) onLink(a netip.Addr) bool {
	if a.IsLinkLocalUnicast() {
		return true
	}
	return slices.ContainsFunc(l.prefixes, func(p netip.Prefix) bool { return p.Contains(a) })
}

type joinKey struct {
	group netip.Addr
	link  string
}

type quietKey struct{ link, reason string }

// session is one run of sockets for one Config.
type session struct {
	cfg     Config
	filter  Filter
	links   []*rlink
	byIndex map[int]*rlink
	byName  map[string]*rlink
	names   map[int]string
	own     map[netip.Addr]bool
	missing map[string]bool
	joined  map[joinKey]int

	conns          []conn
	m4, m6         conn
	l4, l6         conn
	g4, b4, send   conn
	slots4, slots6 slotTable
	proxies        *proxyTable
	dedupe         dedupe
	buckets        map[string]*bucket
	quiet          map[quietKey]time.Time

	bindProblem, joinProblem string
	closed                   bool
}

func active(c Config) bool {
	if !c.MDNS && !c.SSDP {
		return false
	}
	return slices.ContainsFunc(c.Links, func(l Link) bool { return l.Asks }) &&
		slices.ContainsFunc(c.Links, func(l Link) bool { return l.Answers })
}

// Run relays until ctx ends.
func (r *Relay) Run(ctx context.Context) {
	r.mu.Lock()
	r.running = true
	r.startLocked()
	r.mu.Unlock()
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.mu.Lock()
			r.running = false
			r.stopLocked()
			r.mu.Unlock()
			r.wg.Wait()
			return
		case <-t.C:
			r.tick()
		}
	}
}

// Configure sets what the relay does; an equal Config is a no-op.
func (r *Relay) Configure(c Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wanted && reflect.DeepEqual(r.want, c) {
		return
	}
	r.want, r.wanted = c, true
	if r.running {
		r.stopLocked()
		r.startLocked()
	}
}

// Status reports whether the relay runs, on which interfaces and what went wrong.
func (r *Relay) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	var st Status
	now := r.Now()
	for name, at := range r.overrun {
		if now.Sub(at) < overrunFor {
			st.Overrun = append(st.Overrun, name)
		}
	}
	slices.Sort(st.Overrun)
	s := r.sess
	if s == nil {
		return st
	}
	links := make([]Link, 0, len(s.links))
	for _, l := range s.links {
		st.Interfaces = append(st.Interfaces, l.Name)
		links = append(links, l.Link)
	}
	st.Problem = s.bindProblem
	if st.Problem == "" {
		st.Problem = s.joinProblem
	}
	open := (s.cfg.MDNS && (s.m4 != nil || s.m6 != nil)) || (s.cfg.SSDP && (s.g4 != nil || s.b4 != nil))
	st.Running = open && active(Config{MDNS: true, Links: links})
	return st
}

// Announcements lists what devices announced.
func (r *Relay) Announcements() []Announcement { return r.seen.List() }

func (r *Relay) startLocked() {
	if !r.wanted || !active(r.want) {
		return
	}
	c := r.want
	s := &session{
		cfg:     c,
		filter:  NewFilter(c.Services),
		missing: map[string]bool{},
		joined:  map[joinKey]int{},
		dedupe:  dedupe{},
		buckets: map[string]*bucket{},
		quiet:   map[quietKey]time.Time{},
		proxies: newProxyTable(0, -1),
	}
	open := func(c conn, err error) conn {
		if err != nil {
			s.bindProblem = err.Error()
			r.log.Warn("discovery socket", "err", err)
			return nil
		}
		s.conns = append(s.conns, c)
		return c
	}
	open4 := func(address string, o sockOpts, hops, mcastHops int) conn {
		c, err := listen4(address, o, hops, mcastHops)
		if err != nil {
			return open(nil, err)
		}
		return open(c, nil)
	}
	open6 := func(address string) conn {
		c, err := listen6(address, 255)
		if err != nil {
			return open(nil, err)
		}
		return open(c, nil)
	}
	if c.MDNS {
		s.m4 = open4(fmt.Sprintf("0.0.0.0:%d", mdnsPort), sockOpts{}, 255, 255)
		s.m6 = open6(fmt.Sprintf("[::]:%d", mdnsPort))
		first := c.ReplyPorts[0]
		s.l4 = open4(fmt.Sprintf("0.0.0.0:%d", first), sockOpts{}, 255, 255)
		second := 0
		if first > 0 {
			second = first + 1
		}
		s.l6 = open6(fmt.Sprintf("[::]:%d", second))
	}
	if c.SSDP {
		s.g4 = open4(netip.AddrPortFrom(ssdpGroup, ssdpPort).String(), sockOpts{}, 2, 2)
		s.b4 = open4(netip.AddrPortFrom(broadcast4, ssdpPort).String(), sockOpts{broadcast: true}, 2, 2)
		s.send = open4("0.0.0.0:0", sockOpts{broadcast: true}, 2, 2)
		if c.ReplyPorts[0] > 0 {
			s.proxies = newProxyTable(c.ReplyPorts[0]+2, c.ReplyPorts[1])
		}
	}
	r.sess = s
	r.refreshLocked(s)
	read := func(c conn, handle func(*session, bool, []byte, int, netip.AddrPort, netip.Addr), v6 bool) {
		if c == nil {
			return
		}
		r.wg.Go(func() { r.read(s, c, v6, handle) })
	}
	read(s.m4, r.mdnsPacket, false)
	read(s.m6, r.mdnsPacket, true)
	read(s.l4, r.legacyReply, false)
	read(s.l6, r.legacyReply, true)
	read(s.g4, r.ssdpPacket, false)
	read(s.b4, r.ssdpPacket, true)
}

func (r *Relay) stopLocked() {
	s := r.sess
	if s == nil {
		return
	}
	s.closed = true
	for _, c := range s.conns {
		_ = c.Close()
	}
	for _, p := range s.proxies.byClient {
		_ = p.conn.Close()
	}
	r.sess = nil
}

// read hands each datagram on c to handle until c closes; the flag is v6 for mDNS and broadcast for SSDP.
func (r *Relay) read(s *session, c conn, flag bool, handle func(*session, bool, []byte, int, netip.AddrPort, netip.Addr)) {
	b := make([]byte, maxDatagram)
	for {
		n, ifindex, src, dst, err := c.read(b)
		if err != nil {
			r.mu.Lock()
			closed := s.closed
			r.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			r.log.Debug("discovery read", "err", err)
			continue
		}
		handle(s, flag, b[:n], ifindex, src, dst)
	}
}

func (r *Relay) tick() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.Now()
	for name, at := range r.overrun {
		if now.Sub(at) >= overrunFor {
			delete(r.overrun, name)
		}
	}
	s := r.sess
	if s == nil {
		return
	}
	r.refreshLocked(s)
	s.dedupe.sweep(now)
	s.slots4.sweep(now)
	s.slots6.sweep(now)
	r.closeExpiredLocked(s, now)
	for k, at := range s.quiet {
		if now.Sub(at) >= time.Second {
			delete(s.quiet, k)
		}
	}
}

func (r *Relay) closeExpiredLocked(s *session, now time.Time) {
	for _, p := range s.proxies.expired(now) {
		_ = p.conn.Close()
	}
}

// refreshLocked resolves the links by name and reads the router's addresses, joining groups on new links.
func (r *Relay) refreshLocked(s *session) {
	ifs, err := net.Interfaces()
	if err != nil {
		r.log.Debug("discovery interfaces", "err", err)
		return
	}
	s.own = map[netip.Addr]bool{}
	s.names = map[int]string{}
	found := map[string]*rlink{}
	for _, ifi := range ifs {
		s.names[ifi.Index] = ifi.Name
		l := &rlink{index: ifi.Index, ifi: ifi}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			ones, _ := ipn.Mask.Size()
			s.own[addr] = true
			l.prefixes = append(l.prefixes, netip.PrefixFrom(addr, ones).Masked())
		}
		found[ifi.Name] = l
	}
	s.links = nil
	s.byIndex = map[int]*rlink{}
	s.byName = map[string]*rlink{}
	s.joinProblem = ""
	for _, want := range s.cfg.Links {
		l := found[want.Name]
		if l == nil || l.ifi.Flags&net.FlagMulticast == 0 {
			if !s.missing[want.Name] {
				r.log.Debug("discovery interface skipped until it exists with multicast", "interface", want.Name)
				s.missing[want.Name] = true
			}
			continue
		}
		delete(s.missing, want.Name)
		l.Link = want
		s.links = append(s.links, l)
		s.byIndex[l.index] = l
		s.byName[l.Name] = l
		if l.has(false) {
			r.joinLocked(s, s.m4, l, mdnsGroup4)
			r.joinLocked(s, s.g4, l, ssdpGroup)
		}
		if l.has(true) {
			r.joinLocked(s, s.m6, l, mdnsGroup6)
		}
	}
}

func (r *Relay) joinLocked(s *session, c conn, l *rlink, group netip.Addr) {
	if c == nil {
		return
	}
	k := joinKey{group, l.Name}
	if s.joined[k] == l.index {
		return
	}
	if err := c.join(&l.ifi, group); err != nil {
		s.joinProblem = fmt.Sprintf("join %s on %s: %v", group, l.Name, err)
		r.log.Warn("discovery join", "group", group, "interface", l.Name, "err", err)
		return
	}
	s.joined[k] = l.index
}

func asking(k Kind) bool {
	return k == KindQuery || k == KindLegacy || k == KindProbe || k == KindSearch
}

func answering(k Kind) bool {
	return k == KindAnswer || k == KindAlive || k == KindByebye || k == KindUpdate
}

// destinations names the links a packet of kind k arriving on from goes to.
func destinations(k Kind, from string, links []Link) []string {
	i := slices.IndexFunc(links, func(l Link) bool { return l.Name == from })
	if i < 0 {
		return nil
	}
	src := links[i]
	var out []string
	for _, l := range links {
		if l.Name == from {
			continue
		}
		if (asking(k) && src.Asks && l.Answers) || (answering(k) && src.Answers && l.Asks) {
			out = append(out, l.Name)
		}
	}
	return out
}

// headerKind classifies an mDNS payload by its header alone.
func headerKind(p []byte, srcPort int) (Kind, bool) {
	if len(p) < 12 {
		return "", false
	}
	switch {
	case p[2]&0x80 != 0:
		return KindAnswer, true
	case srcPort != mdnsPort:
		return KindLegacy, true
	case binary.BigEndian.Uint16(p[8:10]) > 0:
		return KindProbe, true
	}
	return KindQuery, true
}

func (s *session) linkSet() []Link {
	out := make([]Link, len(s.links))
	for i, l := range s.links {
		out[i] = l.Link
	}
	return out
}

func (s *session) bucket(link string, now time.Time) *bucket {
	b := s.buckets[link]
	if b == nil {
		b = newBucket(now)
		s.buckets[link] = b
	}
	return b
}

// quietDrop drops ev with reason, keeping at most one such event a second per link and reason.
func (s *session) quietDrop(ev *Event, reason string, now time.Time) *Event {
	k := quietKey{ev.From, reason}
	if at, ok := s.quiet[k]; ok && now.Sub(at) < time.Second {
		return nil
	}
	s.quiet[k] = now
	ev.Dropped = reason
	return ev
}

// admit applies dedupe and the cap; a non-empty reason drops.
func (r *Relay) admitLocked(s *session, l *rlink, payload []byte, now time.Time) string {
	if s.dedupe.seen(payload, l.index, now) {
		return "duplicate"
	}
	if !s.bucket(l.Name, now).take(now) {
		r.overrun[l.Name] = now
		return "over cap"
	}
	return ""
}

func (r *Relay) emit(ev *Event) {
	if ev != nil && r.events != nil {
		r.events.Add(*ev)
	}
}

func (r *Relay) begin(s *session, protocol string, ifindex int, src netip.AddrPort) (*Event, *rlink, time.Time, bool) {
	now := r.Now()
	ev := &Event{Stamp: logring.Stamp{Time: now}, Protocol: protocol, Source: src.Addr().String()}
	if s.closed {
		return nil, nil, now, false
	}
	l := s.byIndex[ifindex]
	if l == nil {
		ev.From = s.names[ifindex]
		ev.Dropped = "not in relay"
		return ev, nil, now, false
	}
	ev.From = l.Name
	if s.own[src.Addr()] {
		ev.Dropped = "own address"
		return ev, l, now, false
	}
	return ev, l, now, true
}

func (r *Relay) mdnsPacket(s *session, v6 bool, b []byte, ifindex int, src netip.AddrPort, dst netip.Addr) {
	defer panics.Drop(r.log, "discovery packet")
	r.emit(r.mdnsLocked(s, v6, b, ifindex, src, dst))
}

func (r *Relay) mdnsLocked(s *session, v6 bool, b []byte, ifindex int, src netip.AddrPort, dst netip.Addr) *Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, l, now, ok := r.begin(s, protocolMDNS, ifindex, src)
	if !ok {
		return ev
	}
	if !dst.IsMulticast() && dst != broadcast4 && !l.onLink(src.Addr()) {
		ev.Dropped = "off-link"
		return ev
	}
	m, kind, reason := r.classify(s, b, int(src.Port()))
	ev.Kind = kind
	if reason != "" {
		ev.Dropped = reason
		return ev
	}
	if m != nil {
		ev.Name = m.Name()
		if kind == KindAnswer && l.Answers {
			r.seen.ObserveMDNS(l.Name, m)
		}
	}
	dests := destinations(kind, l.Name, s.linkSet())
	if len(dests) == 0 {
		ev.Dropped = "no role"
		return ev
	}
	out, reason := rewrite(b, m, kind, s.filter)
	if out == nil {
		ev.Dropped = reason
		return ev
	}
	if reason := r.admitLocked(s, l, out, now); reason != "" {
		return s.quietDrop(ev, reason, now)
	}
	c, group := s.m4, mdnsGroup4
	if v6 {
		c, group = s.m6, mdnsGroup6
	}
	if kind == KindLegacy {
		tbl := &s.slots4
		c = s.l4
		if v6 {
			tbl, c = &s.slots6, s.l6
		}
		n, ok := tbl.take(binary.BigEndian.Uint16(out), src, l.Name, now)
		if c == nil || !ok {
			ev.Dropped = "no slot"
			return ev
		}
		binary.BigEndian.PutUint16(out, n)
	}
	ev.To = r.sendLocked(s, c, out, dests, netip.AddrPortFrom(group, mdnsPort), now)
	return ev
}

// classify parses an mDNS payload, falling back to the header when no filter needs the records.
func (r *Relay) classify(s *session, b []byte, srcPort int) (*MDNS, Kind, string) {
	m, err := ParseMDNS(b, srcPort)
	if err == nil {
		return m, m.Kind, ""
	}
	kind, ok := headerKind(b, srcPort)
	if !ok || len(s.filter) > 0 {
		return nil, kind, "parse"
	}
	return nil, kind, ""
}

// rewrite clears QU on questions and applies Rewrite when the payload parsed; nil drops with the reason.
func rewrite(b []byte, m *MDNS, kind Kind, allow Filter) ([]byte, string) {
	if kind != KindAnswer {
		if _, err := ClearQU(b); err != nil {
			return nil, "parse"
		}
	}
	if m == nil {
		return b, ""
	}
	out, reason := Rewrite(b, m, allow)
	if out == nil && reason == "" {
		reason = "no records"
	}
	return out, reason
}

func (r *Relay) sendLocked(s *session, c conn, out []byte, dests []string, to netip.AddrPort, now time.Time) []string {
	var sent []string
	for _, name := range dests {
		d := s.byName[name]
		if d == nil || c == nil {
			continue
		}
		if err := c.write(out, d.index, to); err != nil {
			r.log.Debug("discovery send", "interface", name, "to", to, "err", err)
			continue
		}
		s.dedupe.mark(out, d.index, now)
		sent = append(sent, name)
	}
	return sent
}

func (r *Relay) legacyReply(s *session, v6 bool, b []byte, ifindex int, src netip.AddrPort, _ netip.Addr) {
	defer panics.Drop(r.log, "discovery packet")
	r.emit(r.legacyLocked(s, v6, b, ifindex, src))
}

func (r *Relay) legacyLocked(s *session, v6 bool, b []byte, ifindex int, src netip.AddrPort) *Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, l, now, ok := r.begin(s, protocolMDNS, ifindex, src)
	if !ok {
		return ev
	}
	if !l.onLink(src.Addr()) {
		ev.Dropped = "off-link"
		return ev
	}
	m, kind, reason := r.classify(s, b, int(src.Port()))
	ev.Kind = kind
	if reason != "" {
		ev.Dropped = reason
		return ev
	}
	if m != nil {
		ev.Name = m.Name()
	}
	if kind != KindAnswer || !l.Answers {
		ev.Dropped = "no role"
		return ev
	}
	tbl := &s.slots4
	if v6 {
		tbl = &s.slots6
	}
	sl, ok := tbl.lookup(binary.BigEndian.Uint16(b), now)
	if !ok {
		ev.Dropped = "no slot"
		return ev
	}
	client := s.byName[sl.link]
	if client == nil || client == l {
		ev.Dropped = "no role"
		return ev
	}
	if m != nil {
		r.seen.ObserveMDNS(l.Name, m)
	}
	out, reason := rewrite(b, m, kind, s.filter)
	if out == nil {
		ev.Dropped = reason
		return ev
	}
	if !s.bucket(l.Name, now).take(now) {
		r.overrun[l.Name] = now
		return s.quietDrop(ev, "over cap", now)
	}
	binary.BigEndian.PutUint16(out, sl.id)
	c := s.m4
	if v6 {
		c = s.m6
	}
	ev.To = r.sendLocked(s, c, out, []string{client.Name}, sl.client, now)
	return ev
}

func (r *Relay) ssdpPacket(s *session, bcast bool, b []byte, ifindex int, src netip.AddrPort, _ netip.Addr) {
	defer panics.Drop(r.log, "discovery packet")
	r.emit(r.ssdpLocked(s, bcast, b, ifindex, src))
}

func (r *Relay) ssdpLocked(s *session, bcast bool, b []byte, ifindex int, src netip.AddrPort) *Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, l, now, ok := r.begin(s, protocolSSDP, ifindex, src)
	if !ok {
		return ev
	}
	m, err := ParseSSDP(b)
	if err != nil {
		ev.Dropped = "parse"
		return ev
	}
	ev.Kind, ev.Name = m.Kind, m.Name()
	if m.Kind == KindAlive && l.Answers {
		r.seen.ObserveSSDP(l.Name, src.Addr(), m)
	}
	dests := destinations(m.Kind, l.Name, s.linkSet())
	if len(dests) == 0 {
		ev.Dropped = "no role"
		return ev
	}
	if reason := r.admitLocked(s, l, b, now); reason != "" {
		return s.quietDrop(ev, reason, now)
	}
	to := netip.AddrPortFrom(ssdpGroup, ssdpPort)
	if bcast {
		to = netip.AddrPortFrom(broadcast4, ssdpPort)
	}
	if m.Kind != KindSearch {
		ev.To = r.sendLocked(s, s.send, b, dests, to, now)
		return ev
	}
	p := r.proxyLocked(s, src, l.Name)
	if p == nil {
		ev.Dropped = "no slot"
		return ev
	}
	p.link = l.Name
	p.expires = now.Add(searchLife(m.MX))
	ev.To = r.sendLocked(s, p.conn, b, dests, to, now)
	return ev
}

// proxyLocked finds or opens the search proxy for client, nil when no port is free.
func (r *Relay) proxyLocked(s *session, client netip.AddrPort, link string) *proxy {
	if p := s.proxies.byClient[client]; p != nil {
		return p
	}
	for _, port := range s.proxies.free() {
		c, err := listen4(fmt.Sprintf("0.0.0.0:%d", port), sockOpts{broadcast: true}, 64, 2)
		if err != nil {
			r.log.Debug("discovery search port", "port", port, "err", err)
			continue
		}
		p := &proxy{port: port, client: client, link: link, conn: c}
		s.proxies.add(p)
		r.wg.Go(func() { r.read(s, c, false, r.proxyHandler(p)) })
		r.wg.Go(func() { r.expireProxy(s, p) })
		return p
	}
	return nil
}

// expireProxy closes p once it has gone a search's life without a search.
func (r *Relay) expireProxy(s *session, p *proxy) {
	for {
		r.mu.Lock()
		if s.closed || s.proxies.byClient[p.client] != p {
			r.mu.Unlock()
			return
		}
		wait := p.expires.Sub(r.Now())
		if wait <= 0 {
			delete(s.proxies.byClient, p.client)
			_ = p.conn.Close()
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		time.Sleep(min(wait, time.Second))
	}
}

func (r *Relay) proxyHandler(p *proxy) func(*session, bool, []byte, int, netip.AddrPort, netip.Addr) {
	return func(s *session, _ bool, b []byte, ifindex int, src netip.AddrPort, _ netip.Addr) {
		defer panics.Drop(r.log, "discovery packet")
		r.emit(r.proxyReplyLocked(s, p, b, ifindex, src))
	}
}

func (r *Relay) proxyReplyLocked(s *session, p *proxy, b []byte, ifindex int, src netip.AddrPort) *Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.proxies.byClient[p.client] != p {
		return nil
	}
	ev, l, now, ok := r.begin(s, protocolSSDP, ifindex, src)
	if !ok {
		return ev
	}
	m, err := ParseSSDP(b)
	if err != nil {
		ev.Dropped = "parse"
		return ev
	}
	ev.Kind, ev.Name = m.Kind, m.Name()
	client := s.byName[p.link]
	if m.Kind != KindReply || !l.Answers || client == nil || client == l {
		ev.Dropped = "no role"
		return ev
	}
	r.seen.ObserveSSDP(l.Name, src.Addr(), m)
	if !s.bucket(l.Name, now).take(now) {
		r.overrun[l.Name] = now
		return s.quietDrop(ev, "over cap", now)
	}
	ev.To = r.sendLocked(s, p.conn, b, []string{client.Name}, p.client, now)
	return ev
}
