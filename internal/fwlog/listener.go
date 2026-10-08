package fwlog

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"ostiole/internal/netlink"
	"ostiole/internal/panics"
)

// Group is the nflog group the renderer sends log statements to.
const Group = 1

// Listener reads nflog and feeds a Ring.
type Listener struct {
	Ring *Ring
	Log  *slog.Logger
	// Zones names the zone of the drops whose prefix carries none.
	Zones *Zones

	mu     sync.Mutex
	ifaces map[int]string
	last   time.Time
}

// Run blocks until ctx is done, delivering logged packets to the ring. It
// needs CAP_NET_ADMIN.
func (l *Listener) Run(ctx context.Context) error {
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	lg, err := netlink.OpenLog(Group)
	if err != nil {
		return fmt.Errorf("open nflog group %d: %w", Group, err)
	}
	defer func() { _ = lg.Close() }()
	log.Info("firewall log listener running", "group", Group)
	return lg.Read(ctx, func(p netlink.LogPacket) {
		// The packets come from anywhere, the WAN included: one that trips
		// the parser is dropped, not the daemon.
		defer panics.Drop(log, "firewall log packet")
		l.Ring.Add(l.entry(p))
	}, func(err error) { log.Warn("nflog", "err", err) })
}

// entry reads one logged packet.
func (l *Listener) entry(p netlink.LogPacket) Entry {
	e := Entry{Time: time.Now(), Prefix: p.Prefix}
	if !p.Time.IsZero() {
		e.Time = p.Time
	}
	e.RuleID, e.Zone, e.Kind, e.Action = ParsePrefix(e.Prefix)
	e.InIface = l.ifaceName(p.InDev)
	e.OutIface = l.ifaceName(p.OutDev)
	if e.Zone == "" && SystemKinds[e.Kind] {
		e.Zone = l.Zones.Of(e.InIface)
	}
	if p.Payload != nil {
		Decode(&e, p.Payload)
	}
	return e
}

// ifaceName resolves an ifindex, refreshing the cache every few seconds so
// VLANs that appear later are named too.
func (l *Listener) ifaceName(index int) string {
	if index == 0 {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if name, ok := l.ifaces[index]; ok && time.Since(l.last) < 10*time.Second {
		return name
	}
	if time.Since(l.last) >= 10*time.Second || l.ifaces == nil {
		l.ifaces = map[int]string{}
		if list, err := net.Interfaces(); err == nil {
			for _, i := range list {
				l.ifaces[i.Index] = i.Name
			}
		}
		l.last = time.Now()
	}
	if name, ok := l.ifaces[index]; ok {
		return name
	}
	return fmt.Sprintf("if%d", index)
}
