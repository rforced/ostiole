package diag

import (
	"net"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/netnstest"
)

func TestAddressMatchesAddressesAndPrefixes(t *testing.T) {
	t.Parallel()
	if !addressMatches("10.0.0.5", "192.168.1.1", "10.0.0.5") {
		t.Error("an exact address should match")
	}
	if !addressMatches("10.0.0.0/8", "10.9.9.9") {
		t.Error("a prefix should match an address inside it")
	}
	if addressMatches("10.0.0.0/8", "192.168.1.1") {
		t.Error("a prefix matched an address outside it")
	}
	if !addressMatches("2001:db8::1", "2001:DB8::1") {
		t.Error("address comparison should ignore case")
	}
	if addressMatches("not-an-address", "10.0.0.1") {
		t.Error("rubbish matched something")
	}
}

func TestFilterKeepsWhatItShould(t *testing.T) {
	t.Parallel()
	s := State{
		Protocol: "tcp", Source: "192.168.1.50", SourcePort: 51000,
		Destination: "203.0.113.10", DestPort: 443,
		ReplySource: "203.0.113.10", ReplyDest: "198.51.100.1", ReplyDestPort: 51000,
	}
	cases := map[string]struct {
		opts StatesOptions
		want bool
	}{
		"no filter":       {StatesOptions{}, true},
		"protocol":        {StatesOptions{Protocol: "tcp"}, true},
		"wrong protocol":  {StatesOptions{Protocol: "udp"}, false},
		"port":            {StatesOptions{Port: 443}, true},
		"source port":     {StatesOptions{Port: 51000}, true},
		"wrong port":      {StatesOptions{Port: 80}, false},
		"source address":  {StatesOptions{Address: "192.168.1.50"}, true},
		"lan prefix":      {StatesOptions{Address: "192.168.1.0/24"}, true},
		"the nat address": {StatesOptions{Address: "198.51.100.1"}, true},
		"elsewhere":       {StatesOptions{Address: "10.0.0.1"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := matches(s, tc.opts); got != tc.want {
				t.Errorf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// A translated connection is the one worth spotting in the table, so the
// NAT flag has to be right.
func TestStateOfSpotsTranslation(t *testing.T) {
	t.Parallel()
	plain := &netlink.ConntrackFlow{
		Forward: netlink.IPTuple{SrcIP: net.ParseIP("10.0.0.2"), DstIP: net.ParseIP("1.1.1.1"), Protocol: 6, SrcPort: 5000, DstPort: 53},
		Reverse: netlink.IPTuple{SrcIP: net.ParseIP("1.1.1.1"), DstIP: net.ParseIP("10.0.0.2"), Protocol: 6, SrcPort: 53, DstPort: 5000},
	}
	if stateOf(plain).NAT {
		t.Error("an untranslated connection was reported as NAT")
	}
	translated := &netlink.ConntrackFlow{
		Forward: netlink.IPTuple{SrcIP: net.ParseIP("10.0.0.2"), DstIP: net.ParseIP("1.1.1.1"), Protocol: 6},
		Reverse: netlink.IPTuple{SrcIP: net.ParseIP("1.1.1.1"), DstIP: net.ParseIP("203.0.113.9"), Protocol: 6},
	}
	got := stateOf(translated)
	if !got.NAT {
		t.Error("a masqueraded connection was not reported as NAT")
	}
	if got.ReplyDest != "203.0.113.9" {
		t.Errorf("reply destination = %q, which is where the NAT shows", got.ReplyDest)
	}
	if got.Protocol != "tcp" {
		t.Errorf("protocol = %q", got.Protocol)
	}
}

func TestTCPStateNames(t *testing.T) {
	t.Parallel()
	if got := tcpStateName(3); got != "ESTABLISHED" {
		t.Errorf("state 3 = %q", got)
	}
	if got := tcpStateName(200); got != "state-200" {
		t.Errorf("an unknown state = %q", got)
	}
}

func TestNeighbourStateNames(t *testing.T) {
	t.Parallel()
	if got := neighStateName(netlink.NUD_REACHABLE); got != "REACHABLE" {
		t.Errorf("state = %q", got)
	}
	// The kernel's states are a bitmask and can be combined.
	if got := neighStateName(netlink.NUD_STALE | netlink.NUD_PERMANENT); got != "STALE+PERMANENT" {
		t.Errorf("combined = %q", got)
	}
	if got := neighStateName(0); got != "NONE" {
		t.Errorf("zero = %q", got)
	}
}

// Reading the real ARP table needs a kernel, so this runs in a namespace
// where an entry can be created without touching the host.
func TestNeighboursReadsTheKernel(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}

	link := netnstest.Dummy(t, "lan0", "192.168.77.1/24")
	mac, _ := net.ParseMAC("02:00:00:00:00:01")
	if err := netlink.NeighAdd(&netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       netlink.FAMILY_V4,
		State:        netlink.NUD_PERMANENT,
		IP:           net.ParseIP("192.168.77.50"),
		HardwareAddr: mac,
	}); err != nil {
		t.Fatalf("add neighbour: %v", err)
	}

	// An entry that says the neighbour just answered.
	if err := netlink.NeighAdd(&netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       netlink.FAMILY_V4,
		State:        netlink.NUD_REACHABLE,
		IP:           net.ParseIP("192.168.77.51"),
		HardwareAddr: mac,
	}); err != nil {
		t.Fatalf("add neighbour: %v", err)
	}

	before := time.Now()
	got, err := Neighbours()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]Neighbour{}
	for _, n := range got {
		found[n.Address] = n
	}
	n, ok := found["192.168.77.50"]
	if !ok {
		t.Fatalf("the entry that was just added is not in the table: %+v", got)
	}
	if n.MAC != "02:00:00:00:00:01" {
		t.Errorf("mac = %q", n.MAC)
	}
	if n.Interface != "lan0" {
		t.Errorf("interface = %q", n.Interface)
	}
	if n.Family != "IPv4" || n.State != "PERMANENT" {
		t.Errorf("entry = %+v", n)
	}
	// Typed in, so nothing was heard from it.
	if !n.Seen.IsZero() {
		t.Errorf("a permanent entry was seen at %v", n.Seen)
	}
	r, ok := found["192.168.77.51"]
	if !ok || r.State != "REACHABLE" {
		t.Fatalf("reachable entry = %+v", r)
	}
	if r.Seen.Before(before.Add(-5*time.Second)) || r.Seen.After(time.Now()) {
		t.Errorf("seen at %v, want just now (%v)", r.Seen, before)
	}
}

func TestNeighbourAgeIsInClockTicks(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 25, 18, 0, 0, 0, time.UTC)
	if got := ago(now, 150); !got.Equal(now.Add(-1500 * time.Millisecond)) {
		t.Errorf("150 ticks = %v", got)
	}
	// The kernel's u32 at its largest is 497 days back, not a wrap.
	if got := ago(now, 1<<32-1); !got.Before(now.Add(-497 * 24 * time.Hour)) {
		t.Errorf("max ticks = %v", got)
	}
}
