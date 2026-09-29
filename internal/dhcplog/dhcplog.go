// Package dhcplog keeps what the DHCP server says about each client at the
// Info and Debug levels: a line a message, DHCPv4 and DHCPv6, read from
// its journal.
package dhcplog

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/logring"
	"github.com/rforced/ostiole/internal/logsearch"
	"github.com/rforced/ostiole/internal/model"
)

// Event is one message of a client's with the DHCP server, as the server
// logged it.
type Event struct {
	logring.Stamp
	// Message is what was sent without its DHCP: DISCOVER, OFFER, REQUEST,
	// ACK, NAK, RELEASE, DECLINE, INFORM, and for DHCPv6 SOLICIT,
	// ADVERTISE, REPLY, RENEW, REBIND and the rest.
	Message   string `json:"message"`
	Interface string `json:"interface"`
	// Address is what was offered, asked for or given, when the message
	// has one.
	Address string `json:"address,omitempty"`
	// MAC is a DHCPv4 client's hardware address, DUID a DHCPv6 client's
	// identifier.
	MAC  string `json:"mac,omitempty"`
	DUID string `json:"duid,omitempty"`
	// Name is the name a client gave with an address it was given, and
	// Detail anything else the server said, such as why it refused.
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Log is the DHCP server's messages in memory.
type Log = logring.Ring[Event, *Event]

// New returns an empty log of the default size.
func New() *Log {
	var k model.LogKeep
	return logring.New[Event, *Event](k.Size(model.DefaultDHCPLogEntries), k.Retention())
}

// Settings sizes the log in a configuration.
func Settings(c *model.Config) (int, time.Duration) {
	l := c.Services.DHCP.Log
	return l.Size(model.DefaultDHCPLogEntries), l.Retention()
}

// Kept says whether the configuration keeps the log at all: only the
// levels that keep what the daemons say about their clients do.
func Kept(c *model.Config) bool { return c.System.Logging.Records() }

// The log's directory under logfile.Dir, and the format of its lines: an
// event as the API serves it.
const (
	FileName    = "dhcp"
	FileVersion = 1
)

// Files describes the log to the writer that keeps it in files.
func Files(l *Log) logfile.Log {
	return l.Files(FileName, FileVersion, Kept, func(c *model.Config) int {
		return int(c.Services.DHCP.Log.Retention() / (24 * time.Hour))
	})
}

// ParseLine reads a line of the log's files.
func ParseLine(line []byte) (Event, time.Time, error) {
	return logring.Parse[Event, *Event](line)
}

// Parse reads a line of the DHCP server's, keeping only a message's:
//
//	DHCPACK(eth1) 10.0.0.5 aa:bb:cc:dd:ee:ff laptop
//	DHCPREPLY(eth1) fd00::1c4 00:03:00:01:aa:bb:cc:dd:ee:ff laptop
//	DHCPNAK(eth1) 10.0.0.5 aa:bb:cc:dd:ee:ff wrong network
//
// At Debug each line starts with the transaction's number, and more lines
// say what was asked and sent, which are passed over, as are the router
// advertisements. dnsmasq 2.85 to 2.93 write them alike.
func Parse(message string) (Event, bool) {
	s := strings.TrimRight(message, " ")
	if i := strings.IndexByte(s, ' '); i > 0 {
		if _, err := strconv.ParseUint(s[:i], 10, 32); err == nil {
			s = s[i+1:]
		}
	}
	rest, ok := strings.CutPrefix(s, "DHCP")
	if !ok {
		return Event{}, false
	}
	open := strings.IndexByte(rest, '(')
	shut := strings.IndexByte(rest, ')')
	if open <= 0 || shut < open {
		return Event{}, false
	}
	e := Event{Message: rest[:open], Interface: rest[open+1 : shut]}
	fields := strings.Fields(rest[shut+1:])
	if len(fields) == 0 {
		return Event{}, false
	}
	if _, err := netip.ParseAddr(fields[0]); err == nil {
		e.Address, fields = fields[0], fields[1:]
	}
	if len(fields) == 0 {
		return Event{}, false
	}
	if _, err := net.ParseMAC(fields[0]); err == nil && strings.Count(fields[0], ":") == 5 {
		e.MAC = strings.ToLower(fields[0])
	} else {
		e.DUID = strings.ToLower(fields[0])
	}
	// What follows the client is the name it gave with an address, or
	// the server's word on it, which may run to several.
	if more := strings.Join(fields[1:], " "); more != "" {
		if len(fields) == 2 && (e.Message == "ACK" || e.Message == "REPLY") {
			e.Name = more
		} else {
			e.Detail = more
		}
	}
	return e, true
}

// Search hands a the values the Log tab shows for the event, as it shows
// them: the message, where, the address, the client and what it or the
// server said. device is the client's name on this router. buf is
// scratch, handed back to be used again.
func (e *Event) Search(a logsearch.Adder, buf []byte, device string) []byte {
	a.Add(e.Message)
	a.Add(e.Interface)
	a.Add(e.Address)
	a.Add(device)
	a.Add(e.MAC)
	a.Add(e.DUID)
	a.Add(e.Name)
	a.Add(e.Detail)
	return buf
}

// Matcher is a search's test of an event, the same for the log's files as
// for its memory. device names a client by its address.
func Matcher(q logsearch.Query, device func(*Event) string) func(*Event) bool {
	row := q.Row()
	var buf []byte
	return func(e *Event) bool {
		if q.Empty() {
			return true
		}
		row.Reset()
		var who string
		if device != nil {
			who = device(e)
		}
		buf = e.Search(&row, buf, who)
		return row.Match()
	}
}

// ClientMAC is the client's hardware address: its MAC, or the one a DHCPv6
// identifier of the link-layer kinds carries, empty for any other.
func (e *Event) ClientMAC() string {
	if e.MAC != "" {
		return e.MAC
	}
	b, err := hexBytes(e.DUID)
	if err != nil {
		return ""
	}
	// DUID-LLT is 00:01, Ethernet's 00:01, four bytes of time and the
	// address; DUID-LL is 00:03, 00:01 and the address.
	switch {
	case len(b) == 14 && b[0] == 0 && b[1] == 1 && b[2] == 0 && b[3] == 1:
		return net.HardwareAddr(b[8:]).String()
	case len(b) == 10 && b[0] == 0 && b[1] == 3 && b[2] == 0 && b[3] == 1:
		return net.HardwareAddr(b[4:]).String()
	}
	return ""
}

// hexBytes reads bytes written as hex pairs between colons.
func hexBytes(s string) ([]byte, error) {
	var out []byte
	for part := range strings.SplitSeq(s, ":") {
		v, err := strconv.ParseUint(part, 16, 8)
		if err != nil || len(part) != 2 {
			return nil, strconv.ErrSyntax
		}
		out = append(out, byte(v))
	}
	return out, nil
}
