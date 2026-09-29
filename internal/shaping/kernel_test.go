package shaping

import (
	"net"
	"testing"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netnstest"
	"github.com/rforced/ostiole/internal/testenv"
)

// The production Kernel against a real one: the helper device is made
// once and brought up, read back with what is queued on it, refused where
// somebody else's device has the name, and deleted, twice without fuss.
func TestNetlinkKeepsTheHelperDevice(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	k := Netlink{}
	name := IFBName("lan0")
	if ok, err := k.LinkExists(name); err != nil || ok {
		t.Fatalf("before: exists %v, %v", ok, err)
	}
	if err := k.EnsureIFB(name); err != nil {
		testenv.Unavailable(t, "no ifb here: %v", err)
	}
	if err := k.EnsureIFB(name); err != nil {
		t.Errorf("a second ensure: %v", err)
	}
	if ok, err := k.LinkExists(name); err != nil || !ok {
		t.Errorf("after: exists %v, %v", ok, err)
	}
	if l := netnstest.Link(t, name); l.Kind != "ifb" || l.Flags&net.FlagUp == 0 {
		t.Errorf("%s = %+v, want an ifb that is up", name, l)
	}

	qs, err := k.Qdiscs(name)
	if err != nil || len(qs) == 0 {
		t.Fatalf("qdiscs = %+v, %v", qs, err)
	}
	for _, q := range qs {
		if q.Ours() || q.IsIngress() {
			t.Errorf("%+v is taken for one of ours", q)
		}
	}
	if qs, err := k.Qdiscs("nothere0"); err != nil || qs != nil {
		t.Errorf("a missing device's qdiscs = %+v, %v", qs, err)
	}
	// Without Ostiole's root on it, the helper is not counted as one.
	if ifbs, err := k.IFBs(); err != nil || len(ifbs) != 0 {
		t.Errorf("helpers = %v, %v", ifbs, err)
	}

	taken := model.IFBName("lan1")
	netnstest.Dummy(t, taken)
	if err := k.EnsureIFB(taken); err == nil {
		t.Errorf("%s, a dummy, was taken for an ifb", taken)
	}

	for range 2 {
		if err := k.DeleteLink(name); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if ok, err := k.LinkExists(name); err != nil || ok {
		t.Errorf("after deleting: exists %v, %v", ok, err)
	}
}
