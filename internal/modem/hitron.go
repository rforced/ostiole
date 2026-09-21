package modem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// hitron reads the JSON behind a Hitron CODA's status pages. The pages
// under /data answer without a login on the firmware seen so far.
type hitron struct{}

func (hitron) detect(ctx context.Context, c *client) (bool, error) {
	var model struct {
		Vendor string `json:"vendorname"`
	}
	if err := c.getJSON(ctx, "/data/system_model.asp", &model); err != nil {
		var pe *pageError
		if errors.As(err, &pe) {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(model.Vendor, "HITRON"), nil
}

func (hitron) fetch(ctx context.Context, c *client) (*Status, error) {
	var model struct {
		Vendor string `json:"vendorname"`
		Model  string `json:"modelName"`
	}
	var sys []struct {
		Hardware string `json:"hwVersion"`
		Firmware string `json:"swVersion"`
		Serial   string `json:"serialNumber"`
		MAC      string `json:"rfMac"`
		Uptime   string `json:"systemUptime"`
		Time     string `json:"systemTime"`
	}
	var link []struct {
		Status string `json:"LinkStatus"`
		Duplex string `json:"LinkDuplex"`
		Speed  string `json:"LinkSpeed"`
	}
	var boot []map[string]string
	var ds []struct {
		Port       string `json:"portId"`
		Frequency  string `json:"frequency"`
		Modulation string `json:"modulation"`
		Power      string `json:"signalStrength"`
		SNR        string `json:"snr"`
		Octets     string `json:"dsoctets"`
		Corrected  string `json:"correcteds"`
		Uncorrect  string `json:"uncorrect"`
		Channel    string `json:"channelId"`
	}
	var us []struct {
		Port       string `json:"portId"`
		Frequency  string `json:"frequency"`
		Bandwidth  string `json:"bandwidth"`
		Modulation string `json:"modtype"`
		Mode       string `json:"scdmaMode"`
		Power      string `json:"signalStrength"`
		Channel    string `json:"channelId"`
	}
	var dsOFDM []struct {
		Index     string `json:"receive"`
		FFT       string `json:"ffttype"`
		Frequency string `json:"Subcarr0freqFreq"`
		PLC       string `json:"plclock"`
		NCP       string `json:"ncplock"`
		MDC1      string `json:"mdc1lock"`
		Power     string `json:"plcpower"`
		SNR       string `json:"SNR"`
		Octets    string `json:"dsoctets"`
		Corrected string `json:"correcteds"`
		Uncorrect string `json:"uncorrect"`
	}
	var usOFDMA []struct {
		Index     string `json:"uschindex"`
		State     string `json:"state"`
		Frequency string `json:"frequency"`
		Bandwidth string `json:"channelBw"`
		Power     string `json:"repPower"`
		FFT       string `json:"fftVal"`
	}
	pages := []struct {
		path string
		into any
		need bool
	}{
		{"/data/system_model.asp", &model, true},
		{"/data/getSysInfo.asp", &sys, true},
		{"/data/getLinkStatus.asp", &link, false},
		{"/data/getCMInit.asp", &boot, false},
		{"/data/dsinfo.asp", &ds, false},
		{"/data/usinfo.asp", &us, false},
		{"/data/dsofdminfo.asp", &dsOFDM, false},
		{"/data/usofdminfo.asp", &usOFDMA, false},
	}
	for _, p := range pages {
		if err := c.getJSON(ctx, p.path, p.into); err != nil && p.need {
			return nil, err
		}
	}

	st := &Status{Vendor: "Hitron", Model: model.Model, Provisioning: []Step{}, Downstream: []Downstream{}, Upstream: []Upstream{}}
	if len(sys) > 0 {
		st.Hardware = sys[0].Hardware
		st.Firmware = sys[0].Firmware
		st.Serial = sys[0].Serial
		st.MAC = strings.ToLower(sys[0].MAC)
		st.Uptime = sys[0].Uptime
		st.Clock = sys[0].Time
	}
	if len(link) > 0 {
		st.Link = Link{Up: strings.EqualFold(link[0].Status, "up"), Speed: link[0].Speed, Duplex: link[0].Duplex}
	}
	if len(boot) > 0 {
		st.Provisioning = hitronSteps(boot[0])
	}
	for _, d := range ds {
		st.Downstream = append(st.Downstream, Downstream{
			Channel:       atoi(d.Channel),
			Kind:          "qam",
			Frequency:     atoi64(d.Frequency),
			Modulation:    hitronModulation(d.Modulation),
			Locked:        true,
			Power:         atof(d.Power),
			SNR:           atof(d.SNR),
			Octets:        atou(d.Octets),
			Corrected:     atou(d.Corrected),
			Uncorrectable: atou(d.Uncorrect),
		})
	}
	for _, d := range dsOFDM {
		if strings.EqualFold(strings.TrimSpace(d.FFT), "NA") {
			continue // an unused receiver
		}
		yes := func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "YES") }
		st.Downstream = append(st.Downstream, Downstream{
			Channel:       atoi(d.Index) + 1,
			Kind:          "ofdm",
			Frequency:     atoi64(d.Frequency),
			Modulation:    "OFDM " + strings.TrimSpace(d.FFT),
			Locked:        yes(d.PLC) && yes(d.NCP) && yes(d.MDC1),
			Power:         atof(d.Power),
			SNR:           atof(d.SNR),
			Octets:        atou(d.Octets),
			Corrected:     atou(d.Corrected),
			Uncorrectable: atou(d.Uncorrect),
		})
	}
	for _, u := range us {
		st.Upstream = append(st.Upstream, Upstream{
			Channel:    atoi(u.Channel),
			Kind:       "qam",
			Frequency:  atoi64(u.Frequency),
			Bandwidth:  atoi64(u.Bandwidth),
			Modulation: u.Modulation,
			Mode:       u.Mode,
			Power:      atof(u.Power),
		})
	}
	for _, u := range usOFDMA {
		if strings.EqualFold(strings.TrimSpace(u.State), "DISABLED") {
			continue
		}
		st.Upstream = append(st.Upstream, Upstream{
			Channel:    atoi(u.Index) + 1,
			Kind:       "ofdma",
			Frequency:  atoi64(u.Frequency),
			Bandwidth:  atoi64(u.Bandwidth),
			Modulation: "OFDMA " + strings.TrimSpace(u.FFT),
			Mode:       strings.TrimSpace(u.State),
			Power:      atof(u.Power),
		})
	}
	return st, nil
}

