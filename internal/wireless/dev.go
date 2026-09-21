package wireless

import (
	"fmt"
	"strconv"
	"strings"
)

// DevInfo is what a radio is doing right now: the channel it settled on,
// which is not always the one it was asked for.
type DevInfo struct {
	Interface  string
	SSID       string
	Type       string
	Channel    int
	MHz        int
	Width      int
	TxPowerDBm float64
}

// ParseDevInfo reads `iw dev <interface> info`.
func ParseDevInfo(text string) (DevInfo, error) {
	var d DevInfo
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Interface "):
			d.Interface = strings.TrimPrefix(line, "Interface ")
		case strings.HasPrefix(line, "ssid "):
			d.SSID = strings.TrimPrefix(line, "ssid ")
		case strings.HasPrefix(line, "type "):
			d.Type = strings.TrimPrefix(line, "type ")
		case strings.HasPrefix(line, "txpower "):
			d.TxPowerDBm = number(strings.TrimPrefix(line, "txpower "))
		case strings.HasPrefix(line, "channel "):
			// channel 40 (5200 MHz), width: 80 MHz, center1: 5210 MHz
			d.Channel = int(number(strings.TrimPrefix(line, "channel ")))
			for _, part := range strings.Split(line, ",") {
				part = strings.TrimSpace(part)
				if w, ok := strings.CutPrefix(part, "width: "); ok {
					d.Width = int(number(w))
				}
			}
			if open := strings.Index(line, "("); open >= 0 {
				d.MHz = int(number(line[open+1:]))
			}
		}
	}
	if d.Interface == "" {
		return d, fmt.Errorf("no interface in iw output")
	}
	return d, nil
}

// RegCountry reads `iw reg get`: the kernel's domain and the domains of
// the devices that manage their own.
func RegCountry(text string) (global string, phys map[string]string) {
	phys = map[string]string{}
	current := ""
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "global":
			current = ""
		case strings.HasPrefix(line, "phy#"):
			n, _, _ := strings.Cut(strings.TrimPrefix(line, "phy#"), " ")
			current = "phy" + n
		case strings.HasPrefix(line, "country "):
			code, _, _ := strings.Cut(strings.TrimPrefix(line, "country "), ":")
			if current == "" {
				global = code
			} else {
				phys[current] = code
			}
		}
	}
	return global, phys
}

// number reads the leading number of a string, which is how iw prints a
// value and its unit.
func number(s string) float64 {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] == '-' || s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0
	}
	return f
}
