// Package chrony reads what the router's time service is doing from
// chronyd's command port on the loopback, speaking the protocol chronyc
// speaks. It never changes anything: every request is one chronyd answers
// as monitoring. Running chronyc instead would cost a process per
// question and, under SELinux, an audit record for each, since the
// daemon's NoNewPrivileges keeps chronyc out of its own domain.
package chrony

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNotAuthorised says chronyd would not answer the command over its
// command port: it is not one of the monitoring commands it opened, which
// a chronyd older than 4.7 cannot do for authdata and serverstats at all.
var ErrNotAuthorised = errors.New("chronyd does not answer this command on its command port")

// DefaultAddr is where chronyd listens for monitoring unless told
// otherwise. Its Unix socket is no use here: a client binds a socket of
// its own beside it under /run/chrony, which the daemon's sandbox cannot
// write.
const DefaultAddr = "127.0.0.1:323"

// Client asks chronyd.
type Client struct {
	// Addr is the command port; empty means DefaultAddr.
	Addr string
}

// New returns a client for the local chronyd.
func New() *Client { return &Client{} }

func (c *Client) dial(ctx context.Context) (*conn, error) {
	addr := c.Addr
	if addr == "" {
		addr = DefaultAddr
	}
	return dial(ctx, addr)
}

// Tracking is what chronyd makes of the system clock.
type Tracking struct {
	// Address is the source the clock follows; empty while it follows none.
	Address string
	Stratum int
	// RefTime is when the clock was last corrected from a source; zero
	// when it never was.
	RefTime time.Time
	// Offset is how far the clock is from true time, in seconds. Positive
	// means it is ahead.
	Offset         float64
	RootDelay      float64
	RootDispersion float64
	// Leap is chronyd's leap status: Normal, Insert second, Delete second,
	// or Not synchronised.
	Leap string
}

// Synchronised reports whether the clock is following a source.
func (t Tracking) Synchronised() bool {
	return t.Leap != "" && t.Leap != "Not synchronised"
}

// Source is one server the router asks.
type Source struct {
	// Name is the one it was configured with: every server of a pool
	// carries the pool's name.
	Name    string
	Address string
	// State is selected (the clock follows it), combined (it agrees and
	// counts), excluded (it counts for nothing), unusable (no answer, or
	// too few), falseticker (it disagrees with the rest) or jittery.
	State   string
	Stratum int
	// Poll is how often it is asked.
	Poll time.Duration
	// Reach has a bit for each of the last eight polls that was answered.
	Reach uint8
	// LastRx is how long ago it last answered; negative when it never has.
	LastRx time.Duration
	// Offset is the router's clock against this source at the last
	// answer, in seconds. Positive means the router is ahead.
	Offset float64
	// Error is the margin of that measurement, in seconds.
	Error float64
}

// Auth is how answers from one source are authenticated.
type Auth struct {
	Name    string
	Address string
	// Mode is NTS, SK for a symmetric key, or empty for none.
	Mode string
	// LastKE is how long ago the last NTS key exchange succeeded; negative
	// when none has.
	LastKE time.Duration
	// Attempts counts key exchanges tried since the last one that
	// succeeded. More than one means the server or the path to it is
	// failing.
	Attempts int
	// NAK says the server refused the last request's cookie.
	NAK bool
	// Cookies is how many requests can still be made before another key
	// exchange.
	Cookies int
}

// ServerStats counts what chronyd answered as a server since it started.
type ServerStats struct {
	NTPReceived int64
	NTPDropped  int64
}

var leapNames = map[uint16]string{0: "Normal", 1: "Insert second", 2: "Delete second", 3: "Not synchronised"}

// Tracking reads the state of the system clock.
func (c *Client) Tracking(ctx context.Context) (Tracking, error) {
	cn, err := c.dial(ctx)
	if err != nil {
		return Tracking{}, err
	}
	defer func() { _ = cn.Close() }()
	d, err := cn.exchange(ctx, cmdTracking, nil)
	if err != nil {
		return Tracking{}, err
	}
	return decodeTracking(d), nil
}

