package wireless

import (
	"strconv"
	"strings"
)

// Station is one client on a network.
type Station struct {
	MAC              string
	Interface        string
	ConnectedSeconds int
	InactiveMS       int
	RxBytes          uint64
	TxBytes          uint64
	RxPackets        uint64
	TxPackets        uint64
	SignalDBm        int
	TxBitrate        string
	RxBitrate        string
	Authorized       bool
}

// ParseStations reads `iw dev <interface> station dump`. Keys it has no
// use for are skipped, and there are a great many of them.
func ParseStations(text string) []Station {
	var out []Station
	var cur *Station
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "Station "); ok {
			mac, on, _ := strings.Cut(rest, " ")
			out = append(out, Station{
				MAC:       mac,
				Interface: strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(on), "(on "), ")"),
			})
			cur = &out[len(out)-1]
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || cur == nil {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "inactive time":
			cur.InactiveMS = int(number(value))
		case "connected time":
			cur.ConnectedSeconds = int(number(value))
		case "rx bytes":
			cur.RxBytes = unsigned(value)
		case "tx bytes":
			cur.TxBytes = unsigned(value)
		case "rx packets":
			cur.RxPackets = unsigned(value)
		case "tx packets":
			cur.TxPackets = unsigned(value)
		case "signal":
			cur.SignalDBm = int(number(value))
		case "tx bitrate":
			cur.TxBitrate = value
		case "rx bitrate":
			cur.RxBitrate = value
		case "authorized":
			cur.Authorized = value == "yes"
		}
	}
	return out
}

func unsigned(s string) uint64 {
	field, _, _ := strings.Cut(strings.TrimSpace(s), " ")
	n, err := strconv.ParseUint(field, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
