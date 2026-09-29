package dnslog

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/rforced/ostiole/internal/netlink"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/panics"
)

// Listener reads the nflog group the resolver's answers are copied to and
// feeds the log, and Names while it wants them. It needs CAP_NET_ADMIN.
type Listener struct {
	Log *Log
	// Names is told every address a client was given, for Traffic's
	// destinations; nil tells nobody.
	Names Names
	Slog  *slog.Logger
}

// Names is what wants the addresses each client was answered.
type Names interface {
	Wanted() bool
	Answered(client netip.Addr, name string, addrs []netip.Addr, at time.Time)
}

// Run blocks until ctx is done. A reply that arrives while the log and
// Names are both off is thrown away: the rule and the listener are
// switched on and off at different moments, and they are the ones that
// decide.
func (l *Listener) Run(ctx context.Context) error {
	log := l.Slog
	if log == nil {
		log = slog.Default()
	}
	lg, err := netlink.OpenLog(nft.QueryLogGroup)
	if err != nil {
		return fmt.Errorf("open nflog group %d: %w", nft.QueryLogGroup, err)
	}
	defer func() { _ = lg.Close() }()
	log.Info("query log listener running", "group", nft.QueryLogGroup)
	return lg.Read(ctx, func(p netlink.LogPacket) {
		// An answer carries whatever a remote server put in it: one that
		// trips the parser is dropped, not the daemon.
		defer panics.Drop(log, "query log packet")
		logged := l.Log.Enabled()
		named := l.Names != nil && l.Names.Wanted()
		if p.Payload == nil || (!logged && !named) {
			return
		}
		client, payload, ok := dnsReply(p.Payload)
		if !ok {
			return
		}
		r, ok := parseReply(payload)
		if !ok {
			return
		}
		at := time.Now()
		if !p.Time.IsZero() {
			at = p.Time
		}
		if named && r.rcode == 0 && len(r.addrs) > 0 {
			l.Names.Answered(client, r.name, r.addrs, at)
		}
		if !logged {
			return
		}
		e, lists := classifyReply(l.Log.Index(), r)
		e.Client, e.Time = client, at
		l.Log.Add(e, lists)
	}, func(err error) { log.Warn("nflog", "err", err) })
}

// dnsReply peels an IPv4 or IPv6 packet off a DNS answer and returns who
// it was sent to: a UDP one, or the TCP segment that starts an answer,
// after its length. The rule matches source port 53, but the check is
// repeated here rather than trusted: nothing else in this package would
// notice a rule that grew a second match.
//
// fwlog.Decode fills a firewall entry and does not say where the payload
// starts, so the headers are read again here rather than exported.
func dnsReply(b []byte) (netip.Addr, []byte, bool) {
	if len(b) < 1 {
		return netip.Addr{}, nil, false
	}
	var dst netip.Addr
	var proto byte
	var rest []byte
	switch b[0] >> 4 {
	case 4:
		if len(b) < 20 {
			return netip.Addr{}, nil, false
		}
		ihl := int(b[0]&0x0f) * 4
		// A fragment past the first carries no transport header; the
		// resolver's answers on a LAN are not fragmented.
		frag := binary.BigEndian.Uint16(b[6:8]) & 0x1fff
		if ihl < 20 || len(b) < ihl || frag != 0 {
			return netip.Addr{}, nil, false
		}
		dst, proto, rest = netip.AddrFrom4([4]byte(b[16:20])), b[9], b[ihl:]
	case 6:
		// Extension headers on a local answer would mean somebody else
		// built the packet, so the whole family of them is dropped
		// rather than walked.
		if len(b) < 40 {
			return netip.Addr{}, nil, false
		}
		dst, proto, rest = netip.AddrFrom16([16]byte(b[24:40])), b[6], b[40:]
	default:
		return netip.Addr{}, nil, false
	}
	var msg []byte
	var ok bool
	switch proto {
	case 17:
		msg, ok = udpPayload(rest)
	case 6:
		msg, ok = tcpAnswer(rest)
	}
	if !ok {
		return netip.Addr{}, nil, false
	}
	return dst.Unmap(), msg, true
}

// udpPayload is what a UDP datagram from port 53 carries.
func udpPayload(rest []byte) ([]byte, bool) {
	if len(rest) < 8 || binary.BigEndian.Uint16(rest[0:2]) != 53 {
		return nil, false
	}
	// The UDP length covers the header, and the kernel copied the whole
	// packet, so anything past it is padding.
	end := int(binary.BigEndian.Uint16(rest[4:6]))
	if end < 8 || end > len(rest) {
		end = len(rest)
	}
	return rest[8:end], true
}

// tcpAnswer is the answer a TCP segment from port 53 starts: past its
// two-byte length, as much of it as the segment holds. An answer too long
// for one segment is read from its first, which has its name and its
// result; the rest of it, and a segment that only acknowledges, are
// passed over.
func tcpAnswer(rest []byte) ([]byte, bool) {
	if len(rest) < 20 || binary.BigEndian.Uint16(rest[0:2]) != 53 {
		return nil, false
	}
	off := int(rest[12]>>4) * 4
	if off < 20 || off > len(rest) {
		return nil, false
	}
	data := rest[off:]
	// A header is 12 bytes; less than that after the length is no start.
	if len(data) < 2+12 {
		return nil, false
	}
	size := int(binary.BigEndian.Uint16(data[0:2]))
	if size < 12 {
		return nil, false
	}
	msg := data[2:]
	if len(msg) > size {
		msg = msg[:size]
	}
	return msg, true
}
