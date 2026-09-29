package fwlog

import (
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
)

// Ostiole's own drops log no zone in their prefix, so the listener names
// the zone of the interface the packet arrived on. Everything else keeps
// what its prefix says.
func TestTheListenerNamesTheZoneOfASystemDrop(t *testing.T) {
	t.Parallel()
	zones := &Zones{}
	zones.Set(&model.Config{Interfaces: []model.Interface{
		{Name: "eth0", Zone: "wan", Enabled: true},
		{Name: "eth1", Zone: "lan", Enabled: true},
		{Name: "eth2", Zone: "iot"},
	}})
	// The interface names come from the listener's cache, not the machine
	// the test runs on.
	l := &Listener{Zones: zones, ifaces: map[int]string{2: "eth0", 3: "eth1", 4: "eth2", 5: "wg0"}, last: time.Now()}

	for _, c := range []struct {
		prefix string
		in     int
		want   string
	}{
		{"ostiole:s:protect-scanner:drop: ", 2, "wan"},
		{"ostiole:s:block-dot:drop: ", 3, "lan"},
		{"ostiole:s:protect-synflood:drop: ", 4, ""},  // disabled
		{"ostiole:s:protect-icmpflood:drop: ", 5, ""}, // in no zone
		{"ostiole:z:guest:drop: ", 2, "guest"},
		{"ostiole:r:web-in:accept: ", 2, ""},
		{"ostiole:c:input:drop: ", 2, ""},
	} {
		if got := l.entry(netlink.LogPacket{Prefix: c.prefix, InDev: c.in}).Zone; got != c.want {
			t.Errorf("%q in on %d: zone %q, want %q", c.prefix, c.in, got, c.want)
		}
	}

	// Before the watcher's first pass there is nothing to name it by.
	bare := &Listener{ifaces: map[int]string{2: "eth0"}, last: time.Now()}
	if got := bare.entry(netlink.LogPacket{Prefix: "ostiole:s:protect-scanner:drop: ", InDev: 2}).Zone; got != "" {
		t.Errorf("zone %q with no zones known", got)
	}
}
