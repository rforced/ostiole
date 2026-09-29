package netlink

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Flow is one connection the kernel tracks.
type Flow struct {
	// ID names the connection while the kernel tracks it, and never
	// another at the same time.
	ID uint32
	// Forward is the connection as it was opened and Reverse as its
	// replies come back. The two differ where it is translated.
	Forward, Reverse Tuple
	Mark             uint32
	// Timeout is how many seconds the entry has left.
	Timeout uint32
	// TCPState is the tracked TCP state, zero for other protocols.
	TCPState uint8
}

// Tuple is one direction of a flow.
type Tuple struct {
	Protocol         uint8
	SrcIP, DstIP     net.IP
	SrcPort, DstPort uint16
	// Packets and Bytes stay zero unless net.netfilter.nf_conntrack_acct
	// is on.
	Packets, Bytes uint64
}

// ctnetlink's message types and attributes, from
// linux/netfilter/nfnetlink_conntrack.h. x/sys/unix does not have them.
const (
	ipctnlMsgCtNew    = 0
	ipctnlMsgCtGet    = 1
	ipctnlMsgCtDelete = 2

	// nfnlgrpConntrackDestroy is the multicast group a connection's end
	// is announced on.
	nfnlgrpConntrackDestroy = 3

	ctaTupleOrig     = 1
	ctaTupleReply    = 2
	ctaProtoinfo     = 4
	ctaTimeout       = 7
	ctaMark          = 8
	ctaCountersOrig  = 9
	ctaCountersReply = 10
	ctaID            = 12

	ctaTupleIP    = 1
	ctaTupleProto = 2

	ctaIPv4Src = 1
	ctaIPv4Dst = 2
	ctaIPv6Src = 3
	ctaIPv6Dst = 4

	ctaProtoNum     = 1
	ctaProtoSrcPort = 2
	ctaProtoDstPort = 3

	ctaProtoinfoTCP      = 1
	ctaProtoinfoTCPState = 1

	ctaCountersPackets = 1
	ctaCountersBytes   = 2
)

// Flows reads the connection tracking table of one family, handing each
// flow to fn as it arrives: a busy router tracks tens of thousands, and
// none of them is kept here.
func Flows(fam int, fn func(Flow)) error {
	var e encoder
	e.nfgenmsg(fam, 0)
	if e.err != nil {
		return e.err
	}
	c, err := dial(unix.NETLINK_NETFILTER, 0)
	if err != nil {
		return err
	}
	defer func() { _ = c.close() }()
	return c.exchange(unix.NFNL_SUBSYS_CTNETLINK<<8|ipctnlMsgCtGet, unix.NLM_F_DUMP, e.b, func(m message) error {
		if m.typ != unix.NFNL_SUBSYS_CTNETLINK<<8|ipctnlMsgCtNew {
			return nil
		}
		f, err := parseFlow(m.data)
		if err != nil {
			return err
		}
		fn(f)
		return nil
	})
}

// parseFlow reads one flow. Past struct nfgenmsg its attributes nest, and
// every number in them is big-endian.
func parseFlow(b []byte) (Flow, error) {
	if len(b) < 4 {
		return Flow{}, errors.New("netlink: a short flow")
	}
	as, err := attrs(b[4:])
	if err != nil {
		return Flow{}, err
	}
	var f Flow
	for _, a := range as {
		switch a.typ {
		case ctaTupleOrig:
			err = f.Forward.parse(a.data)
		case ctaTupleReply:
			err = f.Reverse.parse(a.data)
		case ctaCountersOrig:
			err = f.Forward.parseCounters(a.data)
		case ctaCountersReply:
			err = f.Reverse.parseCounters(a.data)
		case ctaProtoinfo:
			f.TCPState, err = parseTCPState(a.data)
		case ctaTimeout:
			f.Timeout = be32(a.data)
		case ctaMark:
			f.Mark = be32(a.data)
		case ctaID:
			f.ID = be32(a.data)
		}
		if err != nil {
			return Flow{}, err
		}
	}
	return f, nil
}

// FlowEnds receives the connections the kernel stops tracking, each with
// the counters it ended on. A connection announces its end only if it
// opened while net.netfilter.nf_conntrack_events was 1, or while somebody
// listened with it at 2, the kernel's default; its counters are there only
// if it opened while nf_conntrack_acct was 1.
type FlowEnds struct {
	c      *conn
	closed atomic.Bool
}

// flowEndsBuffer is the receive buffer asked for: a burst of ends that
// overruns it loses only those ends.
const flowEndsBuffer = 4 << 20

