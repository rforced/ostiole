package wg

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/netlink"
)

// Each tunnel is read by name: one with no device is down, a peer is named
// by its key, and a key the configuration does not hold keeps no name.
func TestStatusNamesPeersByTheirKeys(t *testing.T) {
	t.Parallel()
	laptop, stranger := make([]byte, KeyLen), make([]byte, KeyLen)
	laptop[0], stranger[0] = 1, 2
	shook := time.Unix(1790000000, 0)
	read := func(name string) (netlink.WireGuardDevice, bool, error) {
		if name != "wg0" {
			return netlink.WireGuardDevice{}, false, nil
		}
		return netlink.WireGuardDevice{Name: "wg0", ListenPort: 51820, Peers: []netlink.WireGuardPeer{
			{PublicKey: laptop, Endpoint: netip.MustParseAddrPort("198.51.100.7:40000"), LastHandshake: shook, RxBytes: 10, TxBytes: 20},
			{PublicKey: stranger},
		}}, true, nil
	}
	laptopKey := base64.StdEncoding.EncodeToString(laptop)
	got, err := Status([]Configured{
		{Name: "wg0", Peers: map[string]string{laptopKey: "laptop"}},
		{Name: "wg1"},
	}, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Up || got[0].ListenPort != 51820 || got[1].Up || got[1].Peers == nil {
		t.Fatalf("tunnels = %+v", got)
	}
	p := got[0].Peers[0]
	if p.Name != "laptop" || p.PublicKey != laptopKey || p.Endpoint != "198.51.100.7:40000" ||
		!p.LastHandshake.Equal(shook) || p.RxBytes != 10 || p.TxBytes != 20 {
		t.Errorf("laptop = %+v", p)
	}
	if p := got[0].Peers[1]; p.Name != "" || p.Endpoint != "" || !p.LastHandshake.IsZero() {
		t.Errorf("stranger = %+v", p)
	}

	refused := errors.New("operation not permitted")
	if _, err := Status([]Configured{{Name: "wg0"}}, func(string) (netlink.WireGuardDevice, bool, error) {
		return netlink.WireGuardDevice{}, false, refused
	}); !errors.Is(err, refused) {
		t.Errorf("err = %v, want the device's error", err)
	}
}
