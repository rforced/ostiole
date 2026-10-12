// Package openvpn reads what an OpenVPN client is doing: its management
// socket for the state and the counters, the kernel for the data channel,
// and the journal for why the data channel stayed in userspace.
package openvpn

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ostiole/internal/netlink"
)

// SocketDir is the unit's runtime directory, where each tunnel's
// management socket is.
const SocketDir = "/run/ostiole-openvpn"

// dialTimeout bounds the connect; the daemon answers one client at a
// time, and a page waiting on a stuck one is worse than an empty row.
const dialTimeout = 2 * time.Second

// State is what one tunnel reports.
type State struct {
	Connected     bool
	State         string
	Since         time.Time
	TunnelAddress string
	RemoteAddress string
	RemotePort    int
	BytesIn       uint64
	BytesOut      uint64
}

// Runner runs a command; the real one execs it.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Client reads the tunnels. The zero value reads a router's.
type Client struct {
	// Dir holds the management sockets; empty is SocketDir.
	Dir string
	// Run executes journalctl; nil execs it.
	Run Runner
	// LinkKind names a device's driver; nil asks the kernel.
	LinkKind func(name string) (string, error)
}

// Default reads this router's tunnels.
var Default = &Client{}

// Status reads one tunnel's state and counters from this router.
func Status(ctx context.Context, name string) (State, error) { return Default.Status(ctx, name) }

// DataChannel says where this router's tunnel moves its packets.
func DataChannel(name string) string { return Default.DataChannel(name) }

// Note is the journal's last word on this router's data channel.
func Note(ctx context.Context, name string) string { return Default.Note(ctx, name) }

// SocketPath is a tunnel's management socket under dir.
func SocketPath(dir, name string) string { return filepath.Join(dir, name+".sock") }

func (c *Client) dir() string {
	if c.Dir == "" {
		return SocketDir
	}
	return c.Dir
}

func (c *Client) runner() Runner {
	if c.Run == nil {
		return execRunner{}
	}
	return c.Run
}

// Status asks the management socket for `state`, then `status 2`.
// load-stats is never asked: it counts the control channel only.
func (c *Client) Status(ctx context.Context, name string) (State, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "unix", SocketPath(c.dir(), name))
	if err != nil {
		return State{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(2 * dialTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return State{}, err
	}
	r := bufio.NewReader(conn)
	state, err := command(conn, r, "state")
	if err != nil {
		return State{}, err
	}
	status, err := command(conn, r, "status 2")
	if err != nil {
		return State{}, err
	}
	st := ParseState(state)
	st.BytesIn, st.BytesOut = ParseStatus(status)
	return st, nil
}

// command sends one command and reads its answer up to END. The real-time
// lines the daemon interleaves start with ">" and are skipped.
func command(conn net.Conn, r *bufio.Reader, cmd string) ([]string, error) {
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return nil, err
	}
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("openvpn %s: %w", cmd, err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "END":
			return lines, nil
		case strings.HasPrefix(line, ">"):
		case strings.HasPrefix(line, "ERROR:"):
			return nil, fmt.Errorf("openvpn %s: %s", cmd, strings.TrimSpace(strings.TrimPrefix(line, "ERROR:")))
		default:
			lines = append(lines, line)
		}
	}
}

// ParseState reads the answer to `state`: the newest line, as
// `<unix>,CONNECTED,SUCCESS,<local>,<remote>,<port>,,`.
func ParseState(lines []string) State {
	var st State
	for _, line := range lines {
		f := strings.Split(line, ",")
		if len(f) < 2 {
			continue
		}
		unix, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		st = State{State: f[1], Since: time.Unix(unix, 0).UTC()}
		st.Connected = st.State == "CONNECTED"
		if len(f) > 3 {
			st.TunnelAddress = f[3]
		}
		if len(f) > 4 {
			st.RemoteAddress = f[4]
		}
		if len(f) > 5 {
			st.RemotePort, _ = strconv.Atoi(f[5])
		}
	}
	return st
}

// ParseStatus reads the bytes that crossed the transport from the answer
// to `status 2`. They move under the kernel's data channel too.
func ParseStatus(lines []string) (in, out uint64) {
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		switch key {
		case "TCP/UDP read bytes":
			in, _ = strconv.ParseUint(value, 10, 64)
		case "TCP/UDP write bytes":
			out, _ = strconv.ParseUint(value, 10, 64)
		}
	}
	return in, out
}

// DataChannel is "kernel" when the device is an ovpn one, "userspace" when
// it is a tun, and empty when there is no device.
func (c *Client) DataChannel(name string) string {
	kind, err := c.linkKind(name)
	if err != nil {
		return ""
	}
	switch kind {
	case "ovpn":
		return "kernel"
	case "tun":
		return "userspace"
	}
	return ""
}

func (c *Client) linkKind(name string) (string, error) {
	if c.LinkKind != nil {
		return c.LinkKind(name)
	}
	l, err := netlink.LinkByName(name)
	if err != nil {
		return "", err
	}
	return l.Kind, nil
}

// Unit is the instance of the templated unit that runs a tunnel.
func Unit(name string) string { return "ostiole-openvpn@" + name + ".service" }

// Note is the newest journal line of the tunnel's unit about the data
// channel offload: why it stayed in userspace. Empty when there is none,
// or no journal to ask.
func (c *Client) Note(ctx context.Context, name string) string {
	out, err := c.runner().Run(ctx, "journalctl", "-u", Unit(name), "-n", "200", "-o", "cat", "--no-pager")
	if err != nil && len(out) == 0 {
		return ""
	}
	return LastNote(string(out))
}

// LastNote picks the newest line about the data channel offload.
func LastNote(journal string) string {
	lines := strings.Split(journal, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.Contains(strings.ToLower(l), "data channel offload") {
			return l
		}
	}
	return ""
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// SecureBootSkipped reports what the install says when it left the
// kernel's data channel out: Secure Boot is on, and no ovpn module is
// installed for the running kernel.
func SecureBootSkipped() bool { return secureBootSkipped("/") }

func secureBootSkipped(root string) bool {
	vars, _ := filepath.Glob(filepath.Join(root, "sys/firmware/efi/efivars/SecureBoot-*"))
	on := false
	for _, v := range vars {
		// Four bytes of attributes, then the value.
		if raw, err := os.ReadFile(v); err == nil && len(raw) > 4 && raw[4] == 1 {
			on = true
		}
	}
	if !on {
		return false
	}
	if _, err := os.Stat(filepath.Join(root, "sys/module/ovpn")); err == nil {
		return false
	}
	release, err := os.ReadFile(filepath.Join(root, "proc/sys/kernel/osrelease"))
	if err != nil {
		return false
	}
	mods := filepath.Join(root, "lib/modules", strings.TrimSpace(string(release)))
	for _, dir := range []string{"kernel/drivers/net/ovpn", "extra/ovpn", "weak-updates/ovpn"} {
		if _, err := os.Stat(filepath.Join(mods, dir)); err == nil { //nolint:gosec // the running kernel names the directory
			return false
		}
	}
	return true
}
