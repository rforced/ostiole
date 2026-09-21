// Package wireless reads what a radio can do and renders hostapd's
// configuration for it. Reads go through iw and the text it prints, the
// way the firewall goes through nft (ADR-0001).
package wireless

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// Runner runs a command and returns its output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Phy is one wifi device: what it is and what it can be asked to do.
type Phy struct {
	Name   string
	Driver string
	MAC    string
	Modes  []string
	// APLimit is how many access points may run on this device at once.
	APLimit int
	Bands   map[model.Band]BandInfo
	// Features are the extended features the driver reports, by the name
	// nl80211 gives them.
	Features map[string]bool
}

// BeaconProtection reports whether this device can sign the beacons it
// sends. The client-side flag is a different feature and does not count:
// a card that can verify somebody else's beacons cannot necessarily
// protect its own.
func (p Phy) BeaconProtection() bool { return p.Features["BEACON_PROTECTION"] }

// BandInfo is what a device can do in one band.
type BandInfo struct {
	Channels []Channel
	HT       bool
	VHT      bool
	HE       bool
	// HTCapab and VHTCapab are the capability flags in hostapd's spelling.
	HTCapab  []string
	VHTCapab []string
	MaxWidth int
}

// Channel is one frequency and what the regulatory domain allows on it.
type Channel struct {
	Number   int
	MHz      int
	MaxDBm   int
	Disabled bool
	// NoIR forbids transmitting first, which is what a card says about a
	// band it has not learned the rules for yet.
	NoIR bool
	// Radar means a radar may be using the channel, so transmitting needs
	// detection the card may not have.
	Radar bool
}

// Supports reports whether the device has this band at all.
func (p *Phy) Supports(b model.Band) bool {
	_, ok := p.Bands[b]
	return ok
}

// Serves reports whether a network may be started on the band: the card
// has it, a channel allows it, and it is not 6 GHz on an Intel card, which
// no country it learns ever opens for an access point. Right after the
// driver loads the channels carry no flags yet, which is why the driver
// rule is there as well.
func (p *Phy) Serves(b model.Band) bool {
	info, ok := p.Bands[b]
	if !ok || !info.Serves() {
		return false
	}
	return b != model.Band6G || p.Driver != "iwlwifi"
}

// NoIROnly reports a band whose every allowed channel is still no-IR:
// what a self-managed card says about 5 GHz before it has learned its
// country, and what it says about 6 GHz for good.
func (b BandInfo) NoIROnly() bool {
	seen := false
	for _, c := range b.Channels {
		if c.Disabled {
			continue
		}
		if !c.NoIR {
			return false
		}
		seen = true
	}
	return seen
}

// Serves reports whether a network may be started on the band at all: one
// channel that is neither disabled nor no-IR. Intel cards mark every 6 GHz
// channel no-IR whatever the country, and stay that way.
func (b BandInfo) Serves() bool {
	for _, c := range b.Channels {
		if !c.Disabled && !c.NoIR {
			return true
		}
	}
	return false
}

// Channel returns the channel in a band.
func (b BandInfo) Channel(n int) (Channel, bool) {
	for _, c := range b.Channels {
		if c.Number == n {
			return c, true
		}
	}
	return Channel{}, false
}

var (
	freqRe  = regexp.MustCompile(`^([0-9.]+) MHz \[([0-9]+)\](.*)$`)
	dbmRe   = regexp.MustCompile(`\(([0-9.]+) dBm\)`)
	comboRe = regexp.MustCompile(`#\{([^}]*)\}\s*<=\s*([0-9]+)`)
)

