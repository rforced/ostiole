// Package netlink talks to the kernel over netlink sockets: links,
// addresses, routes, rules, neighbours and qdiscs through rtnetlink, the
// connection tracking table, and the packets an nft log statement sends to
// a group. It holds what Ostiole asks and nothing more.
package netlink

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// native is the byte order of netlink's headers and of rtnetlink's
// attributes. Netfilter's attributes are big-endian, and say so where they
// are read.
var native = binary.NativeEndian

// ErrLinkNotFound is a lookup by name for a link the kernel does not have.
var ErrLinkNotFound = errors.New("link not found")

// errInterrupted is a dump the kernel says changed while it was read.
var errInterrupted = errors.New("the dump changed while it was read")

// dumpAttempts is how many times a dump is read before an interrupted one
// is an error.
const dumpAttempts = 3

// kernelError is a request the kernel refused: the errno, which errors.Is
// finds, and the kernel's own explanation when it gives one.
type kernelError struct {
	errno unix.Errno
	msg   string
}

func (e *kernelError) Error() string {
	if e.msg == "" {
		return e.errno.Error()
	}
	return e.errno.Error() + ": " + e.msg
}

func (e *kernelError) Unwrap() error { return e.errno }

// message is one netlink message: its header, and what follows it.
type message struct {
	typ   uint16
	flags uint16
	seq   uint32
	data  []byte
}

// conn is one netlink socket. Its reads go through the runtime's poller,
// so a deadline interrupts one.
type conn struct {
	f   *os.File
	raw syscall.RawConn
	seq uint32
}

// dial opens a socket for proto, joined to the multicast groups in groups.
// It belongs to the network namespace of the thread that opens it.
func dial(proto int, groups uint32) (*conn, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, proto)
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}
	// The kernel's own explanation of a refusal, without the request
	// echoed back in front of it. An old kernel just says less.
	_ = unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_EXT_ACK, 1)
	_ = unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_CAP_ACK, 1)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		_ = unix.Close(fd)
		return nil, os.NewSyscallError("bind", err)
	}
	f := os.NewFile(uintptr(fd), "netlink")
	raw, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &conn{f: f, raw: raw}, nil
}

func (c *conn) close() error { return c.f.Close() }

// send writes one request and returns its sequence number.
func (c *conn) send(typ, flags uint16, body []byte) (uint32, error) {
	c.seq++
	b := make([]byte, unix.NLMSG_HDRLEN, unix.NLMSG_HDRLEN+len(body))
	native.PutUint32(b, uint32(unix.NLMSG_HDRLEN+len(body))) //nolint:gosec // requests are a few hundred bytes
	native.PutUint16(b[4:], typ)
	native.PutUint16(b[6:], flags|unix.NLM_F_REQUEST)
	native.PutUint32(b[8:], c.seq)
	b = append(b, body...)
	var serr error
	err := c.raw.Write(func(fd uintptr) bool {
		serr = unix.Sendto(int(fd), b, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK})
		return !errors.Is(serr, unix.EAGAIN)
	})
	if err == nil {
		err = serr
	}
	if err != nil {
		return 0, os.NewSyscallError("sendto", err)
	}
	return c.seq, nil
}

// receive reads one datagram from the kernel. It is sized by a peek first,
// so nothing is cut however much the kernel packed into it, and it gets a
// buffer of its own, so what it holds outlives the next read.
func (c *conn) receive() ([]message, error) {
	for {
		var b []byte
		var from unix.Sockaddr
		var rerr error
		err := c.raw.Read(func(fd uintptr) bool {
			var n int
			n, _, rerr = recvfrom(int(fd), nil, unix.MSG_PEEK|unix.MSG_TRUNC)
			if rerr != nil {
				return !errors.Is(rerr, unix.EAGAIN)
			}
			b = make([]byte, n)
			n, from, rerr = recvfrom(int(fd), b, 0)
			b = b[:max(n, 0)]
			return !errors.Is(rerr, unix.EAGAIN)
		})
		if err == nil && rerr != nil {
			err = os.NewSyscallError("recvfrom", rerr)
		}
		if err != nil {
			return nil, err
		}
		// Another process can write to this socket too; only the kernel
		// is listened to.
		if sa, ok := from.(*unix.SockaddrNetlink); !ok || sa.Pid != 0 {
			continue
		}
		return parseMessages(b)
	}
}

