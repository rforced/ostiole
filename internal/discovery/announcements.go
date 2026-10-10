package discovery

import (
	"cmp"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	maxSeen       = 2000
	maxTTL        = 24 * time.Hour
	defaultMaxAge = 1800 * time.Second
	flushGrace    = time.Second
	protocolMDNS  = "mdns"
	protocolSSDP  = "ssdp"
)

// Announcement is one service a device announced, for the Announcements tab.
type Announcement struct {
	Protocol  string    `json:"protocol"`
	Interface string    `json:"interface"`
	Type      string    `json:"type"`
	Name      string    `json:"name"`
	Host      string    `json:"host,omitempty"`
	Port      uint16    `json:"port,omitempty"`
	Addresses []string  `json:"addresses,omitempty"`
	Location  string    `json:"location,omitempty"`
	LastSeen  time.Time `json:"lastSeen"`
	Expires   time.Time `json:"expires"`
}

type seenKey struct {
	link string
	name string // lower case
}

type instance struct {
	typ, name string
	host      string
	port      uint16
	lastSeen  time.Time
	expires   time.Time
}

type addrSeen struct {
	seen, expires time.Time
}

type host struct {
	addrs    map[netip.Addr]addrSeen
	lastSeen time.Time
}

// Seen is the table of announcements heard; safe for concurrent use.
type Seen struct {
	now       func() time.Time
	mu        sync.Mutex
	instances map[seenKey]*instance
	hosts     map[seenKey]*host
	devices   map[seenKey]*Announcement
}

// NewSeen returns an empty table reading the time from now.
func NewSeen(now func() time.Time) *Seen {
	if now == nil {
		now = time.Now
	}
	return &Seen{
		now:       now,
		instances: map[seenKey]*instance{},
		hosts:     map[seenKey]*host{},
		devices:   map[seenKey]*Announcement{},
	}
}

// ObserveMDNS records the services and addresses in an mDNS answer heard on link.
func (s *Seen) ObserveMDNS(link string, m *MDNS) {
	if m == nil || m.Kind != KindAnswer {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, r := range m.Records {
		if r.Type == dnsmessage.TypePTR {
			s.observePTR(link, r, now)
		}
	}
	for _, r := range m.Records {
		if r.Type == dnsmessage.TypeSRV {
			s.observeSRV(link, r, now)
		}
	}
	flushed := map[seenKey][2]bool{}
	for _, r := range m.Records {
		if (r.Type == dnsmessage.TypeA || r.Type == dnsmessage.TypeAAAA) && r.Addr.IsValid() {
			s.observeAddr(link, r, now, flushed)
		}
	}
}

func (s *Seen) observePTR(link string, r Record, now time.Time) {
	if strings.HasPrefix(strings.ToLower(r.Name), servicesMeta) || ServiceType(r.Name) == "" {
		return
	}
	typ, name := splitInstance(r.Target)
	if name == "" {
		return
	}
	key := seenKey{link, strings.ToLower(r.Target)}
	if r.TTL == 0 {
		delete(s.instances, key)
		return
	}
	in := s.instances[key]
	if in == nil {
		if len(s.instances) >= maxSeen {
			evict(s.instances, now, func(in *instance) (time.Time, time.Time) { return in.lastSeen, in.expires })
		}
		in = &instance{typ: typ, name: name}
		s.instances[key] = in
	}
	in.lastSeen = now
	in.expires = later(in.expires, now.Add(ttl(r.TTL)))
}

func (s *Seen) observeSRV(link string, r Record, now time.Time) {
	in := s.instances[seenKey{link, strings.ToLower(r.Name)}]
	if in == nil {
		return
	}
	if r.TTL == 0 {
		in.host, in.port = "", 0
		return
	}
	in.host, in.port = strings.TrimSuffix(r.Target, "."), r.Port
	in.lastSeen = now
	in.expires = later(in.expires, now.Add(ttl(r.TTL)))
}

func (s *Seen) observeAddr(link string, r Record, now time.Time, flushed map[seenKey][2]bool) {
	key := seenKey{link, hostKey(r.Name)}
	addr := r.Addr.Unmap()
	h := s.hosts[key]
	if r.TTL == 0 {
		if h != nil {
			delete(h.addrs, addr)
			if len(h.addrs) == 0 {
				delete(s.hosts, key)
			}
		}
		return
	}
	if h == nil {
		if len(s.hosts) >= maxSeen {
			evict(s.hosts, now, func(h *host) (time.Time, time.Time) { return h.lastSeen, hostExpires(h) })
		}
		h = &host{addrs: map[netip.Addr]addrSeen{}}
		s.hosts[key] = h
	}
	family := 0
	if addr.Is6() {
		family = 1
	}
	if f := flushed[key]; r.Flush && !f[family] {
		for a, sa := range h.addrs {
			if a.Is6() == addr.Is6() && now.Sub(sa.seen) > flushGrace {
				delete(h.addrs, a)
			}
		}
		f[family] = true
		flushed[key] = f
	}
	h.addrs[addr] = addrSeen{seen: now, expires: now.Add(ttl(r.TTL))}
	h.lastSeen = now
}

// ObserveSSDP records an SSDP alive or reply heard on link from src, and forgets one on byebye.
func (s *Seen) ObserveSSDP(link string, src netip.Addr, m *SSDP) {
	if m == nil || m.USN == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := seenKey{link, m.USN}
	switch m.Kind {
	case KindByebye:
		delete(s.devices, key)
		return
	case KindAlive, KindReply:
	default:
		return
	}
	now := s.now()
	age := defaultMaxAge
	if m.MaxAge > 0 {
		age = min(time.Duration(m.MaxAge)*time.Second, maxTTL)
	}
	d := s.devices[key]
	if d == nil {
		if len(s.devices) >= maxSeen {
			evict(s.devices, now, func(d *Announcement) (time.Time, time.Time) { return d.LastSeen, d.Expires })
		}
		d = &Announcement{}
		s.devices[key] = d
	}
	*d = Announcement{
		Protocol:  protocolSSDP,
		Interface: link,
		Type:      m.Type(),
		Name:      m.USN,
		Location:  m.Location,
		LastSeen:  now,
		Expires:   now.Add(age),
	}
	if src.IsValid() {
		d.Addresses = []string{src.Unmap().String()}
	}
}

// List returns what is live, expired entries pruned, sorted by Interface, Type and Name.
func (s *Seen) List() []Announcement {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	out := make([]Announcement, 0, len(s.instances)+len(s.devices))
	for key, in := range s.instances {
		a := Announcement{
			Protocol:  protocolMDNS,
			Interface: key.link,
			Type:      in.typ,
			Name:      in.name,
			Host:      in.host,
			Port:      in.port,
			LastSeen:  in.lastSeen,
			Expires:   in.expires,
		}
		if h := s.hosts[seenKey{key.link, hostKey(in.host)}]; in.host != "" && h != nil {
			addrs := make([]netip.Addr, 0, len(h.addrs))
			for addr := range h.addrs {
				addrs = append(addrs, addr)
			}
			slices.SortFunc(addrs, netip.Addr.Compare)
			for _, addr := range addrs {
				a.Addresses = append(a.Addresses, addr.String())
			}
		}
		out = append(out, a)
	}
	for _, d := range s.devices {
		a := *d
		a.Addresses = slices.Clone(a.Addresses)
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b Announcement) int {
		return cmp.Or(
			cmp.Compare(a.Interface, b.Interface),
			cmp.Compare(a.Type, b.Type),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Protocol, b.Protocol),
		)
	})
	return out
}

