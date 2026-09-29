package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

var errNoAddress = errors.New("no gateway address yet")

// ICMPProber pings the monitor address. It needs a raw socket, which the
// daemon has because it runs as root.
type ICMPProber struct {
	// ID identifies our echo requests; the process ID, as ping does.
	ID int
	// seq numbers the requests within one process run.
	seq int
}

// NewICMPProber returns a prober tagged with this process.
func NewICMPProber() *ICMPProber { return &ICMPProber{ID: os.Getpid() & 0xffff} }

// Probe sends one echo request and waits for its reply.
func (p *ICMPProber) Probe(ctx context.Context, address, iface string, timeout time.Duration) (time.Duration, error) {
	ip := net.ParseIP(address)
	if ip == nil {
		return 0, fmt.Errorf("invalid monitor address %q", address)
	}
	v4 := ip.To4() != nil

	network := "ip4:icmp"
	var typ icmp.Type = ipv4.ICMPTypeEcho
	if !v4 {
		network = "ip6:ipv6-icmp"
		typ = ipv6.ICMPTypeEchoRequest
	}
	// A raw socket, not x/net/icmp's wrapper, so the probe can be pinned to
	// one interface.
	var lc net.ListenConfig
	conn, err := lc.ListenPacket(ctx, network, listenAddress(v4))
	if err != nil {
		return 0, fmt.Errorf("icmp socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Pinning to the gateway's interface makes the answer prove that path
	// works, not merely that the address is reachable somehow.
	if iface != "" {
		if err := bindDevice(conn, iface); err != nil {
			return 0, err
		}
	}

	p.seq++
	seq := p.seq & 0xffff
	msg := icmp.Message{
		Type: typ,
		Code: 0,
		Body: &icmp.Echo{ID: p.ID, Seq: seq, Data: []byte("ostiole")},
	}
	raw, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	start := time.Now()
	if _, err := conn.WriteTo(raw, &net.IPAddr{IP: ip}); err != nil {
		return 0, err
	}

	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return 0, err
		}
		if peer.String() != ip.String() {
			continue
		}
		payload := buf[:n]
		if v4 {
			// A raw IPv4 socket hands over the IP header as well.
			payload = stripIPv4Header(payload)
		}
		reply, err := icmp.ParseMessage(protocolNumber(v4), payload)
		if err != nil {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if !ok || echo.ID != p.ID || echo.Seq != seq {
			continue
		}
		switch reply.Type {
		case ipv4.ICMPTypeEchoReply, ipv6.ICMPTypeEchoReply:
			return time.Since(start), nil
		}
	}
}

func listenAddress(v4 bool) string {
	if v4 {
		return "0.0.0.0"
	}
	return "::"
}

func protocolNumber(v4 bool) int {
	if v4 {
		return 1 // ICMP
	}
	return 58 // ICMPv6
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
