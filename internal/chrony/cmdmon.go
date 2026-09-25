package chrony

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"syscall"
	"time"
)

// The command protocol is chrony's candm.h, version 6, which every chrony
// since 2.2 speaks. A request is a 20-byte header and the command's
// fields; a reply is a 28-byte header and the report. Every field is
// big-endian. A request is padded to the length of its reply, or chronyd
// drops it: the port must not answer more than it was sent.
const (
	protoVersion  = 6
	pktRequest    = 1
	pktReply      = 2
	requestHeader = 20
	replyHeader   = 28
	// maxReply is larger than any reply chronyd sends.
	maxReply = 1024
)

// Statuses a reply carries.
const (
	statusOK            = 0
	statusUnauthorised  = 2
	statusNoSuchSource  = 4
	statusBadPktVersion = 18
)

// Address families of candm.h's IPAddr. A source whose name has not
// resolved yet goes by an ID.
const (
	familyUnspec = 0
	familyInet4  = 1
	familyInet6  = 2
	familyID     = 3
)

// Source modes of a SOURCE_DATA reply.
const (
	modeClient = 0
	modePeer   = 1
	modeRef    = 2
)

// command is one request chronyd answers as monitoring: its code, the
// reply that answers it, and how long the fields of each are.
type command struct {
	name        string // as opencommands names it
	code, reply uint16
	data, rdata int
}

var (
	cmdNSources    = command{"sources", 14, 2, 0, 4}
	cmdSourceData  = command{"sources", 15, 3, 4, 48}
	cmdTracking    = command{"tracking", 33, 5, 0, 76}
	cmdServerStats = command{"serverstats", 54, 25, 0, 168}
	cmdSourceName  = command{"sourcename", 65, 19, 20, 256}
	cmdAuthData    = command{"authdata", 67, 20, 20, 24}
)

func (c command) length() int { return max(requestHeader+c.data, replyHeader+c.rdata) }

// refusal is a reply that says no.
type refusal struct {
	command string
	status  uint16
}

func (r *refusal) Error() string {
	return fmt.Sprintf("chronyd refused %s with status %d", r.command, r.status)
}

// conn is one socket to the command port.
type conn struct {
	net.Conn
	addr string
	buf  [maxReply]byte
}

func dial(ctx context.Context, addr string) (*conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return nil, err
	}
	return &conn{Conn: c, addr: addr}, nil
}

// exchange sends one request and returns the report that answers it. An
// unanswered request goes again after one second, then two, as chronyc
// sends it, each time under a new sequence number so a late reply to an
// earlier one is not taken for it. The number is random, so another
// process on the router cannot answer in chronyd's place.
func (c *conn) exchange(ctx context.Context, cmd command, fields []byte) ([]byte, error) {
	req := make([]byte, cmd.length())
	req[0], req[1] = protoVersion, pktRequest
	binary.BigEndian.PutUint16(req[4:], cmd.code)
	copy(req[requestHeader:], fields)
	stop := context.AfterFunc(ctx, func() { _ = c.SetReadDeadline(time.Now()) })
	defer stop()
	wait := time.Second
	for attempt := range 3 {
		binary.BigEndian.PutUint16(req[6:], uint16(attempt))
		_, _ = rand.Read(req[8:12])
		seq := binary.BigEndian.Uint32(req[8:])
		if _, err := c.Write(req); err != nil {
			return nil, c.failed(err)
		}
		resend := time.Now().Add(wait)
		wait *= 2
		for {
			deadline := resend
			if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
				deadline = d
			}
			if err := c.SetReadDeadline(deadline); err != nil {
				return nil, err
			}
			// After the deadline is set, so a cancel from here on cuts
			// the read short.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n, err := c.Read(c.buf[:])
			if errors.Is(err, os.ErrDeadlineExceeded) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
					return nil, context.DeadlineExceeded
				}
				break
			}
			if err != nil {
				return nil, c.failed(err)
			}
			rpy := c.buf[:n]
			if n < replyHeader || rpy[1] != pktReply || rpy[2] != 0 || rpy[3] != 0 ||
				binary.BigEndian.Uint16(rpy[4:]) != cmd.code || binary.BigEndian.Uint32(rpy[16:]) != seq {
				continue
			}
			return answer(cmd, rpy)
		}
	}
	return nil, fmt.Errorf("chronyd did not answer %s on %s", cmd.name, c.addr)
}

