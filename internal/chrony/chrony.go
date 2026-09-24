// Package chrony reads what the router's time service is doing, through
// chronyc's CSV output on the loopback command port. It never changes
// anything: every command it sends is one chronyd answers as monitoring.
package chrony

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrNoTool says chronyc is not on this router.
	ErrNoTool = errors.New("chronyc is not installed")
	// ErrNotAuthorised says chronyd would not answer the command over its
	// command port: it is not one of the monitoring commands it opened,
	// which a chronyd older than 4.7 cannot do for authdata and
	// serverstats at all.
	ErrNotAuthorised = errors.New("chronyd does not answer this command on its command port")
)

// never is how chronyc's CSV writes "no sample yet" in a seconds column.
const never = 4294967295

// Client runs chronyc.
type Client struct {
	// Bin is chronyc; empty means none was found and every call returns
	// ErrNoTool.
	Bin string
	// Run runs the binary and returns its output; nil execs it.
	Run func(ctx context.Context, bin string, args ...string) ([]byte, error)
}

// New looks for chronyc. A miss leaves Bin empty rather than failing, so
// a page can say what is missing.
func New() *Client {
	path, err := exec.LookPath("chronyc")
	if err != nil {
		return &Client{}
	}
	return &Client{Bin: path}
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

// Tracking reads the state of the system clock.
func (c *Client) Tracking(ctx context.Context) (Tracking, error) {
	rows, err := c.csv(ctx, "-n", "tracking")
	if err != nil {
		return Tracking{}, err
	}
	if len(rows) != 1 || len(rows[0]) < 14 {
		return Tracking{}, fmt.Errorf("chronyc tracking: unexpected output")
	}
	return parseTracking(rows[0])
}

func parseTracking(f []string) (Tracking, error) {
	var t Tracking
	var err error
	num := func(s string) float64 {
		if err != nil {
			return 0
		}
		var v float64
		v, err = strconv.ParseFloat(s, 64)
		return v
	}
	if f[1] != "" && f[0] != "00000000" {
		t.Address = f[1]
	}
	t.Stratum, err = strconv.Atoi(f[2])
	if ref := num(f[3]); ref > 0 {
		sec := int64(ref)
		t.RefTime = time.Unix(sec, int64((ref-float64(sec))*1e9)).UTC()
	}
	// chronyc writes how far the clock is behind: positive is slow.
	t.Offset = -num(f[4])
	t.RootDelay = num(f[10])
	t.RootDispersion = num(f[11])
	t.Leap = f[13]
	if err != nil {
		return Tracking{}, fmt.Errorf("chronyc tracking: %w", err)
	}
	return t, nil
}

// Sources reads every source with its configured name. chronyc lists them
// in the same order with and without names, so the two reads line up.
func (c *Client) Sources(ctx context.Context) ([]Source, error) {
	byAddr, err := c.csv(ctx, "-n", "sources")
	if err != nil {
		return nil, err
	}
	byName, err := c.csv(ctx, "-N", "sources")
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(byAddr))
	for i, f := range byAddr {
		if len(f) < 10 {
			return nil, fmt.Errorf("chronyc sources: unexpected output")
		}
		s, err := parseSource(f)
		if err != nil {
			return nil, err
		}
		s.Name = s.Address
		if len(byName) == len(byAddr) && len(byName[i]) >= 3 {
			s.Name = byName[i][2]
		}
		out = append(out, s)
	}
	return out, nil
}

var sourceStates = map[string]string{
	"*": "selected",
	"+": "combined",
	"-": "excluded",
	"?": "unusable",
	"x": "falseticker",
	"~": "jittery",
}

func parseSource(f []string) (Source, error) {
	s := Source{Address: f[2], State: sourceStates[f[1]]}
	if s.State == "" {
		s.State = "unusable"
	}
	var err error
	atoi := func(v string) int {
		if err != nil {
			return 0
		}
		var n int
		n, err = strconv.Atoi(v)
		return n
	}
	num := func(v string) float64 {
		if err != nil {
			return 0
		}
		var x float64
		x, err = strconv.ParseFloat(v, 64)
		return x
	}
	s.Stratum = atoi(f[3])
	if poll := atoi(f[4]); poll >= -32 && poll <= 32 {
		s.Poll = time.Duration(pow2(poll) * float64(time.Second))
	}
	reach, rerr := strconv.ParseUint(f[5], 8, 8)
	if rerr != nil && err == nil {
		err = rerr
	}
	s.Reach = uint8(reach)
	s.LastRx = seconds(atoi(f[6]))
	s.Offset = num(f[7])
	s.Error = num(f[9])
	if err != nil {
		return Source{}, fmt.Errorf("chronyc sources: %w", err)
	}
	return s, nil
}

