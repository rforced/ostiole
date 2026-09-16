package diag

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

type fakePinger struct {
	fail map[int]bool
	sent int
}

func (f *fakePinger) Probe(_ context.Context, _, _ string, _ time.Duration) (time.Duration, error) {
	f.sent++
	if f.fail[f.sent] {
		return 0, errors.New("timeout")
	}
	return time.Duration(f.sent) * time.Millisecond, nil
}

func TestPingSummarises(t *testing.T) {
	t.Parallel()
	p := &fakePinger{fail: map[int]bool{2: true}}
	res, err := Ping(context.Background(), p, PingOptions{Target: "10.0.0.1", Count: 4, Interval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 4 || res.Received != 3 {
		t.Fatalf("result = %+v", res)
	}
	if res.LossPercent != 25 {
		t.Errorf("loss = %v, want 25", res.LossPercent)
	}
	if res.MinMS != 1 || res.MaxMS != 4 {
		t.Errorf("min/max = %v/%v", res.MinMS, res.MaxMS)
	}
	if res.AvgMS <= res.MinMS || res.AvgMS >= res.MaxMS {
		t.Errorf("avg = %v, want between min and max", res.AvgMS)
	}
	if res.Probes[1].Error == "" {
		t.Errorf("lost probe = %+v, want an error", res.Probes[1])
	}
}

func TestPingRejectsUnresolvableTargets(t *testing.T) {
	t.Parallel()
	if _, err := Ping(context.Background(), &fakePinger{}, PingOptions{Target: ""}); err == nil {
		t.Error("empty target accepted")
	}
	_, err := Ping(context.Background(), &fakePinger{}, PingOptions{
		Target: "ostiole-does-not-exist.invalid", Count: 1,
	})
	if err == nil {
		t.Error("unresolvable name accepted")
	}
}

func TestParseJournalReadsBothMessageForms(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"__REALTIME_TIMESTAMP":"1758000000000000","_SYSTEMD_UNIT":"ostiole.service","PRIORITY":"6","MESSAGE":"listening","_HOSTNAME":"fw"}
{"__REALTIME_TIMESTAMP":"1758000001000000","_COMM":"kernel","PRIORITY":"3","MESSAGE":[104,105]}
not json
`)
	entries := parseJournal(raw)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Message != "listening" || entries[0].Unit != "ostiole.service" || entries[0].Priority != 6 {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[0].Time.Unix() != 1758000000 {
		t.Errorf("time = %v", entries[0].Time)
	}
	// A message that is not valid UTF-8 arrives as a byte array.
	if entries[1].Message != "hi" || entries[1].Unit != "kernel" {
		t.Errorf("entry 1 = %+v", entries[1])
	}
}

func TestJournalRejectsInjectedArguments(t *testing.T) {
	t.Parallel()
	_, err := Journal(context.Background(), JournalOptions{Unit: "ostiole.service; rm -rf /"})
	if err == nil {
		t.Fatal("dangerous unit name accepted")
	}
	if _, err := Journal(context.Background(), JournalOptions{Since: "`reboot`"}); err == nil {
		t.Fatal("dangerous since accepted")
	}
}

// frame builds a minimal Ethernet/IPv4/UDP packet for the filter tests.
func frame(src, dst string, sport, dport int) []byte {
	b := make([]byte, 14+20+8)
	binary.BigEndian.PutUint16(b[12:14], 0x0800)
	ip := b[14:]
	ip[0] = 0x45
	ip[9] = 17
	copy(ip[12:16], net.ParseIP(src).To4())
	copy(ip[16:20], net.ParseIP(dst).To4())
	udp := ip[20:]
	binary.BigEndian.PutUint16(udp[0:2], uint16(sport))
	binary.BigEndian.PutUint16(udp[2:4], uint16(dport))
	return b
}

func TestCaptureFilter(t *testing.T) {
	t.Parallel()
	pkt := frame("10.0.0.1", "10.0.0.2", 12345, 53)
	cases := []struct {
		name string
		opts CaptureOptions
		want bool
	}{
		{"no filter", CaptureOptions{}, true},
		{"matching source", CaptureOptions{Address: "10.0.0.1"}, true},
		{"matching destination", CaptureOptions{Address: "10.0.0.2"}, true},
		{"other address", CaptureOptions{Address: "10.0.0.3"}, false},
		{"matching port", CaptureOptions{Port: 53}, true},
		{"source port", CaptureOptions{Port: 12345}, true},
		{"other port", CaptureOptions{Port: 80}, false},
		{"address and port", CaptureOptions{Address: "10.0.0.1", Port: 53}, true},
		{"address with wrong port", CaptureOptions{Address: "10.0.0.1", Port: 80}, false},
	}
	for _, c := range cases {
		if got := keep(pkt, c.opts); got != c.want {
			t.Errorf("%s: keep = %v, want %v", c.name, got, c.want)
		}
	}
	// A truncated frame is not kept when a filter is set.
	if keep([]byte{1, 2, 3}, CaptureOptions{Address: "10.0.0.1"}) {
		t.Error("truncated frame passed the filter")
	}
}

func TestPcapFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writePcapHeader(&buf, 100); err != nil {
		t.Fatal(err)
	}
	pkt := frame("10.0.0.1", "10.0.0.2", 1, 2)
	if err := writePcapPacket(&buf, pkt, 20, time.Unix(1758000000, 123456000)); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if got := binary.LittleEndian.Uint32(out[0:4]); got != pcapMagic {
		t.Errorf("magic = %x", got)
	}
	// Two 16-bit fields: tcpdump rejects a file that claims version 4.2.
	major, minor := binary.LittleEndian.Uint16(out[4:6]), binary.LittleEndian.Uint16(out[6:8])
	if major != 2 || minor != 4 {
		t.Errorf("version = %d.%d, want 2.4", major, minor)
	}
	if got := binary.LittleEndian.Uint32(out[20:24]); got != linkTypeEth {
		t.Errorf("link type = %d", got)
	}
	rec := out[24:40]
	if got := binary.LittleEndian.Uint32(rec[0:4]); got != 1758000000 {
		t.Errorf("timestamp = %d", got)
	}
	if got := binary.LittleEndian.Uint32(rec[4:8]); got != 123456 {
		t.Errorf("microseconds = %d", got)
	}
	// Snaplen truncates the stored bytes but the original length is kept.
	if got := binary.LittleEndian.Uint32(rec[8:12]); got != 20 {
		t.Errorf("captured length = %d, want the snaplen", got)
	}
	if got := binary.LittleEndian.Uint32(rec[12:16]); got != uint32(len(pkt)) {
		t.Errorf("original length = %d, want %d", got, len(pkt))
	}
	if len(out) != 24+16+20 {
		t.Errorf("total length = %d", len(out))
	}
}

// TestCaptureAndTraceInNamespace exercises the real sockets inside an
// unprivileged network namespace, where this process has the raw socket
// capability without being root on the host.
func TestCaptureAndTraceInNamespace(t *testing.T) {
	if os.Getenv("OSTIOLE_DIAG_NETNS") == "" {
		runInNamespace(t, "OSTIOLE_DIAG_NETNS", "TestCaptureAndTraceInNamespace")
		return
	}

	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatalf("lo: %v", err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		t.Fatalf("bring lo up: %v", err)
	}

	// Traceroute to the loopback finishes in one hop.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Traceroute(ctx, TraceOptions{Target: "127.0.0.1", MaxHops: 3, Timeout: time.Second})
	if err != nil {
		t.Fatalf("traceroute: %v", err)
	}
	if !res.Complete || len(res.Hops) != 1 || !res.Hops[0].Final {
		t.Fatalf("hops = %+v", res.Hops)
	}

	// A capture on the loopback records the traffic a ping makes. The
	// loopback has no Ethernet header, so only the packet count is checked.
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(100 * time.Millisecond)
		conn, err := net.Dial("udp", "127.0.0.1:9999")
		if err == nil {
			for i := 0; i < 3; i++ {
				_, _ = conn.Write([]byte("ostiole"))
				time.Sleep(20 * time.Millisecond)
			}
			_ = conn.Close()
		}
	}()
	n, err := Capture(ctx, &buf, CaptureOptions{Interface: "lo", Count: 2, Duration: 3 * time.Second})
	<-done
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if n == 0 {
		t.Fatal("captured nothing")
	}
	if buf.Len() <= 24 {
		t.Fatalf("pcap has only a header: %d bytes", buf.Len())
	}
	if got := binary.LittleEndian.Uint32(buf.Bytes()[0:4]); got != pcapMagic {
		t.Errorf("magic = %x", got)
	}
}

// runInNamespace re-runs the calling test inside a fresh unprivileged user
// and network namespace, where it has the network capabilities without
// being root. It skips when the sandbox forbids that, which is what
// GitHub's runners do.
func runInNamespace(t *testing.T, env, name string) {
	t.Helper()
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not installed")
	}
	cmd := exec.Command("unshare", "-Urn", os.Args[0], "-test.run", "^"+name+"$", "-test.v")
	cmd.Env = append(os.Environ(), env+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	if strings.Contains(string(out), "uid_map") || strings.Contains(string(out), "Operation not permitted") {
		t.Skipf("unprivileged namespaces are not allowed here: %s", strings.TrimSpace(string(out)))
	}
	t.Fatalf("inside namespace: %v\n%s", err, out)
}
