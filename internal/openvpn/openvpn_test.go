package openvpn

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Recorded from OpenVPN 2.7.0 on the Ubuntu lab, the client connected
// over DCO.
const (
	recordedState = "1760216000,CONNECTED,SUCCESS,10.200.0.2,10.199.0.2,1194,,\r\nEND\r\n"
	// status 2 answers in comma-separated lines.
	recordedStatus = "TITLE,OpenVPN 2.7.0 x86_64-pc-linux-gnu [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD] [DCO]\r\n" +
		"TIME,2026-10-11 14:13:20,1760216000\r\n" +
		"HEADER,CLIENT_LIST,unused\r\n" +
		"TUN/TAP read bytes,1260\r\n" +
		"TUN/TAP write bytes,840\r\n" +
		"TCP/UDP read bytes,4721\r\n" +
		"TCP/UDP write bytes,3958\r\n" +
		"Auth read bytes,840\r\n" +
		"END\r\n"
)

// serve answers the management commands the way the daemon does, with a
// banner first and a real-time line in the middle.
func serve(t *testing.T, dir, name string, answers map[string]string) {
	t.Helper()
	ln, err := net.Listen("unix", SocketPath(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(">INFO:OpenVPN Management Interface Version 6 -- type 'help' for more info\r\n"))
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			answer, ok := answers[cmd]
			if !ok {
				answer = "ERROR: unknown command [" + cmd + "], enter 'help' for more options\r\n"
			}
			_, _ = conn.Write([]byte(">BYTECOUNT:4721,3958\r\n" + answer))
		}
	}()
}

func TestStatusReadsStateAndCounters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	serve(t, dir, "ovpn0", map[string]string{"state": recordedState, "status 2": recordedStatus})
	st, err := (&Client{Dir: dir}).Status(context.Background(), "ovpn0")
	if err != nil {
		t.Fatal(err)
	}
	want := State{
		Connected: true, State: "CONNECTED", Since: time.Unix(1760216000, 0).UTC(),
		TunnelAddress: "10.200.0.2", RemoteAddress: "10.199.0.2", RemotePort: 1194,
		BytesIn: 4721, BytesOut: 3958,
	}
	if st != want {
		t.Errorf("Status = %+v, want %+v", st, want)
	}
}

func TestStatusReportsAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	serve(t, dir, "ovpn0", map[string]string{"state": recordedState})
	if _, err := (&Client{Dir: dir}).Status(context.Background(), "ovpn0"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("Status = %v, want the daemon's error", err)
	}
}

func TestStatusWithoutASocket(t *testing.T) {
	t.Parallel()
	if _, err := (&Client{Dir: t.TempDir()}).Status(context.Background(), "ovpn0"); err == nil {
		t.Error("no socket gave no error")
	}
}

func TestParseStateWhileConnecting(t *testing.T) {
	t.Parallel()
	st := ParseState([]string{"1760216000,WAIT,,,,,,"})
	if st.Connected || st.State != "WAIT" || st.TunnelAddress != "" || st.RemotePort != 0 {
		t.Errorf("ParseState = %+v", st)
	}
	if st := ParseState(nil); st != (State{}) {
		t.Errorf("no lines = %+v", st)
	}
}

func TestDataChannelFromTheLinkKind(t *testing.T) {
	t.Parallel()
	kinds := map[string]string{"ovpn0": "ovpn", "ovpn1": "tun", "ovpn2": "dummy"}
	c := &Client{LinkKind: func(name string) (string, error) {
		k, ok := kinds[name]
		if !ok {
			return "", errors.New("link not found")
		}
		return k, nil
	}}
	for name, want := range map[string]string{"ovpn0": "kernel", "ovpn1": "userspace", "ovpn2": "", "ovpn3": ""} {
		if got := c.DataChannel(name); got != want {
			t.Errorf("DataChannel(%s) = %q, want %q", name, got, want)
		}
	}
}

type journal struct {
	out  string
	err  error
	args []string
}

func (j *journal) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	j.args = append([]string{name}, args...)
	return []byte(j.out), j.err
}

// The Rocky lab's stock kernel, recorded.
func TestNoteIsTheLastOffloadLine(t *testing.T) {
	t.Parallel()
	j := &journal{out: "OpenVPN 2.7.6 x86_64-redhat-linux-gnu\n" +
		"Note: Kernel support for ovpn interfaces missing, disabling data channel offload. Use Linux 6.16.0 or newer with ovpn support or use ovpn-backports for interface support.\n" +
		"TUN/TAP device ovpn0 opened\n" +
		"Interface ovpn0 exists and is non-DCO. Disabling data channel offload\n" +
		"Initialization Sequence Completed\n"}
	got := (&Client{Run: j}).Note(context.Background(), "ovpn0")
	if got != "Interface ovpn0 exists and is non-DCO. Disabling data channel offload" {
		t.Errorf("Note = %q", got)
	}
	if strings.Join(j.args, " ") != "journalctl -u ostiole-openvpn@ovpn0.service -n 200 -o cat --no-pager" {
		t.Errorf("ran %v", j.args)
	}
	if got := (&Client{Run: &journal{out: "DCO device ovpn0 opened\n"}}).Note(context.Background(), "ovpn0"); got != "" {
		t.Errorf("Note without an offload line = %q", got)
	}
	if got := (&Client{Run: &journal{err: errors.New("executable file not found")}}).Note(context.Background(), "ovpn0"); got != "" {
		t.Errorf("Note without journalctl = %q", got)
	}
}

func TestSecureBootSkipped(t *testing.T) {
	t.Parallel()
	root := func(t *testing.T, secureBoot byte, module string) string {
		t.Helper()
		r := t.TempDir()
		write := func(path string, data []byte) {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(r, path)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(r, path), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c", []byte{6, 0, 0, 0, secureBoot})
		write("proc/sys/kernel/osrelease", []byte("6.12.0-lab\n"))
		if module != "" {
			write(filepath.Join("lib/modules/6.12.0-lab", module, "ovpn.ko"), nil)
		}
		return r
	}
	if !secureBootSkipped(root(t, 1, "")) {
		t.Error("Secure Boot on and no module: not reported")
	}
	if secureBootSkipped(root(t, 0, "")) {
		t.Error("Secure Boot off: reported")
	}
	if secureBootSkipped(root(t, 1, "extra/ovpn")) {
		t.Error("the module is installed: reported")
	}
	if secureBootSkipped(t.TempDir()) {
		t.Error("no EFI at all: reported")
	}
}
