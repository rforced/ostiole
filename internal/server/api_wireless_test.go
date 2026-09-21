package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/wireless"
)

// wifiCmd answers the systemctl and iw calls the wireless routes make.
type wifiCmd struct {
	installed bool
	active    bool
	// journal is what journalctl prints for the radio's instance.
	journal string
}

const (
	wifiDevInfo = `Interface ap0
	ifindex 7
	ssid ostiole-lan
	type AP
	channel 40 (5200 MHz), width: 80 MHz, center1: 5210 MHz
	txpower 22.00 dBm
`
	wifiReg = `global
country 00: DFS-UNSET

phy#0 (self-managed)
country US: DFS-FCC
`
)

func (c wifiCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "journalctl" {
		return []byte(c.journal), nil
	}
	if name == "iw" {
		switch {
		case len(args) >= 2 && args[0] == "reg" && args[1] == "get":
			return []byte(wifiReg), nil
		case len(args) >= 3 && args[0] == "dev" && args[2] == "info":
			return []byte(wifiDevInfo), nil
		case len(args) >= 4 && args[0] == "dev" && args[2] == "station":
			if args[1] != "ap0" {
				return nil, nil
			}
			raw, err := os.ReadFile("../wireless/testdata/station-dump.txt")
			return raw, err
		}
		return nil, nil
	}
	switch {
	case len(args) >= 1 && args[0] == "cat":
		if c.installed {
			return []byte("[Unit]"), nil
		}
		return nil, errors.New("no such unit")
	case len(args) >= 1 && args[0] == "is-active":
		if c.active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit 3")
	}
	return nil, nil
}

func wirelessAPIConfig() *model.Config {
	return &model.Config{
		Version: model.SchemaVersion,
		System:  model.System{Hostname: "fw", Management: model.Management{WebPort: 443, SSHPort: 22}},
		Zones:   []model.Zone{{Name: "lan", AntiLockout: true}},
		Interfaces: []model.Interface{
			{Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.1.1/24"},
				IPv6: model.IPv6{Mode: model.AddrNone}},
			{Name: "ap0", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrNone},
				IPv6: model.IPv6{Mode: model.AddrNone},
				Wireless: &model.WirelessNetwork{
					Radio: "wlp3s0", SSID: "ostiole-lan",
					Security: model.SecurityMixed, Passphrase: "correct horse battery",
				}},
		},
		NAT: model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
		Wireless: model.Wireless{
			Country: "US",
			Radios: []model.Radio{
				{Name: "wlp3s0", Enabled: true, Band: model.Band5G, Channel: 36, Width: 80, Standard: model.StandardAX},
			},
		},
	}
}

func newWirelessServer(t *testing.T, cmd wifiCmd, cfg *model.Config, withHandle bool) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st := store.New(dir)
	if cfg != nil {
		if _, err := st.Save(cfg, "table inet ostiole {}\n"); err != nil {
			t.Fatal(err)
		}
	}
	eng := engine.New(st, &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Engine: eng, Auth: as}
	if withHandle {
		raw, err := os.ReadFile("../wireless/testdata/ax210-phy.txt")
		if err != nil {
			t.Fatal(err)
		}
		phy, err := wireless.ParsePhy(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		phy.Name, phy.Driver = "phy0", "iwlwifi"
		deps.Wireless = &services.Wireless{
			Dir: dir, Cmd: cmd,
			List:  func() []string { return []string{"wlp3s0"} },
			Probe: func(context.Context, string) (*wireless.Phy, error) { return &phy, nil },
		}
	}
	srv := httptest.NewServer(Handler(deps))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv
}

func getRadios(t *testing.T, srv *httptest.Server) wirelessRadios {
	t.Helper()
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/wireless/radios", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("radios: %d %s", resp.StatusCode, raw)
	}
	var got wirelessRadios
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// A router with no hostapd, and the e2e server where every handle is nil,
// answer both routes rather than failing them.
func TestWirelessRoutesAreNeverAnError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		handle bool
		cmd    wifiCmd
	}{
		{"no handle", false, wifiCmd{}},
		{"no unit", true, wifiCmd{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newWirelessServer(t, tc.cmd, wirelessAPIConfig(), tc.handle)
			got := getRadios(t, srv)
			if got.SetUp || len(got.Radios) != 0 {
				t.Errorf("radios = %+v", got)
			}
			resp, raw := do(t, srv, http.MethodGet, "/api/v1/wireless/clients", nil)
			if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
				t.Errorf("clients = %d %s", resp.StatusCode, raw)
			}
		})
	}
}