func decodeTracking(d []byte) Tracking {
	t := Tracking{
		Stratum:        int(binary.BigEndian.Uint16(d[24:])),
		RefTime:        decodeTime(d[28:]),
		RootDelay:      decodeFloat(d[64:]),
		RootDispersion: decodeFloat(d[68:]),
		Leap:           leapNames[binary.BigEndian.Uint16(d[26:])],
	}
	if t.Leap == "" {
		t.Leap = "Invalid"
	}
	// chronyd reports the correction still to apply: positive is slow.
	t.Offset = -decodeFloat(d[40:])
	refID := binary.BigEndian.Uint32(d)
	ip, family := decodeAddr(d[4:])
	switch {
	case refID == 0:
	case family == familyUnspec:
		// A reference clock, or the local one, goes by its ID.
		t.Address = refIDName(refID)
	case ip.IsValid():
		t.Address = ip.String()
	}
	return t
}

// listed is one source as the list reports it, with its address as
// chronyd wrote it, which is how it is asked about again.
type listed struct {
	Source
	mode uint16
	addr []byte
}

var sourceStates = map[uint16]string{
	0: "selected",
	1: "unusable",
	2: "falseticker",
	3: "jittery",
	4: "combined",
	5: "excluded",
}

// list walks the sources by index, as chronyc does.
func (cn *conn) list(ctx context.Context) ([]listed, error) {
	d, err := cn.exchange(ctx, cmdNSources, nil)
	if err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(d)
	var out []listed
	for i := range n {
		d, err := cn.exchange(ctx, cmdSourceData, binary.BigEndian.AppendUint32(nil, i))
		if noSuchSource(err) {
			break
		}
		if err != nil {
			return nil, err
		}
		if s, ok := decodeSource(d); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// decodeSource reads a SOURCE_DATA report. ok is false for a name that has
// not resolved to an address yet, which chronyd lists by an ID and chronyc
// leaves out.
func decodeSource(d []byte) (s listed, ok bool) {
	ip, family := decodeAddr(d)
	s = listed{mode: binary.BigEndian.Uint16(d[26:]), addr: d[:20]}
	switch {
	case s.mode == modeRef && family == familyInet4:
		// A reference clock goes by its ID.
		s.Address = refIDName(binary.BigEndian.Uint32(d))
	case ip.IsValid():
		s.Address = ip.String()
	default:
		return listed{}, false
	}
	s.Name = s.Address
	s.State = sourceStates[binary.BigEndian.Uint16(d[24:])]
	if s.State == "" {
		s.State = "unusable"
	}
	s.Stratum = int(binary.BigEndian.Uint16(d[22:]))
	// The poll is a signed exponent.
	poll := int(binary.BigEndian.Uint16(d[20:]))
	if poll >= 1<<15 {
		poll -= 1 << 16
	}
	if poll >= -32 && poll <= 32 {
		s.Poll = time.Duration(pow2(poll) * float64(time.Second))
	}
	// Eight polls in the low byte of the field.
	s.Reach = d[31]
	s.LastRx = ago(binary.BigEndian.Uint32(d[32:]))
	// The offset as adjusted since it was measured, as chronyc shows first.
	s.Offset = decodeFloat(d[40:])
	s.Error = decodeFloat(d[44:])
	return s, true
}

// name asks for the name a source was configured with. A reference clock
// has none, and one chronyd cannot give keeps its address.
func (cn *conn) name(ctx context.Context, s listed) (string, error) {
	if s.mode == modeRef {
		return s.Name, nil
	}
	d, err := cn.exchange(ctx, cmdSourceName, s.addr)
	var r *refusal
	if errors.As(err, &r) || errors.Is(err, ErrNotAuthorised) {
		return s.Name, nil
	}
	if err != nil {
		return "", err
	}
	if name, ok := decodeName(d); ok {
		return name, nil
	}
	return s.Name, nil
}

// decodeName reads an NTP_SOURCE_NAME report: a name ended by a NUL, and
// printable, or chronyc will not show it.
func decodeName(d []byte) (string, bool) {
	name, _, ok := strings.Cut(string(d), "\x00")
	if !ok || name == "" || strings.ContainsFunc(name, func(r rune) bool { return r <= ' ' || r >= 0x7f }) {
		return "", false
	}
	return name, true
}

// Sources reads every source with its configured name.
func (c *Client) Sources(ctx context.Context) ([]Source, error) {
	cn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cn.Close() }()
	list, err := cn.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(list))
	for _, s := range list {
		if s.Name, err = cn.name(ctx, s); err != nil {
			return nil, err
		}
		out = append(out, s.Source)
	}
	return out, nil
}

