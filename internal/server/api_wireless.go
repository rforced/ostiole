package server

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/wireless"
)

// wirelessRadios is what the Radios tab shows: the cards this router has,
// what each of them can do, and what it is doing now. A router with no
// hostapd answers setUp false and an empty list rather than an error.
type wirelessRadios struct {
	SetUp   bool           `json:"setUp"`
	Country string         `json:"country,omitempty"`
	Radios  []wirelessCard `json:"radios"`
}

// wirelessCard is one device, merged from the kernel and from iw.
type wirelessCard struct {
	Name   string `json:"name"`
	Phy    string `json:"phy,omitempty"`
	Driver string `json:"driver,omitempty"`
	MAC    string `json:"mac,omitempty"`
	// SelfManaged marks a card whose firmware keeps its own regulatory
	// domain, which it learns from the networks around it.
	SelfManaged bool                    `json:"selfManaged,omitempty"`
	MaxNetworks int                     `json:"maxNetworks"`
	Bands       map[string]wirelessBand `json:"bands"`
	Running     *wirelessRunning        `json:"running,omitempty"`
}

// wirelessBand is one band as the card reports it.
type wirelessBand struct {
	Channels  []wirelessChannel `json:"channels"`
	MaxWidth  int               `json:"maxWidth"`
	Standards []string          `json:"standards"`
	// Serves is false on a band no channel allows a network on, which is
	// every 6 GHz channel on an Intel card.
	Serves bool `json:"serves"`
}

type wirelessChannel struct {
	Number   int  `json:"number"`
	MHz      int  `json:"mhz"`
	Radar    bool `json:"radar,omitempty"`
	Disabled bool `json:"disabled,omitempty"`
}

// wirelessRunning is the live state of a radio that is transmitting. The
// channel is the one hostapd settled on, which is not always the one it
// was asked for.
type wirelessRunning struct {
	Channel  int               `json:"channel"`
	Width    int               `json:"width"`
	TxPower  float64           `json:"txPower"`
	Networks []wirelessLiveNet `json:"networks"`
}

type wirelessLiveNet struct {
	Interface string `json:"interface"`
	SSID      string `json:"ssid,omitempty"`
	Clients   int    `json:"clients"`
}

// wirelessClient is one station, joined to its lease so the page can name
// it by something other than a MAC.
type wirelessClient struct {
	MAC              string `json:"mac"`
	Interface        string `json:"interface"`
	SSID             string `json:"ssid,omitempty"`
	Radio            string `json:"radio"`
	Hostname         string `json:"hostname,omitempty"`
	Address          string `json:"address,omitempty"`
	SignalDBm        int    `json:"signalDbm"`
	ConnectedSeconds int    `json:"connectedSeconds"`
	InactiveMS       int    `json:"inactiveMs"`
	RxBytes          uint64 `json:"rxBytes"`
	TxBytes          uint64 `json:"txBytes"`
	TxBitrate        string `json:"txBitrate,omitempty"`
	RxBitrate        string `json:"rxBitrate,omitempty"`
}

func (a *api) registerWireless(mux *router) {
	mux.HandleFunc("GET /api/v1/wireless/radios", a.readNoEngine(a.wirelessRadios))
	mux.HandleFunc("GET /api/v1/wireless/clients", a.readNoEngine(a.wirelessClients))
}

func (a *api) wirelessRadios(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, a.readRadios(r.Context()))
	return nil
}