func TestWirelessRadiosReportTheCard(t *testing.T) {
	t.Parallel()
	srv := newWirelessServer(t, wifiCmd{installed: true}, wirelessAPIConfig(), true)
	got := getRadios(t, srv)
	if !got.SetUp || got.Country != "US" || len(got.Radios) != 1 {
		t.Fatalf("radios = %+v", got)
	}
	card := got.Radios[0]
	if card.Name != "wlp3s0" || card.Driver != "iwlwifi" || card.MaxNetworks != 1 || !card.SelfManaged {
		t.Errorf("card = %+v", card)
	}
	if card.Running != nil {
		t.Errorf("a stopped radio is running: %+v", card.Running)
	}
	five, ok := card.Bands["5g"]
	if !ok || five.MaxWidth != 160 || !five.Serves {
		t.Fatalf("5 GHz = %+v, %v", five, ok)
	}
	if six := card.Bands["6g"]; six.Serves {
		t.Errorf("6 GHz serves on a card that marks every channel no-IR: %+v", six)
	}
	if got := strings.Join(five.Standards, ","); got != "ax,ac,n,legacy" {
		t.Errorf("standards = %q", got)
	}
	var radar bool
	for _, c := range five.Channels {
		if c.Number == 52 {
			radar = c.Radar
		}
	}
	if !radar {
		t.Error("channel 52 is not marked as a radar channel")
	}
}

func TestWirelessReportsWhatIsRunningAndWhoIsOnIt(t *testing.T) {
	t.Parallel()
	srv := newWirelessServer(t, wifiCmd{installed: true, active: true}, wirelessAPIConfig(), true)
	got := getRadios(t, srv)
	run := got.Radios[0].Running
	if run == nil {
		t.Fatal("a running radio reports nothing")
	}
	// hostapd may swap the pair, so the live channel is the one shown.
	if run.Channel != 40 || run.Width != 80 || run.TxPower != 22 {
		t.Errorf("running = %+v", run)
	}
	if len(run.Networks) != 1 || run.Networks[0].SSID != "ostiole-lan" || run.Networks[0].Clients != 2 {
		t.Errorf("networks = %+v", run.Networks)
	}

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/wireless/clients", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clients: %d %s", resp.StatusCode, raw)
	}
	var clients []wirelessClient
	if err := json.Unmarshal(raw, &clients); err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 {
		t.Fatalf("clients = %+v", clients)
	}
	first := clients[0]
	if first.MAC != "02:1a:2b:3c:4d:5e" || first.Radio != "wlp3s0" || first.SSID != "ostiole-lan" ||
		first.SignalDBm != -45 || first.ConnectedSeconds != 1052 {
		t.Errorf("client = %+v", first)
	}
}

func TestServicesStatusReportsWireless(t *testing.T) {
	t.Parallel()
	srv := newWirelessServer(t, wifiCmd{installed: true}, wirelessAPIConfig(), true)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/services/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %s", resp.StatusCode, raw)
	}
	var st servicesStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if !st.WirelessSetUp {
		t.Errorf("status = %+v", st)
	}
}

// A radio that should be on air and is not says why: hostapd's last
// complaint is on the card, so "stopped" is not the whole answer. A radio
// on air, or one nothing is configured on, carries no reason.
func TestWirelessStoppedRadioSaysWhy(t *testing.T) {
	t.Parallel()
	journal := "ap0: interface state UNINITIALIZED->COUNTRY_UPDATE\n" +
		"ap0: Could not set channel for kernel driver\n" +
		"ap0: AP-DISABLED\n" +
		"ap0: interface state COUNTRY_UPDATE->DISABLED\n"
	srv := newWirelessServer(t, wifiCmd{installed: true, active: false, journal: journal}, wirelessAPIConfig(), true)
	got := getRadios(t, srv)
	card := got.Radios[0]
	if card.Running != nil {
		t.Fatalf("a stopped radio reports running = %+v", card.Running)
	}
	if card.Stopped == nil || card.Stopped.Reason != "ap0: Could not set channel for kernel driver" {
		t.Errorf("stopped = %+v", card.Stopped)
	}

	// On air: no reason to give.
	srv = newWirelessServer(t, wifiCmd{installed: true, active: true, journal: journal}, wirelessAPIConfig(), true)
	if card := getRadios(t, srv).Radios[0]; card.Stopped != nil {
		t.Errorf("a running radio reports stopped = %+v", card.Stopped)
	}

	// Nothing configured on it: off, not stopped.
	cfg := wirelessAPIConfig()
	cfg.Interfaces = cfg.Interfaces[:1]
	cfg.Wireless.Radios = nil
	srv = newWirelessServer(t, wifiCmd{installed: true, active: false, journal: journal}, cfg, true)
	if card := getRadios(t, srv).Radios[0]; card.Stopped != nil {
		t.Errorf("an unconfigured radio reports stopped = %+v", card.Stopped)
	}
}
