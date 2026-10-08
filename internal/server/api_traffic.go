package server

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"ostiole/internal/logsearch"
	"ostiole/internal/model"
	"ostiole/internal/traffic"
)

func (a *api) registerTraffic(mux *router) {
	mux.HandleFunc("GET /api/v1/traffic/interfaces", a.readNoEngine(a.trafficInterfaces))
	mux.HandleFunc("GET /api/v1/traffic/devices", a.readNoEngine(a.trafficDevices))
	mux.HandleFunc("GET /api/v1/traffic/devices/{id}", a.readNoEngine(a.trafficDevice))
	mux.HandleFunc("GET /api/v1/traffic/stream", a.readNoEngine(a.trafficStream))
	mux.HandleFunc("GET /api/v1/traffic/destinations", a.readNoEngine(a.trafficDestinations))
	mux.HandleFunc("DELETE /api/v1/traffic", a.admin(a.trafficClear))
	mux.HandleFunc("DELETE /api/v1/traffic/destinations", a.admin(a.clearOne(traffic.DestinationsFile)))
}

var errNoTraffic = errors.New("traffic is not counted by this daemon")

// trafficWindow reads the window asked for, five minutes when none is.
func trafficWindow(r *http.Request) (string, error) {
	w := r.URL.Query().Get("window")
	if w == "" {
		return traffic.Window5m, nil
	}
	if !slices.Contains(traffic.Windows, w) {
		return "", &badRequest{fmt.Errorf("window %q must be one of %s", w, strings.Join(traffic.Windows, ", "))}
	}
	return w, nil
}

// trafficLink is a link over a window, and what the configuration says of
// it.
type trafficLink struct {
	traffic.LinkReport
	Description string `json:"description,omitempty"`
	Zone        string `json:"zone,omitempty"`
	External    bool   `json:"external,omitempty"`
	Configured  bool   `json:"configured"`
}

type trafficLinks struct {
	Window string `json:"window"`
	// Now is the router's clock, which a window ends at.
	Now   time.Time     `json:"now"`
	Links []trafficLink `json:"links"`
}

// trafficInterfaces is every link over a window: the configured ones in
// the order the configuration has them, then the rest by name.
func (a *api) trafficInterfaces(w http.ResponseWriter, r *http.Request) error {
	if a.traffic == nil {
		return &unavailable{errNoTraffic}
	}
	window, err := trafficWindow(r)
	if err != nil {
		return err
	}
	order := map[string]int{}
	out := trafficLinks{Window: window, Now: time.Now(), Links: []trafficLink{}}
	var cfg *model.Config
	if a.engine != nil {
		cfg = a.engine.Effective()
	}
	byName := map[string]trafficLink{}
	for _, l := range a.traffic.LinkReports(window) {
		byName[l.Name] = trafficLink{LinkReport: l}
	}
	if cfg != nil {
		for i, in := range cfg.Interfaces {
			l, ok := byName[in.Name]
			if !ok {
				continue
			}
			l.Configured, l.Description, l.Zone = true, in.Description, in.Zone
			if z, ok := cfg.Zone(in.Zone); ok {
				l.External = z.External
			}
			byName[in.Name] = l
			order[in.Name] = i + 1
		}
	}
	for _, l := range byName {
		out.Links = append(out.Links, l)
	}
	sort.Slice(out.Links, func(i, j int) bool {
		oi, oj := order[out.Links[i].Name], order[out.Links[j].Name]
		switch {
		case oi != 0 && oj != 0:
			return oi < oj
		case oi != 0 || oj != 0:
			return oi != 0
		}
		return out.Links[i].Name < out.Links[j].Name
	})
	writeJSON(w, http.StatusOK, out)
	return nil
}

// trafficDevice is a device over a window, and what the router calls it.
type trafficDevice struct {
	traffic.DeviceReport
	Name string `json:"name,omitempty"`
}

type trafficDevices struct {
	Window  string          `json:"window"`
	Now     time.Time       `json:"now"`
	Devices []trafficDevice `json:"devices"`
	traffic.Status
}

// trafficDevices is every device that moved something in the window, the
// busiest first, named as the leases page would name it.
func (a *api) trafficDevices(w http.ResponseWriter, r *http.Request) error {
	if a.traffic == nil {
		return &unavailable{errNoTraffic}
	}
	window, err := trafficWindow(r)
	if err != nil {
		return err
	}
	out := trafficDevices{Window: window, Now: time.Now(), Devices: []trafficDevice{}, Status: a.traffic.Status()}
	n := a.namer()
	for _, d := range a.traffic.DeviceReports(window) {
		out.Devices = append(out.Devices, trafficDevice{DeviceReport: d, Name: deviceTrafficName(n, d)})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// trafficDevice is one device over a window, with its points.
func (a *api) trafficDevice(w http.ResponseWriter, r *http.Request) error {
	if a.traffic == nil {
		return &unavailable{errNoTraffic}
	}
	window, err := trafficWindow(r)
	if err != nil {
		return err
	}
	d, ok := a.traffic.DeviceReport(r.PathValue("id"), window)
	if !ok {
		return &notFound{fmt.Errorf("no device %q has moved anything lately", r.PathValue("id"))}
	}
	writeJSON(w, http.StatusOK, trafficDevice{DeviceReport: d, Name: deviceTrafficName(a.namer(), d)})
	return nil
}

// deviceTrafficName is what a lease, a static lease or a host override
// calls the device: by its hardware address, else by any of its
// addresses. The router's own row is named by the page.
func deviceTrafficName(n *namer, d traffic.DeviceReport) string {
	if d.Router {
		return ""
	}
	if d.MAC != "" {
		if name := n.ByMAC(d.MAC); name != "" {
			return name
		}
	}
	for _, raw := range d.Addresses {
		if addr, err := netip.ParseAddr(raw); err == nil {
			if name := n.Name(addr); name != "" {
				return name
			}
		}
	}
	return ""
}

// trafficStream sends every link's rate each second and every device's
// after each read of the connection table (server-sent events).
func (a *api) trafficStream(w http.ResponseWriter, r *http.Request) error {
	if a.traffic == nil {
		return &unavailable{errNoTraffic}
	}
	ch, cancel := a.traffic.Subscribe(64)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e traffic.Event) any { return e }, nil)
}