// ParsePhy reads `iw phy <phy> info`.
func ParsePhy(text string) (Phy, error) {
	p := Phy{Bands: map[model.Band]BandInfo{}, Features: map[string]bool{}}
	var (
		section string
		sub     string
		band    *BandInfo
		heAP    bool
		combos  []string
	)
	flush := func() {
		if band == nil || len(band.Channels) == 0 {
			band = nil
			return
		}
		p.Bands[bandOf(band.Channels[0].MHz)] = *band
		band = nil
	}

	for _, raw := range strings.Split(text, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		depth := 0
		for depth < len(raw) && raw[depth] == '\t' {
			depth++
		}
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "*"))

		if depth == 0 {
			if name, ok := strings.CutPrefix(line, "Wiphy "); ok {
				p.Name = name
			}
			continue
		}
		if depth == 1 {
			flush()
			section, sub = line, ""
			if strings.HasPrefix(line, "Band ") && strings.HasSuffix(line, ":") {
				band = &BandInfo{}
			}
			continue
		}

		switch {
		case section == "Supported interface modes:":
			if depth == 2 {
				p.Modes = append(p.Modes, line)
			}
			continue
		case section == "valid interface combinations:":
			combos = append(combos, line)
			continue
		case section == "Supported extended features:":
			// Each line is "[ NAME ]: what it does".
			if name, _, ok := strings.Cut(line, "]:"); ok {
				p.Features[strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "["))] = true
			}
			continue
		case band == nil:
			continue
		}

		if depth == 2 {
			switch {
			case strings.HasPrefix(line, "Capabilities: 0x"):
				sub, band.HT = "ht", true
			case strings.HasPrefix(line, "VHT Capabilities"):
				sub, band.VHT = "vht", true
			case strings.HasPrefix(line, "HE Iftypes:"):
				sub = "he"
				heAP = slices.Contains(splitList(strings.TrimPrefix(line, "HE Iftypes:")), "AP")
				band.HE = band.HE || heAP
			case line == "Frequencies:":
				sub = "freq"
			default:
				sub = ""
			}
			continue
		}

		switch sub {
		case "ht":
			if c := htCapab(line); c != "" {
				band.HTCapab = append(band.HTCapab, c)
			}
		case "vht":
			if c := vhtCapab(line); c != "" {
				band.VHTCapab = append(band.VHTCapab, c)
			}
			if strings.HasPrefix(line, "Supported Channel Width: 160") {
				band.MaxWidth = 160
			}
		case "he":
			if heAP {
				band.MaxWidth = max(band.MaxWidth, heWidth(line))
			}
		case "freq":
			if c, ok := parseFrequency(line); ok {
				band.Channels = append(band.Channels, c)
			}
		}
	}
	flush()

	for _, b := range []model.Band{model.Band2G, model.Band5G, model.Band6G} {
		info, ok := p.Bands[b]
		if !ok {
			continue
		}
		info.MaxWidth = max(info.MaxWidth, widthFloor(info))
		for i, c := range info.Channels {
			// A card that prints no regulatory flags still may not use a
			// radar channel without detection, so the band's own list is
			// what the page and the preflight go by.
			info.Channels[i].Radar = c.Radar || (b == model.Band5G && slices.Contains(model.RadarChannels, c.Number))
		}
		p.Bands[b] = info
	}
	p.APLimit = apLimit(combos)

	if len(p.Bands) == 0 {
		return p, fmt.Errorf("no bands in iw output")
	}
	return p, nil
}

// widthFloor is the width the HT and VHT flags alone imply.
func widthFloor(b BandInfo) int {
	switch {
	case b.VHT:
		return 80
	case b.HT:
		return 40
	}
	return 20
}

// heWidth reads a width out of an HE PHY capability line. iw names the
// 5 GHz sets on 6 GHz too, which is the only place they appear there.
func heWidth(line string) int {
	switch {
	case strings.HasPrefix(line, "HE160"):
		return 160
	case strings.HasPrefix(line, "HE40/HE80"):
		return 80
	case strings.HasPrefix(line, "HE40"):
		return 40
	}
	return 0
}

// bandOf decides which band a frequency belongs to.
func bandOf(mhz int) model.Band {
	switch {
	case mhz < 3000:
		return model.Band2G
	case mhz < 5900:
		return model.Band5G
	}
	return model.Band6G
}

func parseFrequency(line string) (Channel, bool) {
	m := freqRe.FindStringSubmatch(line)
	if m == nil {
		return Channel{}, false
	}
	mhz, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return Channel{}, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return Channel{}, false
	}
	c := Channel{Number: n, MHz: int(math.Round(mhz))}
	rest := m[3]
	c.Disabled = strings.Contains(rest, "disabled")
	c.NoIR = strings.Contains(rest, "no IR")
	c.Radar = strings.Contains(rest, "radar detection")
	if d := dbmRe.FindStringSubmatch(rest); d != nil {
		if dbm, err := strconv.ParseFloat(d[1], 64); err == nil {
			c.MaxDBm = int(math.Round(dbm))
		}
	}
	return c, true
}

