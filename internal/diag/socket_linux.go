package diag

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// captureSocket is an AF_PACKET socket wrapped so it can be read with a
// deadline like any other file.
type captureSocket struct{ f *os.File }

// openCaptureSocket listens for every frame on one interface. It needs
// CAP_NET_RAW, which the daemon has as root.
func openCaptureSocket(ifIndex int) (*captureSocket, error) {
	proto := htons(unix.ETH_P_ALL)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(proto))
	if err != nil {
		return nil, fmt.Errorf("packet socket: %w", err)
	}
	addr := &unix.SockaddrLinklayer{Protocol: proto, Ifindex: ifIndex}
	if err := unix.Bind(fd, addr); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind capture to interface: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &captureSocket{f: os.NewFile(uintptr(fd), "ostiole-capture")}, nil
}

func (c *captureSocket) Read(b []byte) (int, error) { return c.f.Read(b) }

func (c *captureSocket) SetReadDeadline(t time.Time) error { return c.f.SetReadDeadline(t) }

func (c *captureSocket) Close() error { return c.f.Close() }

func isTimeout(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded)
}

// htons puts a protocol number in network byte order, which is what the
// packet socket wants.
func htons(v uint16) uint16 { return v<<8 | v>>8 }
