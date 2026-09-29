package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/wg"
)

func newWireGuardServer(t *testing.T, cfg *model.Config, dev wg.Device) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st := store.New(dir)
	if _, err := st.Save(cfg, "table inet ostiole {}\n"); err != nil {
		t.Fatal(err)
	}
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{
		Engine:    engine.New(st, &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler)),
		Auth:      as,
		WireGuard: dev,
	}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv
}

// Each enabled tunnel of the running configuration comes back, its peers
// named from it. A tunnel whose device is not there is down; a device that
// cannot be read is a 503.
func TestWireGuardStatus(t *testing.T) {
	t.Parallel()
	raw := make([]byte, 32)
	raw[0] = 1
	key := base64.StdEncoding.EncodeToString(raw)
	tunnel := func(name, addr string, enabled bool, peers ...model.WireGuardPeer) model.Interface {
		private, err := wg.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		return model.Interface{Name: name, Zone: "vpn", Enabled: enabled,
			IPv4:      model.IPv4{Mode: model.AddrStatic, Address: addr},
			IPv6:      model.IPv6{Mode: model.AddrNone},
			WireGuard: &model.WireGuard{PrivateKey: private, ListenPort: 51820, Peers: peers}}
	}
	cfg := &model.Config{
		Version: model.SchemaVersion,
		System:  model.System{Hostname: "fw", Management: model.Management{WebPort: 443, SSHPort: 22}},
		Zones:   []model.Zone{{Name: "lan", AntiLockout: true}, {Name: "vpn"}},
		Interfaces: []model.Interface{
			{Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.1.1/24"},
				IPv6: model.IPv6{Mode: model.AddrNone}},
			tunnel("wg0", "10.66.0.1/24", true,
				model.WireGuardPeer{Name: "laptop", Enabled: true, PublicKey: key, AllowedIPs: []string{"10.66.0.2/32"}}),
			tunnel("wg1", "10.67.0.1/24", true),
			tunnel("wg2", "10.68.0.1/24", false),
		},
		NAT: model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
	}
	var refuse error
	srv := newWireGuardServer(t, cfg, func(name string) (netlink.WireGuardDevice, bool, error) {
		if refuse != nil {
			return netlink.WireGuardDevice{}, false, refuse
		}
		if name != "wg0" {
			return netlink.WireGuardDevice{}, false, nil
		}
		return netlink.WireGuardDevice{Name: "wg0", ListenPort: 51820, Peers: []netlink.WireGuardPeer{
			{PublicKey: raw, Endpoint: netip.MustParseAddrPort("198.51.100.7:40000"), RxBytes: 5, TxBytes: 6},
		}}, true, nil
	})

	resp, body := do(t, srv, http.MethodGet, "/api/v1/wireguard/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	var got []wg.Tunnel
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "wg0" || !got[0].Up || got[1].Name != "wg1" || got[1].Up {
		t.Fatalf("tunnels = %+v, want wg0 up and wg1 down", got)
	}
	if p := got[0].Peers; len(p) != 1 || p[0].Name != "laptop" || p[0].Endpoint != "198.51.100.7:40000" || p[0].RxBytes != 5 {
		t.Errorf("peers = %+v", p)
	}

	refuse = errors.New("operation not permitted")
	if resp, body := do(t, srv, http.MethodGet, "/api/v1/wireguard/status", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unreadable device: %d %s, want 503", resp.StatusCode, body)
	}
}