// pow2 is 2 to the n, for a poll interval chronyd gives as its exponent.
func pow2(n int) float64 {
	v := 1.0
	for ; n > 0; n-- {
		v *= 2
	}
	for ; n < 0; n++ {
		v /= 2
	}
	return v
}

var authModes = map[uint16]string{0: "", 1: "SK", 2: "NTS"}

// Auth reads how each server's answers are authenticated. It needs the
// command opened to monitoring, which chronyd before 4.7 cannot do.
func (c *Client) Auth(ctx context.Context) ([]Auth, error) {
	cn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cn.Close() }()
	list, err := cn.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Auth, 0, len(list))
	for _, s := range list {
		if s.mode != modeClient && s.mode != modePeer {
			continue
		}
		d, err := cn.exchange(ctx, cmdAuthData, s.addr)
		if noSuchSource(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		a := decodeAuth(d)
		a.Address = s.Address
		if a.Name, err = cn.name(ctx, s); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// decodeAuth reads an AUTH_DATA report.
func decodeAuth(d []byte) Auth {
	mode, ok := authModes[binary.BigEndian.Uint16(d)]
	if !ok {
		mode = "?"
	}
	return Auth{
		Mode:     mode,
		Attempts: int(binary.BigEndian.Uint16(d[10:])),
		LastKE:   ago(binary.BigEndian.Uint32(d[12:])),
		Cookies:  int(binary.BigEndian.Uint16(d[16:])),
		NAK:      binary.BigEndian.Uint16(d[20:]) > 0,
	}
}

// ServerStats reads what chronyd answered as a server. It needs the
// command opened to monitoring, which chronyd before 4.7 cannot do.
func (c *Client) ServerStats(ctx context.Context) (ServerStats, error) {
	cn, err := c.dial(ctx)
	if err != nil {
		return ServerStats{}, err
	}
	defer func() { _ = cn.Close() }()
	d, err := cn.exchange(ctx, cmdServerStats, nil)
	if err != nil {
		return ServerStats{}, err
	}
	return decodeServerStats(d), nil
}

// decodeServerStats reads a SERVER_STATS4 report, counters of two words
// each, which chronyd has sent since 4.4.
func decodeServerStats(d []byte) ServerStats {
	return ServerStats{NTPReceived: count(d), NTPDropped: count(d[24:])}
}

func count(b []byte) int64 {
	n := binary.BigEndian.Uint64(b)
	if n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}

// Version is what a chronyd build says it is.
type Version struct {
	Major, Minor, Patch int
	// NTS says the build can authenticate servers with Network Time
	// Security.
	NTS bool
}

var versionRe = regexp.MustCompile(`version (\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion reads `chronyd -v`: "chronyd (chrony) version 4.9 (+CMDMON
// +NTS ...)".
func ParseVersion(out string) (Version, error) {
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return Version{}, fmt.Errorf("no version in %q", firstLine(strings.TrimSpace(out)))
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
	}
	v.NTS = strings.Contains(out, "+NTS")
	return v, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// AtLeast reports whether the build is major.minor or newer.
func (v Version) AtLeast(major, minor int) bool {
	return v.Major > major || v.Major == major && v.Minor >= minor
}

// String is the version as chrony writes it.
func (v Version) String() string {
	if v.Major == 0 && v.Minor == 0 {
		return ""
	}
	if v.Patch != 0 {
		return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	}
	return fmt.Sprintf("%d.%d", v.Major, v.Minor)
}