func (c *conn) failed(err error) error {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("chronyd is not listening on %s", c.addr)
	}
	return err
}

// answer checks a reply to cmd and returns a copy of its report.
func answer(cmd command, rpy []byte) ([]byte, error) {
	status := binary.BigEndian.Uint16(rpy[8:])
	switch {
	case status == statusBadPktVersion || rpy[0] != protoVersion:
		return nil, fmt.Errorf("chronyd speaks command protocol %d, not %d", rpy[0], protoVersion)
	case status == statusUnauthorised:
		return nil, fmt.Errorf("%s: %w", cmd.name, ErrNotAuthorised)
	case status != statusOK:
		return nil, &refusal{cmd.name, status}
	case binary.BigEndian.Uint16(rpy[6:]) != cmd.reply:
		return nil, fmt.Errorf("chronyd answered %s with reply %d, not %d", cmd.name, binary.BigEndian.Uint16(rpy[6:]), cmd.reply)
	case len(rpy) < replyHeader+cmd.rdata:
		return nil, fmt.Errorf("chronyd answered %s in %d bytes, short of %d", cmd.name, len(rpy), replyHeader+cmd.rdata)
	}
	return append([]byte(nil), rpy[replyHeader:replyHeader+cmd.rdata]...), nil
}

// noSuchSource reports the refusal chronyd gives for an index or address
// it no longer has: the list changed while it was read.
func noSuchSource(err error) bool {
	var r *refusal
	return errors.As(err, &r) && r.status == statusNoSuchSource
}

// decodeFloat reads chrony's 32-bit float: a 7-bit signed exponent and a
// 25-bit signed coefficient, worth coefficient × 2^(exponent − 25).
func decodeFloat(b []byte) float64 {
	x := binary.BigEndian.Uint32(b)
	exp := int(x >> 25)
	if exp >= 1<<6 {
		exp -= 1 << 7
	}
	coef := int(x & (1<<25 - 1))
	if coef >= 1<<24 {
		coef -= 1 << 25
	}
	return math.Ldexp(float64(coef), exp-25)
}

// decodeTime reads a Timespec: seconds in two words and nanoseconds. A
// high word of 0x7fffffff marks a daemon with 32-bit time. Zero is no time
// at all.
func decodeTime(b []byte) time.Time {
	hi, lo, ns := binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:]), binary.BigEndian.Uint32(b[8:])
	if hi == 0x7fffffff {
		hi = 0
	}
	ns = min(ns, 999999999)
	if hi == 0 && lo == 0 && ns == 0 {
		return time.Time{}
	}
	return time.Unix(int64(hi)<<32|int64(lo), int64(ns)).UTC()
}

// decodeAddr reads an IPAddr: sixteen bytes and a family.
func decodeAddr(b []byte) (netip.Addr, uint16) {
	family := binary.BigEndian.Uint16(b[16:])
	switch family {
	case familyInet4:
		return netip.AddrFrom4([4]byte(b[:4])), family
	case familyInet6:
		return netip.AddrFrom16([16]byte(b[:16])), family
	case familyID:
		return netip.Addr{}, family
	}
	return netip.Addr{}, familyUnspec
}

// refIDName is a reference ID as text, as chronyc writes the ID of a
// reference clock: its printable characters.
func refIDName(id uint32) string {
	var out []byte
	for _, c := range binary.BigEndian.AppendUint32(nil, id) {
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	return string(out)
}

// never is the "no sample yet" count of seconds.
const never = math.MaxUint32

// ago turns a count of seconds that may be never into a duration.
func ago(n uint32) time.Duration {
	if n == never {
		return -1
	}
	return time.Duration(n) * time.Second
}
