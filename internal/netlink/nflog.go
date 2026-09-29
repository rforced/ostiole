package netlink

import (
	"context"
	"encoding/binary"
	"errors"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// LogPacket is one packet an nft log statement sent to a group.
type LogPacket struct {
	// Prefix is the log statement's prefix.
	Prefix string
	// InDev and OutDev are the devices the packet came in and was going
	// out on, zero where there was none.
	InDev, OutDev int
	// Payload is the packet from its network header on, as much of it as
	// the kernel copied. It is the packet's own copy.
	Payload []byte
	// Time is when the packet arrived. The kernel stamps what it logs at
	// prerouting, input and forward, and leaves it zero elsewhere.
	Time time.Time
}

// nfnetlink_log's message types and attributes, from
// linux/netfilter/nfnetlink_log.h. x/sys/unix does not have them.
const (
	nfulnlMsgPacket = 0
	nfulnlMsgConfig = 1

	nfulaCfgCmd  = 1
	nfulaCfgMode = 2

	nfulnlCfgCmdBind = 1
	nfulnlCopyPacket = 2

	nfulaTimestamp     = 3
	nfulaIfindexIndev  = 4
	nfulaIfindexOutdev = 5
	nfulaPayload       = 9
	nfulaPrefix        = 10
)

// LogReader receives the packets of one nflog group.
type LogReader struct {
	c *conn
	// closed tells Read the failure it sees is the reader being closed
	// under it, which the poller reports in words of its own.
	closed atomic.Bool
}

// OpenLog binds an nflog group. The group copies whole packets, up to the
// 65531 bytes the kernel copies at most, and is this reader's until it is
// closed: another socket that tries to bind it is refused with EPERM.
func OpenLog(group uint16) (*LogReader, error) {
	c, err := dial(unix.NETLINK_NETFILTER, 0)
	if err != nil {
		return nil, err
	}
	r := &LogReader{c: c}
	// A copy range of 0 asks for as much as the kernel will copy.
	mode := binary.BigEndian.AppendUint32(nil, 0)
	mode = append(mode, nfulnlCopyPacket, 0)
	for _, cfg := range []struct {
		typ  uint16
		data []byte
	}{
		{nfulaCfgCmd, []byte{nfulnlCfgCmdBind}},
		{nfulaCfgMode, mode},
	} {
		var e encoder
		e.nfgenmsg(unix.AF_UNSPEC, group)
		e.attr(cfg.typ, cfg.data)
		if err := c.exchange(unix.NFNL_SUBSYS_ULOG<<8|nfulnlMsgConfig, unix.NLM_F_ACK, e.b, nil); err != nil {
			_ = c.close()
			return nil, err
		}
	}
	return r, nil
}

// Close gives the group back.
func (r *LogReader) Close() error {
	r.closed.Store(true)
	return r.c.close()
}

// Read hands each packet to fn until ctx is done. A read that fails goes
// to warn and reading carries on, since nothing restarts a reader that
// stops: that includes the socket's buffer filling faster than it is
// read, where the kernel drops what does not fit.
func (r *LogReader) Read(ctx context.Context, fn func(LogPacket), warn func(error)) error {
	stop := context.AfterFunc(ctx, func() { _ = r.c.f.SetReadDeadline(time.Now()) })
	defer stop()
	for {
		msgs, err := r.c.receive()
		switch {
		case ctx.Err() != nil, r.closed.Load():
			return nil
		case err != nil:
			warn(err)
			// A full buffer is past once it is read again; anything else
			// gets a moment first.
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
			if m.typ != unix.NFNL_SUBSYS_ULOG<<8|nfulnlMsgPacket {
				continue
			}
			p, err := parseLogPacket(m.data)
			if err != nil {
				warn(err)
				continue
			}
			fn(p)
		}
	}
}

// parseLogPacket reads one packet. Past struct nfgenmsg come its
// attributes, whose numbers are big-endian.
func parseLogPacket(b []byte) (LogPacket, error) {
	if len(b) < 4 {
		return LogPacket{}, errors.New("netlink: a short log packet")
	}
	as, err := attrs(b[4:])
	if err != nil {
		return LogPacket{}, err
	}
	var p LogPacket
	for _, a := range as {
		switch a.typ {
		case nfulaPrefix:
			p.Prefix = cstring(a.data)
		case nfulaIfindexIndev:
			p.InDev = int(be32(a.data))
		case nfulaIfindexOutdev:
			p.OutDev = int(be32(a.data))
		case nfulaPayload:
			p.Payload = append([]byte(nil), a.data...)
		case nfulaTimestamp:
			// struct nfulnl_msg_packet_timestamp: seconds, microseconds.
			if len(a.data) >= 16 {
				sec, usec := be64(a.data), be64(a.data[8:])
				p.Time = time.Unix(int64(sec), int64(usec)*1000) //nolint:gosec // a timestamp the kernel wrote
			}
		}
	}
	return p, nil
}
