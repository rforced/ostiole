package traffic

import (
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
	"ostiole/internal/testenv"
)

// A WireGuard link refuses a packet no peer is for, an error each with
// nothing moved, and the counter finds them in the kernel's own counters.
func TestAWireGuardLinkWithNoPeerCountsErrors(t *testing.T) {
	if _, err := os.Stat("/sys/module/wireguard"); err != nil {
		testenv.Unavailable(t, "the wireguard module is not loaded")
	}
	if !netnstest.Enter(t) {
		return
	}
	if err := netlink.AddLink("wg9", "wireguard"); err != nil {
		t.Fatal(err)
	}
	wg := netnstest.Link(t, "wg9")
	if err := netlink.AddAddr(wg.Index, netip.MustParsePrefix("192.0.2.1/24")); err != nil {
		t.Fatal(err)
	}
	if err := netlink.SetLinkUp(wg.Index); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	c := &Counter{Source: func() *model.Config { return nil }, Now: func() time.Time { return now }}
	c.sampleLinks(now)
	// A socket each, as a refusal comes back to the sender as an ICMP
	// error that would fail the next write on the same one.
	for range 3 {
		conn, err := net.Dial("udp", "192.0.2.2:9")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write([]byte("x"))
		_ = conn.Close()
	}
	now = now.Add(time.Second)
	c.sampleLinks(now)
	if got := c.LinkErrors(); got["wg9"] != (Errors{TX: 3}) {
		t.Errorf("errors = %+v", got)
	}
	for _, l := range c.LinkReports(Window5m) {
		if l.Name == "wg9" && l.Totals != (Totals{}) {
			t.Errorf("wg9 moved %+v", l.Totals)
		}
	}
}