// apLimit reads the interface combinations for the largest number of
// access points the device runs at once. A device that lists AP in no
// combination runs none.
func apLimit(combos []string) int {
	limit := 0
	for _, m := range comboRe.FindAllStringSubmatch(strings.Join(combos, " "), -1) {
		if !slices.Contains(splitList(m[1]), "AP") {
			continue
		}
		if n, err := strconv.Atoi(m[2]); err == nil {
			limit = max(limit, n)
		}
	}
	return limit
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// htCapab and vhtCapab translate what iw prints into what hostapd takes.
func htCapab(line string) string {
	switch line {
	case "RX LDPC":
		return "[LDPC]"
	case "RX HT20 SGI":
		return "[SHORT-GI-20]"
	case "RX HT40 SGI":
		return "[SHORT-GI-40]"
	case "TX STBC":
		return "[TX-STBC]"
	case "RX STBC 1-stream":
		return "[RX-STBC1]"
	case "RX STBC 2-streams":
		return "[RX-STBC12]"
	case "RX STBC 3-streams":
		return "[RX-STBC123]"
	case "DSSS/CCK HT40":
		return "[DSSS_CCK-40]"
	case "Max AMSDU length: 7935 bytes":
		return "[MAX-AMSDU-7935]"
	}
	return ""
}

func vhtCapab(line string) string {
	switch {
	case line == "Max MPDU length: 11454":
		return "[MAX-MPDU-11454]"
	case line == "Max MPDU length: 7991":
		return "[MAX-MPDU-7991]"
	case strings.HasPrefix(line, "Supported Channel Width: 160 MHz"):
		return "[VHT160]"
	case line == "RX LDPC":
		return "[RXLDPC]"
	case line == "short GI (80 MHz)":
		return "[SHORT-GI-80]"
	case line == "short GI (160/80+80 MHz)":
		return "[SHORT-GI-160]"
	case line == "TX STBC":
		return "[TX-STBC-2BY1]"
	case line == "SU Beamformer":
		return "[SU-BEAMFORMER]"
	case line == "SU Beamformee":
		return "[SU-BEAMFORMEE]"
	case line == "MU Beamformer":
		return "[MU-BEAMFORMER]"
	case line == "MU Beamformee":
		return "[MU-BEAMFORMEE]"
	}
	return ""
}

// ProbePhy reads the device behind a radio's interface.
func ProbePhy(ctx context.Context, run Runner, radio string) (*Phy, error) {
	dir := filepath.Join("/sys/class/net", radio)
	name, err := readFile(filepath.Join(dir, "phy80211/name"))
	if err != nil {
		return nil, fmt.Errorf("%s is not a radio: %w", radio, err)
	}
	out, err := run.Run(ctx, "iw", "phy", name, "info")
	if err != nil {
		return nil, fmt.Errorf("iw phy %s info: %w", name, err)
	}
	p, err := ParsePhy(string(out))
	if err != nil {
		return nil, fmt.Errorf("iw phy %s info: %w", name, err)
	}
	p.Name = name
	p.MAC, _ = readFile(filepath.Join(dir, "address"))
	if link, err := os.Readlink(filepath.Join(dir, "device/driver")); err == nil {
		p.Driver = filepath.Base(link)
	}
	return &p, nil
}

// Radios lists the interfaces the kernel gave this router's wifi devices,
// sorted. There is one per device: the interfaces an access point runs on
// are added later and are Ostiole's own.
func Radios() []string {
	matches, err := filepath.Glob("/sys/class/ieee80211/*/device/net/*")
	if err != nil {
		return nil
	}
	first := map[string]string{}
	index := map[string]int{}
	for _, m := range matches {
		phy := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(m))))
		name := filepath.Base(m)
		n, err := readInt(filepath.Join(m, "ifindex"))
		if err != nil {
			continue
		}
		if cur, ok := index[phy]; !ok || n < cur {
			first[phy], index[phy] = name, n
		}
	}
	out := make([]string, 0, len(first))
	for _, name := range first {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func readInt(path string) (int, error) {
	s, err := readFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}