// recvfrom is unix.Recvfrom, taken again when a signal interrupts it.
func recvfrom(fd int, p []byte, flags int) (int, unix.Sockaddr, error) {
	for {
		n, from, err := unix.Recvfrom(fd, p, flags)
		if !errors.Is(err, unix.EINTR) {
			return n, from, err
		}
	}
}

// exchange sends a request and hands each message that answers it to fn,
// until the kernel acknowledges the request or ends the dump.
func (c *conn) exchange(typ, flags uint16, body []byte, fn func(message) error) error {
	seq, err := c.send(typ, flags, body)
	if err != nil {
		return err
	}
	interrupted := false
	for {
		msgs, err := c.receive()
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.seq != seq {
				continue
			}
			if m.flags&unix.NLM_F_DUMP_INTR != 0 {
				interrupted = true
			}
			switch m.typ {
			case unix.NLMSG_NOOP:
				continue
			case unix.NLMSG_ERROR:
				return refusal(m)
			case unix.NLMSG_DONE:
				if err := refusal(m); err != nil {
					return err
				}
				if interrupted {
					return errInterrupted
				}
				return nil
			}
			if fn != nil {
				if err := fn(m); err != nil {
					return err
				}
			}
		}
	}
}

// request sends one rtnetlink request on a socket of its own and waits for
// the kernel to acknowledge it, handing any answer to fn.
func request(typ, flags uint16, e *encoder, fn func(message) error) error {
	if e.err != nil {
		return e.err
	}
	c, err := dial(unix.NETLINK_ROUTE, 0)
	if err != nil {
		return err
	}
	defer func() { _ = c.close() }()
	return c.exchange(typ, flags|unix.NLM_F_ACK, e.b, fn)
}

// dump lists one of rtnetlink's tables. A dump the kernel says changed
// while it was read is taken again from the start.
func dump(typ uint16, e *encoder) ([]message, error) {
	if e.err != nil {
		return nil, e.err
	}
	c, err := dial(unix.NETLINK_ROUTE, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.close() }()
	for attempt := 1; ; attempt++ {
		var out []message
		err := c.exchange(typ, unix.NLM_F_DUMP, e.b, func(m message) error {
			out = append(out, m)
			return nil
		})
		if errors.Is(err, errInterrupted) && attempt < dumpAttempts {
			continue
		}
		return out, err
	}
}

// parseMessages splits a datagram into its messages.
func parseMessages(b []byte) ([]message, error) {
	var out []message
	for len(b) >= unix.NLMSG_HDRLEN {
		n := int(native.Uint32(b))
		if n < unix.NLMSG_HDRLEN || n > len(b) {
			return nil, fmt.Errorf("netlink: a message of %d bytes in %d", n, len(b))
		}
		out = append(out, message{
			typ:   native.Uint16(b[4:]),
			flags: native.Uint16(b[6:]),
			seq:   native.Uint32(b[8:]),
			data:  b[unix.NLMSG_HDRLEN:n],
		})
		b = b[min(align(n), len(b)):]
	}
	return out, nil
}

// refusal reads the error an acknowledgement or the end of a dump carries,
// nil for none.
func refusal(m message) error {
	if len(m.data) < 4 {
		return fmt.Errorf("netlink: a short answer of type %d", m.typ)
	}
	code := int32(native.Uint32(m.data)) //nolint:gosec // the kernel's int, as it wrote it
	if code >= 0 {
		return nil
	}
	e := &kernelError{errno: unix.Errno(-code)}
	if m.flags&unix.NLM_F_ACK_TLVS == 0 {
		return e
	}
	rest := m.data[4:]
	if m.typ == unix.NLMSG_ERROR {
		// The request comes back ahead of the explanation: its header
		// alone when the socket asked for that, whole otherwise.
		if len(rest) < unix.NLMSG_HDRLEN {
			return e
		}
		n := unix.NLMSG_HDRLEN
		if m.flags&unix.NLM_F_CAPPED == 0 {
			n = int(native.Uint32(rest))
		}
		rest = rest[min(align(n), len(rest)):]
	}
	as, err := attrs(rest)
	if err != nil {
		return e
	}
	for _, a := range as {
		if a.typ == unix.NLMSGERR_ATTR_MSG {
			e.msg = cstring(a.data)
		}
	}
	return e
}

// cstring reads a string the kernel ended with a NUL.
func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
