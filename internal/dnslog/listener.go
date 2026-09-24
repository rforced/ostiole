package dnslog

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	nflog "github.com/florianl/go-nflog/v2"

	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/panics"
)

// Listener reads the nflog group the resolver's answers are copied to and
// feeds the log. It needs CAP_NET_ADMIN.
type Listener struct {
	Log  *Log
	Slog *slog.Logger
}

// Run blocks until ctx is done. A reply that arrives while the log is off
// is parsed and thrown away by Add: the rule and the listener are switched
// on and off at different moments, and the log is the one that decides.
func (l *Listener) Run(ctx context.Context) error {
	log := l.Slog
	if log == nil {
		log = slog.Default()
	}
	nf, err := nflog.Open(&nflog.Config{
		Group:    nft.QueryLogGroup,
		Copymode: nflog.CopyPacket,
		// A DNS answer can be a kilobyte and a busy resolver answers
		// thousands a second, so the receive buffer is larger than the
		// firewall log's.
		Bufsize: 256 * 1024,
	})
	if err != nil {
		return fmt.Errorf("open nflog group %d: %w", nft.QueryLogGroup, err)
	}
	defer func() { _ = nf.Close() }()

	hook := func(attrs nflog.Attribute) int {
		// An answer carries whatever a remote server put in it: one that
		// trips the parser is dropped, not the daemon.
		defer panics.Drop(log, "query log packet")
		if attrs.Payload == nil || !l.Log.Enabled() {
			return 0
		}
		client, payload, ok := udpReply(*attrs.Payload)
		if !ok {
			return 0
		}
		e, lists, ok := classify(l.Log.Index(), payload)
		if !ok {
			return 0
		}
		e.Client = client
		e.Time = time.Now()
		if attrs.Timestamp != nil {
			e.Time = *attrs.Timestamp
		}
		l.Log.Add(e, lists)
		return 0
	}
	errFn := func(err error) int {
		log.Warn("nflog", "err", err)
		return 0
	}
	if err := nf.RegisterWithErrorFunc(ctx, hook, errFn); err != nil {
		return fmt.Errorf("register nflog: %w", err)
	}
	log.Info("query log listener running", "group", nft.QueryLogGroup)
	<-ctx.Done()
	return nil
}

// udpReply peels an IPv4 or IPv6 UDP packet off a DNS answer and returns
// who it was sent to. The rule matches source port 53, but the check is
// repeated here rather than trusted: nothing else in this package would
// notice a rule that grew a second match.
//
// fwlog.Decode fills a firewall entry and does not say where the payload
// starts, so the headers are read again here rather than exported.
func udpReply(b []byte) (netip.Addr, []byte, bool) {
	if len(b) < 1 {
		return netip.Addr{}, nil, false
	}
	var dst netip.Addr
	var rest []byte
	switch b[0] >> 4 {
	case 4:
		if len(b) < 20 {
			return netip.Addr{}, nil, false
		}
		ihl := int(b[0]&0x0f) * 4
		// A fragment past the first carries no UDP header; the resolver's
		// answers on a LAN are not fragmented.
		frag := binary.BigEndian.Uint16(b[6:8]) & 0x1fff
		if ihl < 20 || len(b) < ihl || b[9] != 17 || frag != 0 {
			return netip.Addr{}, nil, false
		}
		dst, rest = netip.AddrFrom4([4]byte(b[16:20])), b[ihl:]
	case 6:
		// Extension headers on a local answer would mean somebody else
		// built the packet, so the whole family of them is dropped
		// rather than walked.
		if len(b) < 40 || b[6] != 17 {
			return netip.Addr{}, nil, false
		}
		dst, rest = netip.AddrFrom16([16]byte(b[24:40])), b[40:]
	default:
		return netip.Addr{}, nil, false
	}
	if len(rest) < 8 || binary.BigEndian.Uint16(rest[0:2]) != 53 {
		return netip.Addr{}, nil, false
	}
	// The UDP length covers the header, and the kernel copied the whole
	// packet, so anything past it is padding.
	end := int(binary.BigEndian.Uint16(rest[4:6]))
	if end < 8 || end > len(rest) {
		end = len(rest)
	}
	return dst.Unmap(), rest[8:end], true
}