// trafficClear forgets every device and what it moved, and every
// destination, their files included.
func (a *api) trafficClear(w http.ResponseWriter, r *http.Request) error {
	if a.traffic == nil {
		return &unavailable{errNoTraffic}
	}
	err := errors.Join(a.clearLog(traffic.DevicesFile, a.traffic.Clear),
		a.clearLog(traffic.DestinationsFile, func() {}))
	a.noteCleared(r, "cleared a log", "log", traffic.DevicesFile)
	a.noteCleared(r, "cleared a log", "log", traffic.DestinationsFile)
	if err != nil {
		return errFilesStay(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true, "at": time.Now()})
	return nil
}

// destinationWindows are the windows destinations are read over.
var destinationWindows = map[string]time.Duration{
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// serviceNames names the ports a destination row most often shows. Anything
// else shows its number.
var serviceNames = map[string]string{
	"tcp/443": "HTTPS",
	"udp/443": "QUIC",
	"udp/53":  "DNS",
	"tcp/53":  "DNS",
	"udp/123": "NTP",
	"tcp/22":  "SSH",
}

// serviceName is what a destination row calls its port.
func serviceName(proto string, port uint16) string {
	return serviceNames[proto+"/"+strconv.Itoa(int(port))]
}

// destinationRow is a destination as the page shows it: what the router
// calls the device, and the service its port is.
type destinationRow struct {
	traffic.DestinationRow
	DeviceName string `json:"deviceName,omitempty"`
	Service    string `json:"service,omitempty"`
}

type trafficDestinations struct {
	Window  string           `json:"window"`
	Enabled bool             `json:"enabled"`
	Entries []destinationRow `json:"entries"`
	// Next is the offset of the page after, when More says there is one.
	Next   int        `json:"next,omitempty"`
	More   bool       `json:"more"`
	Held   int        `json:"held"`
	Oldest *time.Time `json:"oldest,omitempty"`
}

// trafficDestinations is what each device moved with each destination
// over a window, the most first, searched with q and paged with offset.
func (a *api) trafficDestinations(w http.ResponseWriter, r *http.Request) error {
	if a.traffic == nil {
		return &unavailable{errNoTraffic}
	}
	v := r.URL.Query()
	window := v.Get("window")
	if window == "" {
		window = "24h"
	}
	span, ok := destinationWindows[window]
	if !ok {
		return &badRequest{fmt.Errorf("window %q must be 1h, 24h or 7d", window)}
	}
	offset, err := strconv.Atoi(cmp.Or(v.Get("offset"), "0"))
	if err != nil || offset < 0 {
		return &badRequest{fmt.Errorf("offset %q is not a row", v.Get("offset"))}
	}
	limit, err := strconv.Atoi(cmp.Or(v.Get("limit"), strconv.Itoa(logPageRows)))
	if err != nil || limit < 1 || limit > maxLogPageRows {
		return &badRequest{fmt.Errorf("limit must be 1-%d", maxLogPageRows)}
	}
	rows, held, oldest := a.traffic.Destinations(span, v.Get("device"))
	out := trafficDestinations{Window: window, Enabled: a.traffic.DestinationsOn(), Entries: []destinationRow{}, Held: held}
	if !oldest.IsZero() {
		out.Oldest = &oldest
	}
	n := a.namer()
	q := logsearch.Parse(v.Get("q"))
	row := q.Row()
	found := make([]destinationRow, 0, len(rows))
	for _, d := range rows {
		dr := destinationRow{DestinationRow: d, Service: serviceName(d.Protocol, d.Port)}
		dr.DeviceName = deviceTrafficName(n, traffic.DeviceReport{
			ID: d.Device, MAC: macOf(d.Device), Router: d.Device == traffic.RouterID, Addresses: []string{d.Device},
		})
		if !q.Empty() {
			row.Reset()
			for _, s := range []string{dr.Destination, dr.Address, dr.Service, dr.DeviceName, dr.Device, dr.Protocol, strconv.Itoa(int(dr.Port))} {
				row.Add(s)
			}
			if !row.Match() {
				continue
			}
		}
		found = append(found, dr)
	}
	sort.Slice(found, func(i, j int) bool {
		a, b := found[i].Down+found[i].Up, found[j].Down+found[j].Up
		if a != b {
			return a > b
		}
		if found[i].Destination != found[j].Destination {
			return found[i].Destination < found[j].Destination
		}
		return found[i].Device < found[j].Device
	})
	if offset < len(found) {
		end := min(offset+limit, len(found))
		out.Entries = found[offset:end]
		if end < len(found) {
			out.Next, out.More = end, true
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// macOf is a device id that is a hardware address, or "".
func macOf(id string) string {
	if _, err := net.ParseMAC(id); err == nil && strings.Count(id, ":") == 5 {
		return id
	}
	return ""
}