// OpenFlowEnds joins the group connections' ends are announced on. It
// needs CAP_NET_ADMIN for the buffer it asks for, and takes what the
// system allows without it.
func OpenFlowEnds() (*FlowEnds, error) {
	c, err := dial(unix.NETLINK_NETFILTER, 1<<(nfnlgrpConntrackDestroy-1))
	if err != nil {
		return nil, err
	}
	var serr error
	if err := c.raw.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, flowEndsBuffer)
		if serr != nil {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, flowEndsBuffer)
		}
	}); err != nil {
		_ = c.close()
		return nil, err
	}
	if serr != nil {
		_ = c.close()
		return nil, os.NewSyscallError("setsockopt", serr)
	}
	return &FlowEnds{c: c}, nil
}

// Close leaves the group.
func (r *FlowEnds) Close() error {
	r.closed.Store(true)
	return r.c.close()
}

// Read hands each connection that ends to fn until ctx is done. A read
// that fails goes to warn and reading carries on: an overrun, where the
// kernel dropped what did not fit, is past once the buffer is read again.
func (r *FlowEnds) Read(ctx context.Context, fn func(Flow), warn func(error)) error {
	stop := context.AfterFunc(ctx, func() { _ = r.c.f.SetReadDeadline(time.Now()) })
	defer stop()
	for {
		msgs, err := r.c.receive()
		switch {
		case ctx.Err() != nil, r.closed.Load():
			return nil
		case err != nil:
			warn(err)
			if !errors.Is(err, unix.ENOBUFS) {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(time.Second):
				}
			}
			continue
		}
		for _, m := range msgs {
			if m.typ != unix.NFNL_SUBSYS_CTNETLINK<<8|ipctnlMsgCtDelete {
				continue
			}
			f, err := parseFlow(m.data)
			if err != nil {
				warn(err)
				continue
			}
			fn(f)
		}
	}
}

// flushFlows ends every connection the kernel tracks, as a test needs to
// see ends announced.
func flushFlows() error {
	var e encoder
	e.nfgenmsg(unix.AF_UNSPEC, 0)
	if e.err != nil {
		return e.err
	}
	c, err := dial(unix.NETLINK_NETFILTER, 0)
	if err != nil {
		return err
	}
	defer func() { _ = c.close() }()
	return c.exchange(unix.NFNL_SUBSYS_CTNETLINK<<8|ipctnlMsgCtDelete, unix.NLM_F_ACK, e.b, nil)
}

func (t *Tuple) parse(b []byte) error {
	as, err := attrs(b)
	if err != nil {
		return err
	}
	for _, a := range as {
		if a.typ != ctaTupleIP && a.typ != ctaTupleProto {
			continue
		}
		inner, err := attrs(a.data)
		if err != nil {
			return err
		}
		for _, x := range inner {
			switch {
			case a.typ == ctaTupleIP && (x.typ == ctaIPv4Src || x.typ == ctaIPv6Src):
				t.SrcIP = ip(x.data)
			case a.typ == ctaTupleIP && (x.typ == ctaIPv4Dst || x.typ == ctaIPv6Dst):
				t.DstIP = ip(x.data)
			case a.typ == ctaTupleProto && x.typ == ctaProtoNum && len(x.data) > 0:
				t.Protocol = x.data[0]
			case a.typ == ctaTupleProto && x.typ == ctaProtoSrcPort:
				t.SrcPort = be16(x.data)
			case a.typ == ctaTupleProto && x.typ == ctaProtoDstPort:
				t.DstPort = be16(x.data)
			}
		}
	}
	return nil
}

func (t *Tuple) parseCounters(b []byte) error {
	as, err := attrs(b)
	if err != nil {
		return err
	}
	for _, a := range as {
		switch a.typ {
		case ctaCountersPackets:
			t.Packets = be64(a.data)
		case ctaCountersBytes:
			t.Bytes = be64(a.data)
		}
	}
	return nil
}

func parseTCPState(b []byte) (uint8, error) {
	as, err := attrs(b)
	if err != nil {
		return 0, err
	}
	for _, a := range as {
		if a.typ != ctaProtoinfoTCP {
			continue
		}
		inner, err := attrs(a.data)
		if err != nil {
			return 0, err
		}
		for _, x := range inner {
			if x.typ == ctaProtoinfoTCPState && len(x.data) > 0 {
				return x.data[0], nil
			}
		}
	}
	return 0, nil
}
