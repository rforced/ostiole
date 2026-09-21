package fwlog

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	nflog "github.com/florianl/go-nflog/v2"
)

// Group is the nflog group the renderer sends log statements to.
const Group = 1

// Listener reads nflog and feeds a Ring.
type Listener struct {
	Ring *Ring
	Log  *slog.Logger

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
	nf, err := nflog.Open(&nflog.Config{
		Group:    Group,
		Copymode: nflog.CopyPacket,
		Bufsize:  64 * 1024,
	})
	if err != nil {
		return fmt.Errorf("open nflog group %d: %w", Group, err)
	}
	defer func() { _ = nf.Close() }()

	hook := func(attrs nflog.Attribute) int {
		e := Entry{Time: time.Now()}
		if attrs.Timestamp != nil {
			e.Time = *attrs.Timestamp
		}
		if attrs.Prefix != nil {
			e.Prefix = *attrs.Prefix
			e.RuleID, e.Zone, e.Kind, e.Action = ParsePrefix(e.Prefix)
		}
		if attrs.InDev != nil {
			e.InIface = l.ifaceName(int(*attrs.InDev))
		}
		if attrs.OutDev != nil {
			e.OutIface = l.ifaceName(int(*attrs.OutDev))
		}
		if attrs.Payload != nil {
			Decode(&e, *attrs.Payload)
		}
		l.Ring.Add(e)
		return 0
	}
	errFn := func(err error) int {
		log.Warn("nflog", "err", err)
		return 0
	}
	if err := nf.RegisterWithErrorFunc(ctx, hook, errFn); err != nil {
		return fmt.Errorf("register nflog: %w", err)
	}
	log.Info("firewall log listener running", "group", Group)
	<-ctx.Done()
	return nil
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
