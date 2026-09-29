package diag

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// TraceOptions parameterise a traceroute.
type TraceOptions struct {
	Target  string
	MaxHops int
	Timeout time.Duration
	// Resolve looks up the name of every hop, which is slower but far more
	// readable.
	Resolve bool
}

// Hop is one step of the path. An empty Address means nothing answered.
type Hop struct {
	TTL     int     `json:"ttl"`
	Address string  `json:"address,omitempty"`
	Name    string  `json:"name,omitempty"`
	RTTMS   float64 `json:"rttMs,omitempty"`
	Final   bool    `json:"final,omitempty"`
}

// TraceResult is a finished traceroute.
type TraceResult struct {
	Target   string `json:"target"`
	Address  string `json:"address"`
	Hops     []Hop  `json:"hops"`
	Complete bool   `json:"complete"`
}

// Traceroute walks the path with echo requests of increasing time to
// live, reading the time-exceeded messages routers send back.
func Traceroute(ctx context.Context, o TraceOptions) (*TraceResult, error) {
	addr, err := Resolve(ctx, o.Target)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(addr)
	v4 := ip.To4() != nil
	maxHops := o.MaxHops
	if maxHops <= 0 || maxHops > 64 {
		maxHops = 30
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}

	network := "ip4:icmp"
	var echoType icmp.Type = ipv4.ICMPTypeEcho
	if !v4 {
		network = "ip6:ipv6-icmp"
		echoType = ipv6.ICMPTypeEchoRequest
	}
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, network, listenAddress(v4))
	if err != nil {
		return nil, fmt.Errorf("icmp socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	res := &TraceResult{Target: o.Target, Address: addr, Hops: []Hop{}}
	id := os.Getpid() & 0xffff
	for ttl := 1; ttl <= maxHops; ttl++ {
		if err := setTTL(conn, ttl, v4); err != nil {
			return nil, err
		}
		hop, done, err := probeHop(ctx, conn, ip, echoType, id, ttl, timeout, v4)
		if err != nil {
			return res, err
		}
		if o.Resolve && hop.Address != "" {
			if names, err := net.DefaultResolver.LookupAddr(ctx, hop.Address); err == nil && len(names) > 0 {
				hop.Name = names[0]
			}
		}
		res.Hops = append(res.Hops, hop)
		if done {
			res.Complete = true
			break
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
	}
	return res, nil
}

// probeHop sends one echo request and classifies what comes back: a
// time-exceeded message from a router on the way, or an echo reply from
// the target itself.
func probeHop(ctx context.Context, conn net.PacketConn, ip net.IP, echoType icmp.Type, id, ttl int,
	timeout time.Duration, v4 bool,
) (Hop, bool, error) {
	body := &icmp.Echo{ID: id, Seq: ttl, Data: []byte("ostiole-trace")}
	raw, err := (&icmp.Message{Type: echoType, Body: body}).Marshal(nil)
	if err != nil {
		return Hop{TTL: ttl}, false, err
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return Hop{TTL: ttl}, false, err
	}
	start := time.Now()
	if _, err := conn.WriteTo(raw, &net.IPAddr{IP: ip}); err != nil {
		return Hop{TTL: ttl}, false, err
	}

	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				return Hop{TTL: ttl}, false, nil // nobody answered this hop
			}
			return Hop{TTL: ttl}, false, err
		}
		payload := buf[:n]
		if v4 {
			payload = stripIPv4Header(payload)
		}
		msg, err := icmp.ParseMessage(protocolNumber(v4), payload)
		if err != nil {
			continue
		}
		rtt := float64(time.Since(start).Microseconds()) / 1000
		switch b := msg.Body.(type) {
		case *icmp.TimeExceeded:
			if !matchesOurProbe(b.Data, id, ttl, v4) {
				continue
			}
			return Hop{TTL: ttl, Address: peer.String(), RTTMS: rtt}, false, nil
		case *icmp.Echo:
			if b.ID != id || b.Seq != ttl {
				continue
			}
			return Hop{TTL: ttl, Address: peer.String(), RTTMS: rtt, Final: true}, true, nil
		case *icmp.DstUnreach:
			if !matchesOurProbe(b.Data, id, ttl, v4) {
				continue
			}
			// Unreachable is still an answer: the path ends here.
			return Hop{TTL: ttl, Address: peer.String(), RTTMS: rtt, Final: true}, true, nil
		}
	}
}

// matchesOurProbe checks the quoted packet inside an ICMP error against
// the echo request we sent, so another program's traffic is ignored. A
// router only has to quote the original header and eight bytes after it,
// which is the ICMP header and nothing else: the identifier and sequence
// number are all there is to match on.
func matchesOurProbe(quoted []byte, id, seq int, v4 bool) bool {
	if v4 {
		quoted = stripIPv4Header(quoted)
	} else if len(quoted) > 40 {
		quoted = quoted[40:] // fixed IPv6 header
	}
	if len(quoted) < 8 {
		return false
	}
	// Parsing rejects a body this short, so read the header directly.
	if quoted[0] != echoTypeCode(v4) {
		return false
	}
	gotID := int(binary.BigEndian.Uint16(quoted[4:6]))
	gotSeq := int(binary.BigEndian.Uint16(quoted[6:8]))
	return gotID == id && gotSeq == seq
}

// echoTypeCode is the ICMP type of an echo request for the family.
func echoTypeCode(v4 bool) byte {
	if v4 {
		return 8 // ICMP echo
	}
	return 128 // ICMPv6 echo request
}

func setTTL(conn net.PacketConn, ttl int, v4 bool) error {
	if v4 {
		return ipv4.NewPacketConn(conn).SetTTL(ttl)
	}
	return ipv6.NewPacketConn(conn).SetHopLimit(ttl)
}

func listenAddress(v4 bool) string {
	if v4 {
		return "0.0.0.0"
	}
	return "::"
}

func protocolNumber(v4 bool) int {
	if v4 {
		return 1
	}
	return 58
}

// stripIPv4Header drops the leading IP header of a raw IPv4 read.
func stripIPv4Header(b []byte) []byte {
	if len(b) < 20 {
		return b
	}
	hdr := int(b[0]&0x0f) * 4
	if hdr < 20 || hdr > len(b) {
		return b
	}
	return b[hdr:]
}
