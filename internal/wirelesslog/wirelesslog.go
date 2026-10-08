// Package wirelesslog keeps the wireless clients' coming and going, which
// the access point writes at the Info and Debug levels, read from its
// journal.
package wirelesslog

import (
	"net"
	"strings"
	"time"

	"ostiole/internal/logfile"
	"ostiole/internal/logring"
	"ostiole/internal/logsearch"
	"ostiole/internal/model"
)

// What a client did, as the Log tab writes it.
const (
	Connected     = "connected"
	Disconnected  = "disconnected"
	WrongPassword = "wrong password"
)

// Event is one client joining or leaving a network, or failing to join.
type Event struct {
	logring.Stamp
	Event string `json:"event"`
	// Interface is the network's; MAC the client's.
	Interface string `json:"interface"`
	MAC       string `json:"mac"`
}

// Log is the clients' coming and going in memory.
type Log = logring.Ring[Event, *Event]

// New returns an empty log of the default size.
func New() *Log {
	var k model.LogKeep
	return logring.New[Event, *Event](k.Size(model.DefaultWirelessLogEntries), k.Retention())
}

// Settings sizes the log in a configuration.
func Settings(c *model.Config) (int, time.Duration) {
	l := c.Wireless.Log
	return l.Size(model.DefaultWirelessLogEntries), l.Retention()
}

// Kept says whether the configuration keeps the log at all: only the
// levels that keep what the daemons say about their clients do.
func Kept(c *model.Config) bool { return c.System.Logging.Records() }

// The log's directory under logfile.Dir, and the format of its lines: an
// event as the API serves it, without the names put on when it is read.
const (
	FileName    = "wireless"
	FileVersion = 1
)

// Files describes the log to the writer that keeps it in files.
func Files(l *Log) logfile.Log {
	return l.Files(FileName, FileVersion, Kept, func(c *model.Config) int {
		return int(c.Wireless.Log.Retention() / (24 * time.Hour))
	})
}

// ParseLine reads a line of the log's files.
func ParseLine(line []byte) (Event, time.Time, error) {
	return logring.Parse[Event, *Event](line)
}

// events are the access point's words for what a client did.
var events = map[string]string{
	"CONNECTED":             Connected,
	"DISCONNECTED":          Disconnected,
	"POSSIBLE-PSK-MISMATCH": WrongPassword,
}

// Parse reads a line the access point wrote, keeping a client's coming and
// going:
//
//	wlan0: AP-STA-CONNECTED aa:bb:cc:dd:ee:ff
//	wlan0: AP-STA-DISCONNECTED aa:bb:cc:dd:ee:ff
//	wlan0: AP-STA-POSSIBLE-PSK-MISMATCH aa:bb:cc:dd:ee:ff
func Parse(message string) (Event, bool) {
	iface, rest, ok := strings.Cut(message, ": AP-STA-")
	if !ok || iface == "" || strings.ContainsRune(iface, ' ') {
		return Event{}, false
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return Event{}, false
	}
	event, ok := events[fields[0]]
	if !ok {
		return Event{}, false
	}
	if _, err := net.ParseMAC(fields[1]); err != nil || strings.Count(fields[1], ":") != 5 {
		return Event{}, false
	}
	return Event{Event: event, Interface: iface, MAC: strings.ToLower(fields[1])}, true
}

// Search hands a the values the Log tab shows for the event, as it shows
// them: what happened, on which network, and who. network is the
// interface's network name and device the client's name on this router.
func (e *Event) Search(a logsearch.Adder, buf []byte, network, device string) []byte {
	a.Add(e.Event)
	a.Add(network)
	a.Add(e.Interface)
	a.Add(device)
	a.Add(e.MAC)
	return buf
}

// Matcher is a search's test of an event, the same for the log's files as
// for its memory. names gives an event's network and device.
func Matcher(q logsearch.Query, names func(*Event) (network, device string)) func(*Event) bool {
	row := q.Row()
	var buf []byte
	return func(e *Event) bool {
		if q.Empty() {
			return true
		}
		row.Reset()
		var network, device string
		if names != nil {
			network, device = names(e)
		}
		buf = e.Search(&row, buf, network, device)
		return row.Match()
	}
}
