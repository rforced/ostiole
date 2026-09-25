package network

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/netnstest"
)

func addDefault(t *testing.T, link netlink.Link, gw string, metric, proto int) {
	t.Helper()
	err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.ParseIP(gw),
		Priority:  metric,
		Protocol:  netlink.RouteProtocol(proto),
	})
	if err != nil {
		t.Fatalf("add default via %s metric %d: %v", gw, metric, err)
	}
}

// metrics lists the metrics of the default routes the kernel holds, so a
// test can say what survived without repeating the lookup.
func metrics(t *testing.T) []int {
	t.Helper()
	routes, err := defaultRoutes()
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, r := range routes {
		out = append(out, r.Metric)
	}
	return out
}

func same(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSweepStaleTakesOnlyTheReplacedRoute drives the sweep against a real
// kernel: the leftover DHCP route goes once the restored manager has one
// of its own, and not before.
func TestSweepStaleTakesOnlyTheReplacedRoute(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	ctx := context.Background()
	k := KernelRoutes{}
	link := netnstest.Dummy(t, "wan0", "203.0.113.2/24")

	// What networkd leaves behind: a DHCP default at its own metric.
	addDefault(t, link, "203.0.113.1", 1024, unix.RTPROT_DHCP)
	before, err := k.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("before = %v, want one route", before)
	}

	// Nobody has taken the link over yet, so the sweep must not touch it.
	removed, err := k.SweepStale(ctx, before, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v with no replacement in the kernel", removed)
	}
	if got := metrics(t); !same(got, []int{1024}) {
		t.Fatalf("metrics = %v, want the leftover route still there", got)
	}

	// NetworkManager comes back and installs its own default.
	addDefault(t, link, "203.0.113.1", 100, unix.RTPROT_DHCP)
	removed, err = k.SweepStale(ctx, before, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Metric != 1024 {
		t.Fatalf("removed = %v, want the metric 1024 route", removed)
	}
	if got := metrics(t); !same(got, []int{100}) {
		t.Fatalf("metrics = %v, want only the restored manager's route", got)
	}
}

// TestSweepStaleLeavesStaticRoutes proves the sweep is limited to what a
// lease or an advertisement installed. A static default route on a link
// somebody else has taken over is still somebody's decision.
func TestSweepStaleLeavesStaticRoutes(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	k := KernelRoutes{}
	link := netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	addDefault(t, link, "203.0.113.1", 900, unix.RTPROT_STATIC)
	before, err := k.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	addDefault(t, link, "203.0.113.1", 50, unix.RTPROT_DHCP)

	removed, err := k.SweepStale(context.Background(), before, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v, want the static route left alone", removed)
	}
	if got := metrics(t); !same(got, []int{50, 900}) {
		t.Fatalf("metrics = %v, want both routes", got)
	}
}

// TestSweepStaleIgnoresAnotherLink pins the link check: a replacement on
// one interface says nothing about a leftover on a different one.
func TestSweepStaleIgnoresAnotherLink(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	k := KernelRoutes{}
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	wan1 := netnstest.Dummy(t, "wan1", "198.51.100.2/24")
	addDefault(t, wan0, "203.0.113.1", 1024, unix.RTPROT_DHCP)
	before, err := k.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	addDefault(t, wan1, "198.51.100.1", 100, unix.RTPROT_DHCP)

	removed, err := k.SweepStale(context.Background(), before, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("removed %v for a replacement on another link", removed)
	}
	if got := metrics(t); !same(got, []int{100, 1024}) {
		t.Fatalf("metrics = %v, want both routes", got)
	}
}