// hitronSteps orders the provisioning fields the way DOCSIS runs them.
func hitronSteps(m map[string]string) []Step {
	names := []struct{ key, name string }{
		{"hwInit", "Hardware"},
		{"findDownstream", "Find downstream"},
		{"ranging", "Ranging"},
		{"dhcp", "DHCP"},
		{"timeOfday", "Time of day"},
		{"downloadCfg", "Configuration file"},
		{"registration", "Registration"},
		{"bpiStatus", "Encryption"},
		{"networkAccess", "Network access"},
		{"trafficStatus", "Traffic"},
	}
	out := make([]Step, 0, len(names))
	for _, n := range names {
		v, ok := m[n.key]
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		out = append(out, Step{Name: n.name, Status: v, OK: hitronOK(v)})
	}
	return out
}

func hitronOK(v string) bool {
	v = strings.ToLower(v)
	switch {
	case v == "success", v == "permitted", v == "enable", v == "enabled":
		return true
	case strings.Contains(v, "authorized") && !strings.Contains(v, "unauthorized"):
		return true
	}
	return false
}

// hitronModulation names the code dsinfo uses for a QAM order.
func hitronModulation(code string) string {
	switch strings.TrimSpace(code) {
	case "0":
		return "QAM16"
	case "1":
		return "QAM64"
	case "2":
		return "QAM256"
	case "3":
		return "QAM1024"
	}
	return "QAM (" + strings.TrimSpace(code) + ")"
}

// pageError says the page was reached but was not what a driver expects:
// a login screen, or HTML where JSON should be. It is how detection says
// "not mine" rather than "not there".
type pageError struct{ err error }

func (p *pageError) Error() string { return p.err.Error() }
func (p *pageError) Unwrap() error { return p.err }

// getJSON reads one page into v. The modem's web server closes the
// connection without a proper end, so a short read that still parses is
// accepted.
func (c *client) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &pageError{fmt.Errorf("%s: %s", path, resp.Status)}
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &pageError{fmt.Errorf("%s: not JSON: %w", path, err)}
	}
	return nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func atou(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}