func (a *api) readRadios(ctx context.Context) wirelessRadios {
	out := wirelessRadios{Radios: []wirelessCard{}}
	if a.wireless == nil || !a.wireless.Installed(ctx) {
		return out
	}
	out.SetUp = true
	global, perPhy := a.wireless.Regulatory(ctx)
	cfg, _ := a.engine.Store().Load()
	for _, radio := range a.wireless.Radios() {
		phy, err := a.wireless.Phy(ctx, radio)
		if err != nil {
			continue
		}
		card := wirelessCard{
			Name: radio, Phy: phy.Name, Driver: phy.Driver, MAC: phy.MAC,
			MaxNetworks: phy.APLimit,
			Bands:       map[string]wirelessBand{},
		}
		country, self := perPhy[phy.Name]
		card.SelfManaged = self
		if !self {
			country = global
		}
		if out.Country == "" {
			out.Country = country
		}
		for _, b := range model.Bands {
			info, ok := phy.Bands[b]
			if !ok {
				continue
			}
			band := wirelessBand{
				Channels: []wirelessChannel{}, MaxWidth: info.MaxWidth,
				Standards: standardsOf(b, info), Serves: phy.Serves(b),
			}
			for _, c := range info.Channels {
				band.Channels = append(band.Channels, wirelessChannel{
					Number: c.Number, MHz: c.MHz, Radar: c.Radar, Disabled: c.Disabled,
				})
			}
			card.Bands[string(b)] = band
		}
		card.Running = a.runningRadio(ctx, cfg, radio)
		out.Radios = append(out.Radios, card)
	}
	return out
}

// standardsOf names the generations a band can serve, best first.
func standardsOf(b model.Band, info wireless.BandInfo) []string {
	out := []string{}
	if info.HE {
		out = append(out, string(model.StandardAX))
	}
	if info.VHT && b == model.Band5G {
		out = append(out, string(model.StandardAC))
	}
	if info.HT {
		out = append(out, string(model.StandardN))
	}
	if b != model.Band6G {
		out = append(out, string(model.StandardLegacy))
	}
	return out
}

// runningRadio reads what a radio is doing from its first network, which
// is hostapd's own interface. A radio whose instance is stopped is not
// running, whatever the configuration says.
func (a *api) runningRadio(ctx context.Context, cfg *model.Config, radio string) *wirelessRunning {
	if cfg == nil || !a.wireless.Active(ctx, radio) {
		return nil
	}
	nets := cfg.NetworksOn(radio)
	if len(nets) == 0 {
		return nil
	}
	info, err := a.wireless.Dev(ctx, nets[0].Name)
	if err != nil {
		return nil
	}
	run := &wirelessRunning{
		Channel: info.Channel, Width: info.Width, TxPower: info.TxPowerDBm,
		Networks: []wirelessLiveNet{},
	}
	for _, in := range nets {
		stations, _ := a.wireless.Stations(ctx, in.Name)
		run.Networks = append(run.Networks, wirelessLiveNet{
			Interface: in.Name, SSID: in.Wireless.SSID, Clients: len(stations),
		})
	}
	return run
}

// wirelessClients lists who is connected, over every network of every
// radio that is running. Nothing running is an empty list.
func (a *api) wirelessClients(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	out := []wirelessClient{}
	if a.wireless == nil || !a.wireless.Installed(ctx) {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	cfg, _ := a.engine.Store().Load()
	if cfg == nil {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	leases := map[string]wirelessClient{}
	if a.services != nil {
		known, err := a.services.ReadLeases()
		if err == nil {
			for _, l := range known {
				if l.MAC == "" || l.Family != 4 {
					continue
				}
				leases[strings.ToLower(l.MAC)] = wirelessClient{Hostname: l.Hostname, Address: l.IP}
			}
		}
	}
	for _, radio := range cfg.ActiveRadios() {
		if !a.wireless.Active(ctx, radio.Name) {
			continue
		}
		for _, in := range cfg.NetworksOn(radio.Name) {
			stations, err := a.wireless.Stations(ctx, in.Name)
			if err != nil {
				continue
			}
			for _, s := range stations {
				c := wirelessClient{
					MAC: s.MAC, Interface: in.Name, SSID: in.Wireless.SSID, Radio: radio.Name,
					SignalDBm: s.SignalDBm, ConnectedSeconds: s.ConnectedSeconds, InactiveMS: s.InactiveMS,
					RxBytes: s.RxBytes, TxBytes: s.TxBytes, TxBitrate: s.TxBitrate, RxBitrate: s.RxBitrate,
				}
				if l, ok := leases[strings.ToLower(s.MAC)]; ok {
					c.Hostname, c.Address = l.Hostname, l.Address
				}
				out = append(out, c)
			}
		}
	}
	slices.SortFunc(out, func(x, y wirelessClient) int { return strings.Compare(x.MAC, y.MAC) })
	writeJSON(w, http.StatusOK, out)
	return nil
}
