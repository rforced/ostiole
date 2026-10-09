package diag

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"ostiole/internal/logsearch"
	"ostiole/internal/netlink"
	"ostiole/internal/sysctl"
)

// State is one connection the kernel is tracking. It is what the
// firewall's "established" rules are matching against, so it answers
// "why is this getting through" and "what is this router talking to".
type State struct {
	Protocol string `json:"protocol"`
	// Source and Destination are as the connection was opened.
	Source      string `json:"source"`
	SourcePort  uint16 `json:"sourcePort,omitempty"`
	Destination string `json:"destination"`
	DestPort    uint16 `json:"destPort,omitempty"`
	// ReplySource is the address replies come back to. When it differs
	// from the destination, this connection is being NATed, and this is
	// where you see it.
	ReplySource   string `json:"replySource,omitempty"`
	ReplyDest     string `json:"replyDest,omitempty"`
	ReplyDestPort uint16 `json:"replyDestPort,omitempty"`
	// NAT is true when the reply does not simply mirror the request.
	NAT bool `json:"nat"`
	// State is the TCP state, empty for the protocols that have none.
	State string `json:"state,omitempty"`
	// TTL is how long the entry has left.
	TTL     string `json:"ttl"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
	// Mark is the packet mark the flow carries, which is how policy
	// routing pins it to one gateway.
	Mark uint32 `json:"mark,omitempty"`
}

// StatesOptions filters the table. A busy firewall has tens of thousands
// of entries, so the answer is always trimmed.
type StatesOptions struct {
	// Query is the words of the page's search box, each in what the row
	// shows, or a network like 10.0.0.0/8 holding either end.
	Query string
	// Address matches either end, as an address or a prefix.
	Address string
	// Protocol is tcp, udp, icmp, or empty for all.
	Protocol string
	// Port matches either port.
	Port uint16
	// Limit caps the rows returned; zero means DefaultStateLimit.
	Limit int
}

// DefaultStateLimit is how many connections are returned when no limit is
// asked for.
const DefaultStateLimit = 500

// StatesResult is the answer, with the totals the rows were drawn from.
type StatesResult struct {
	States []State `json:"states"`
	// Total is how many connections the kernel is tracking, before
	// filtering; Matched is how many the filter kept.
	Total   int `json:"total"`
	Matched int `json:"matched"`
	// ByProtocol counts the whole table, which is the useful summary.
	ByProtocol map[string]int `json:"byProtocol"`
	// Max is what the kernel will track at once, so Total has something
	// to be read against: a table filling up drops packets and says so
	// only in dmesg. Zero when the limit cannot be read.
	Max int `json:"max,omitempty"`
}

// States reads the connection tracking table.
func States(opts StatesOptions) (*StatesResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultStateLimit
	}
	res := &StatesResult{States: []State{}, ByProtocol: map[string]int{}}
	search := parseSearch(opts.Query)
	var readErr error
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		err := netlink.Flows(family, func(f netlink.Flow) {
			st := stateOf(f)
			res.Total++
			res.ByProtocol[st.Protocol]++
			if !matches(st, opts) || !search.matches(&st) {
				return
			}
			res.Matched++
			if len(res.States) < limit {
				res.States = append(res.States, st)
			}
		})
		// A router with no IPv6, or a kernel without the module, answers
		// for the family it has rather than failing outright.
		if err != nil && readErr == nil {
			readErr = err
		}
	}
	if res.Total == 0 {
		if readErr != nil {
			// Almost always one of two things: the daemon is not root, or
			// the router has never had a connection to track.
			return nil, fmt.Errorf("cannot read the connection table: %w "+
				"(this needs the daemon to be root, and the conntrack module loaded)", readErr)
		}
		return nil, errors.New("the kernel is tracking no connections")
	}
	// Best effort: the rows are the answer, the ceiling is context.
	if ceiling, err := (sysctl.Proc{}).ConntrackMax(); err == nil {
		res.Max = ceiling
	}
	sort.Slice(res.States, func(i, j int) bool {
		if res.States[i].Bytes != res.States[j].Bytes {
			return res.States[i].Bytes > res.States[j].Bytes
		}
		return res.States[i].Source < res.States[j].Source
	})
	return res, nil
}

func stateOf(f netlink.Flow) State {
	st := State{
		Protocol:      protocolName(f.Forward.Protocol),
		Source:        ipString(f.Forward.SrcIP),
		SourcePort:    f.Forward.SrcPort,
		Destination:   ipString(f.Forward.DstIP),
		DestPort:      f.Forward.DstPort,
		ReplySource:   ipString(f.Reverse.SrcIP),
		ReplyDest:     ipString(f.Reverse.DstIP),
		ReplyDestPort: f.Reverse.DstPort,
		TTL:           (time.Duration(f.Timeout) * time.Second).String(),
		Packets:       f.Forward.Packets + f.Reverse.Packets,
		Bytes:         f.Forward.Bytes + f.Reverse.Bytes,
		Mark:          f.Mark,
	}
	// A connection that is not translated has its reply mirroring the
	// request exactly; anything else is NAT at work.
	st.NAT = st.ReplySource != st.Destination || st.ReplyDest != st.Source
	if f.Forward.Protocol == unix.IPPROTO_TCP {
		st.State = tcpStateName(f.TCPState)
	}
	return st
}

func matches(s State, o StatesOptions) bool {
	if o.Protocol != "" && !strings.EqualFold(o.Protocol, s.Protocol) {
		return false
	}
	if o.Port != 0 && s.SourcePort != o.Port && s.DestPort != o.Port && s.ReplyDestPort != o.Port {
		return false
	}
	if o.Address == "" {
		return true
	}
	return addressMatches(o.Address, s.Source, s.Destination, s.ReplySource, s.ReplyDest)
}

// stateSearch is the search box's words: networks, which hold one end of
// a connection, and the rest, which are looked for in what the row shows.
type stateSearch struct {
	networks []*net.IPNet
	words    logsearch.Query
	row      logsearch.Row
	buf      []byte
}

func parseSearch(q string) *stateSearch {
	s := &stateSearch{}
	var words []string
	for w := range strings.FieldsSeq(q) {
		if _, network, err := net.ParseCIDR(w); err == nil {
			s.networks = append(s.networks, network)
			continue
		}
		words = append(words, w)
	}
	s.words = logsearch.Parse(strings.Join(words, " "))
	s.row = s.words.Row()
	return s
}

// matches reports whether every network holds an end of the connection
// and every word is in what its row shows: the protocol, both ends, where
// it leaves as, and its state.
func (s *stateSearch) matches(st *State) bool {
	for _, n := range s.networks {
		if !addressMatches(n.String(), st.Source, st.Destination, st.ReplySource, st.ReplyDest) {
			return false
		}
	}
	if s.words.Empty() {
		return true
	}
	s.row.Reset()
	s.row.Add(st.Protocol)
	s.buf = appendEndpoint(s.buf[:0], st.Source, st.SourcePort)
	s.row.AddBytes(s.buf)
	s.buf = appendEndpoint(s.buf[:0], st.Destination, st.DestPort)
	s.row.AddBytes(s.buf)
	if st.NAT {
		s.buf = appendEndpoint(s.buf[:0], st.ReplyDest, st.ReplyDestPort)
		s.row.AddBytes(s.buf)
	}
	s.row.Add(st.State)
	return s.row.Match()
}

// appendEndpoint writes an address with its port, as the page does.
func appendEndpoint(buf []byte, addr string, port uint16) []byte {
	buf = append(buf, addr...)
	if port != 0 {
		buf = strconv.AppendUint(append(buf, ':'), uint64(port), 10)
	}
	return buf
}

// addressMatches accepts an address or a prefix, so "10.0.0.0/8" finds
// everything from a network.
func addressMatches(want string, candidates ...string) bool {
	if _, network, err := net.ParseCIDR(want); err == nil {
		for _, c := range candidates {
			if ip := net.ParseIP(c); ip != nil && network.Contains(ip) {
				return true
			}
		}
		return false
	}
	for _, c := range candidates {
		if strings.EqualFold(c, want) {
			return true
		}
	}
	return false
}

func protocolName(p uint8) string {
	switch p {
	case unix.IPPROTO_TCP:
		return "tcp"
	case unix.IPPROTO_UDP:
		return "udp"
	case unix.IPPROTO_ICMP:
		return "icmp"
	case unix.IPPROTO_ICMPV6:
		return "icmpv6"
	case unix.IPPROTO_SCTP:
		return "sctp"
	case unix.IPPROTO_GRE:
		return "gre"
	}
	return fmt.Sprintf("proto-%d", p)
}

// tcpStateNames are the kernel's names for the states a tracked TCP
// connection moves through.
var tcpStateNames = []string{
	"NONE", "SYN_SENT", "SYN_RECV", "ESTABLISHED", "FIN_WAIT",
	"CLOSE_WAIT", "LAST_ACK", "TIME_WAIT", "CLOSE", "SYN_SENT2",
}

func tcpStateName(s uint8) string {
	if int(s) < len(tcpStateNames) {
		return tcpStateNames[s]
	}
	return fmt.Sprintf("state-%d", s)
}

func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}

// Neighbour is one entry of the ARP or NDP table: which address is at
// which hardware address, on which interface.
type Neighbour struct {
	Address   string `json:"address"`
	MAC       string `json:"mac,omitempty"`
	Interface string `json:"interface"`
	Family    string `json:"family"`
	// State is the kernel's view of how current this entry is:
	// REACHABLE, STALE, FAILED, and so on.
	State string `json:"state"`
	// Router marks an IPv6 neighbour that advertises itself as one.
	Router bool `json:"router,omitempty"`
	// Seen is when the neighbour last answered, to ARP or NDP or to a
	// connection. The router asks only when it has traffic for it. Zero
	// when the entry holds no address the neighbour gave.
	Seen time.Time `json:"seen,omitzero"`
}

// answered are the states whose hardware address came from the
// neighbour. A permanent entry was typed in; a failed one gave up.
const answered = unix.NUD_REACHABLE | unix.NUD_STALE | unix.NUD_DELAY | unix.NUD_PROBE

// userHZ is the clock tick the kernel counts neighbour ages in, 100 on
// every architecture Ostiole ships for.
const userHZ = 100

// ago is the time a neighbour age in clock ticks points back to.
func ago(now time.Time, ticks uint32) time.Time {
	return now.Add(-time.Duration(ticks) * time.Second / userHZ)
}

// Neighbours reads the ARP and NDP tables. Entries with no hardware
// address and nothing useful to say are left out.
func Neighbours() ([]Neighbour, error) {
	links, err := netlink.Links()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	names := map[int]string{}
	for _, l := range links {
		names[l.Index] = l.Name
	}
	out := []Neighbour{}
	now := time.Now()
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		entries, err := netlink.Neighbours(family)
		if err != nil {
			continue
		}
		for _, n := range entries {
			if n.IP == nil {
				continue
			}
			state := neighStateName(n.State)
			if state == "NOARP" || state == "NONE" {
				continue
			}
			nb := Neighbour{
				Address:   n.IP.String(),
				Interface: names[n.LinkIndex],
				Family:    "IPv4",
				State:     state,
				Router:    n.Flags&unix.NTF_ROUTER != 0,
			}
			if family == unix.AF_INET6 {
				nb.Family = "IPv6"
			}
			if len(n.HardwareAddr) > 0 {
				nb.MAC = n.HardwareAddr.String()
				if n.State&answered != 0 {
					nb.Seen = ago(now, n.Confirmed)
				}
			}
			out = append(out, nb)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Interface != out[j].Interface {
			return out[i].Interface < out[j].Interface
		}
		return out[i].Address < out[j].Address
	})
	return out, nil
}

// neighStateNames map the kernel's neighbour states, which are a bitmask.
var neighStateNames = []struct {
	bit  int
	name string
}{
	{unix.NUD_INCOMPLETE, "INCOMPLETE"},
	{unix.NUD_REACHABLE, "REACHABLE"},
	{unix.NUD_STALE, "STALE"},
	{unix.NUD_DELAY, "DELAY"},
	{unix.NUD_PROBE, "PROBE"},
	{unix.NUD_FAILED, "FAILED"},
	{unix.NUD_NOARP, "NOARP"},
	{unix.NUD_PERMANENT, "PERMANENT"},
}

func neighStateName(state int) string {
	var names []string
	for _, s := range neighStateNames {
		if state&s.bit != 0 {
			names = append(names, s.name)
		}
	}
	if len(names) == 0 {
		return "NONE"
	}
	return strings.Join(names, "+")
}
