package gateway

import (
	"fmt"
	"net"
	"syscall"
)

// bindDevice pins the socket to one interface so the reply proves that
// path works. It needs CAP_NET_RAW, which the daemon has as root.
func bindDevice(conn net.PacketConn, iface string) error {
	sc, ok := conn.(syscallConn)
	if !ok {
		return fmt.Errorf("probe socket does not expose its file descriptor")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	err = raw.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface)
	})
	if err != nil {
		return err
	}
	if setErr != nil {
		return fmt.Errorf("bind probe to %s: %w", iface, setErr)
	}
	return nil
}

type syscallConn interface {
	SyscallConn() (syscall.RawConn, error)
}