// pow2 is 2 to the n, for a poll interval chronyc gives as its exponent.
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

// seconds turns a count chronyc may give as "never" into a duration.
func seconds(n int) time.Duration {
	if n < 0 || n == never {
		return -1
	}
	return time.Duration(n) * time.Second
}

// Auth reads how each source's answers are authenticated. It needs the
// command opened to monitoring, which chronyd before 4.7 cannot do.
func (c *Client) Auth(ctx context.Context) ([]Auth, error) {
	byAddr, err := c.csv(ctx, "-n", "authdata")
	if err != nil {
		return nil, err
	}
	byName, err := c.csv(ctx, "-N", "authdata")
	if err != nil {
		return nil, err
	}
	out := make([]Auth, 0, len(byAddr))
	for i, f := range byAddr {
		if len(f) < 10 {
			return nil, fmt.Errorf("chronyc authdata: unexpected output")
		}
		a, err := parseAuth(f)
		if err != nil {
			return nil, err
		}
		a.Name = a.Address
		if len(byName) == len(byAddr) && len(byName[i]) >= 1 {
			a.Name = byName[i][0]
		}
		out = append(out, a)
	}
	return out, nil
}

func parseAuth(f []string) (Auth, error) {
	a := Auth{Address: f[0], Mode: f[1]}
	if a.Mode == "-" {
		a.Mode = ""
	}
	ints := make([]int, 0, 4)
	for _, i := range []int{5, 6, 7, 8} {
		n, err := strconv.Atoi(f[i])
		if err != nil {
			return Auth{}, fmt.Errorf("chronyc authdata: %w", err)
		}
		ints = append(ints, n)
	}
	a.LastKE = seconds(ints[0])
	a.Attempts, a.NAK, a.Cookies = ints[1], ints[2] > 0, ints[3]
	return a, nil
}

// ServerStats reads what chronyd answered as a server. It needs the
// command opened to monitoring, which chronyd before 4.7 cannot do.
func (c *Client) ServerStats(ctx context.Context) (ServerStats, error) {
	rows, err := c.csv(ctx, "-n", "serverstats")
	if err != nil {
		return ServerStats{}, err
	}
	if len(rows) != 1 || len(rows[0]) < 2 {
		return ServerStats{}, fmt.Errorf("chronyc serverstats: unexpected output")
	}
	var st ServerStats
	if st.NTPReceived, err = strconv.ParseInt(rows[0][0], 10, 64); err == nil {
		st.NTPDropped, err = strconv.ParseInt(rows[0][1], 10, 64)
	}
	if err != nil {
		return ServerStats{}, fmt.Errorf("chronyc serverstats: %w", err)
	}
	return st, nil
}

// csv runs one command over the loopback command port and splits its
// lines. The address is fixed rather than the Unix socket chronyc would
// try first: that needs a socket of its own under /run/chrony, which the
// daemon's sandbox cannot write. -n or -N keeps chronyc from looking
// addresses up in DNS.
func (c *Client) csv(ctx context.Context, names, command string) ([][]string, error) {
	if c.Bin == "" {
		return nil, ErrNoTool
	}
	run := c.Run
	if run == nil {
		run = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, bin, args...).CombinedOutput()
		}
	}
	out, err := run(ctx, c.Bin, "-h", "127.0.0.1", names, "-c", command)
	text := strings.TrimSpace(string(out))
	if strings.HasPrefix(text, "501 ") {
		return nil, ErrNotAuthorised
	}
	if err != nil {
		if text != "" {
			return nil, fmt.Errorf("chronyc %s: %w: %s", command, err, firstLine(text))
		}
		return nil, fmt.Errorf("chronyc %s: %w", command, err)
	}
	var rows [][]string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			rows = append(rows, strings.Split(line, ","))
		}
	}
	return rows, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
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