func (s *Seen) prune(now time.Time) {
	for key, in := range s.instances {
		if !now.Before(in.expires) {
			delete(s.instances, key)
		}
	}
	for key, h := range s.hosts {
		for addr, sa := range h.addrs {
			if !now.Before(sa.expires) {
				delete(h.addrs, addr)
			}
		}
		if len(h.addrs) == 0 {
			delete(s.hosts, key)
		}
	}
	for key, d := range s.devices {
		if !now.Before(d.Expires) {
			delete(s.devices, key)
		}
	}
}

// evict removes the expired entries of a full table, else the one seen longest ago.
func evict[V any](table map[seenKey]V, now time.Time, times func(V) (lastSeen, expires time.Time)) {
	var oldest seenKey
	var oldestSeen time.Time
	found := false
	for key, v := range table {
		seen, expires := times(v)
		if !now.Before(expires) {
			delete(table, key)
			continue
		}
		if !found || seen.Before(oldestSeen) {
			oldest, oldestSeen, found = key, seen, true
		}
	}
	if found && len(table) >= maxSeen {
		delete(table, oldest)
	}
}

func hostExpires(h *host) time.Time {
	var t time.Time
	for _, sa := range h.addrs {
		t = later(t, sa.expires)
	}
	return t
}

func ttl(seconds uint32) time.Duration {
	return min(time.Duration(seconds)*time.Second, maxTTL)
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func hostKey(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// splitInstance returns the service type and the unescaped instance name of a DNS-SD instance name.
func splitInstance(full string) (string, string) {
	typ := ServiceType(full)
	if typ == "" {
		return "", ""
	}
	i := strings.Index(strings.ToLower(full), "."+typ+".")
	if i <= 0 {
		return "", ""
	}
	return typ, unescapeLabel(full[:i])
}

func unescapeLabel(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		if i+3 < len(s) && isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) {
			if n := int(s[i+1]-'0')*100 + int(s[i+2]-'0')*10 + int(s[i+3]-'0'); n < 256 {
				b.WriteByte(byte(n & 0xff))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i+1])
		i++
	}
	return b.String()
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
