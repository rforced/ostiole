package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"ostiole/internal/engine"
	"ostiole/internal/install"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/openvpn"
	"ostiole/internal/store"
)

type fakeTunnels struct{ asked []string }

func (f *fakeTunnels) Status(_ context.Context, name string) (openvpn.State, error) {
	f.asked = append(f.asked, name)
	if name != "ovpn0" {
		return openvpn.State{}, errors.New("no socket")
	}
	return openvpn.State{Connected: true, State: "CONNECTED", Since: time.Unix(1760216000, 0).UTC(),
		TunnelAddress: "10.8.0.2", RemoteAddress: "198.51.100.9", RemotePort: 1194, BytesIn: 4721, BytesOut: 3958}, nil
}

func (f *fakeTunnels) DataChannel(name string) string {
	if name == "ovpn0" {
		return "userspace"
	}
	return ""
}

func (f *fakeTunnels) Note(context.Context, string) string {
	return "Note: Kernel support for ovpn interfaces missing, disabling data channel offload."
}

func openvpnEngine(t *testing.T) *engine.Engine {
	t.Helper()
	tunnel := func(name string, enabled bool) model.Interface {
		return model.Interface{Name: name, Zone: "vpnout", Enabled: enabled,
			IPv4: model.IPv4{Mode: model.AddrNone}, IPv6: model.IPv6{Mode: model.AddrNone},
			OpenVPN: &model.OpenVPN{Remotes: []model.OpenVPNRemote{{Host: "vpn.example", Port: 1194}},
				CAPEM: "-----BEGIN CERTIFICATE-----\nbGFi\n-----END CERTIFICATE-----\n", Password: "never shown", Username: "lab-user"}}
	}
	cfg := &model.Config{
		Version:    model.SchemaVersion,
		System:     model.System{Hostname: "fw", Management: model.Management{WebPort: 443, SSHPort: 22}},
		Zones:      []model.Zone{{Name: "vpnout", External: true}},
		Interfaces: []model.Interface{tunnel("ovpn1", false), tunnel("ovpn0", true)},
		NAT:        model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
	}
	st := store.New(t.TempDir())
	if _, err := st.Save(cfg, "table inet ostiole {}\n", store.Author{}); err != nil {
		t.Fatal(err)
	}
	return engine.New(st, &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
}

func TestOpenVPNStatus(t *testing.T) {
	t.Parallel()
	reader := &fakeTunnels{}
	a := &api{
		engine: openvpnEngine(t),
		units: fakeUnits{state: map[string][2]string{
			"ostiole-openvpn@.service":      {"", "static"},
			"ostiole-openvpn@ovpn0.service": {"active", "enabled"},
		}},
		ovpn:              reader,
		secureBootSkipped: func() bool { return true },
	}
	got := a.readOpenVPN(context.Background())
	if !got.SetUp || !got.SecureBootSkipped || len(got.Tunnels) != 2 {
		t.Fatalf("status = %+v", got)
	}
	up, off := got.Tunnels[0], got.Tunnels[1]
	if up.Name != "ovpn0" || !up.SetUp || !up.Running || !up.Connected || up.State != "CONNECTED" ||
		up.Remote != "198.51.100.9:1194" || up.TunnelAddress != "10.8.0.2" || up.DataChannel != "userspace" ||
		up.BytesIn != 4721 || up.BytesOut != 3958 || up.Since == nil || !strings.Contains(up.Note, "data channel offload") {
		t.Errorf("ovpn0 = %+v", up)
	}
	if off.Name != "ovpn1" || !off.SetUp || off.Running || off.State != "" || off.Note != "" {
		t.Errorf("ovpn1 = %+v", off)
	}
	// A stopped tunnel's socket is not knocked on.
	if !slices.Equal(reader.asked, []string{"ovpn0"}) {
		t.Errorf("asked %v", reader.asked)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "never shown") {
		t.Errorf("the status carries a secret: %s", raw)
	}
}

// Without the unit nothing is asked, and nothing is an error.
func TestOpenVPNStatusNotSetUp(t *testing.T) {
	t.Parallel()
	reader := &fakeTunnels{}
	a := &api{engine: openvpnEngine(t), units: fakeUnits{state: map[string][2]string{}}, ovpn: reader,
		secureBootSkipped: func() bool { t.Error("Secure Boot was asked about with no unit"); return true }}
	got := a.readOpenVPN(context.Background())
	if got.SetUp || got.SecureBootSkipped || len(got.Tunnels) != 2 || got.Tunnels[0].SetUp || got.Tunnels[0].Running {
		t.Errorf("status = %+v", got)
	}
	if len(reader.asked) != 0 {
		t.Errorf("asked %v", reader.asked)
	}

	srv := newTestServerWith(t, func(*Deps) {})
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/openvpn/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"tunnels":[]`) || !strings.Contains(string(raw), `"setUp":false`) {
		t.Errorf("empty status = %s", raw)
	}
}

func TestOpenVPNTiles(t *testing.T) {
	t.Parallel()
	a := &api{}
	cfg := openvpnEngine(t).Effective()
	units := install.ReadUnits(context.Background(), fakeUnits{state: map[string][2]string{
		"ostiole-openvpn@.service":      {"", "static"},
		"ostiole-openvpn@ovpn0.service": {"failed", "enabled"},
	}}, "ostiole-openvpn@.service", "ostiole-openvpn@ovpn0.service", "ostiole-openvpn@ovpn1.service")
	var tiles []ServiceState
	for _, s := range a.serviceStates(context.Background(), cfg, units) {
		if strings.HasPrefix(s.Name, "OpenVPN") {
			tiles = append(tiles, s)
		}
	}
	if len(tiles) != 2 || tiles[0].Unit != "ostiole-openvpn@ovpn0.service" || !tiles[0].Want || tiles[0].State != stateInactive ||
		tiles[1].Want {
		t.Errorf("tiles = %+v", tiles)
	}
	ws := a.warnings(context.Background(), cfg, engine.Status{}, a.serviceStates(context.Background(), cfg, units),
		[]LinkSummary{{Name: "ovpn0", Configured: true, Enabled: true}}, units)
	if w := warningIn(ws, "interface-missing"); w != nil {
		t.Errorf("a tunnel's own device is reported missing: %+v", w)
	}
	if w := warningIn(ws, "service-down"); w == nil || !strings.Contains(w.Title, "OpenVPN ovpn0") {
		t.Errorf("no service-down for the failed tunnel: %+v", ws)
	}
}

func warningIn(ws []Warning, kind string) *Warning {
	for i := range ws {
		if ws[i].Kind == kind {
			return &ws[i]
		}
	}
	return nil
}
