package chrony_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/chrony"
	"github.com/rforced/ostiole/internal/chrony/chronytest"
)

var selected = chrony.Tracking{
	Address: "3.220.42.39", Stratum: 3, RefTime: time.Unix(1790304601, 205858088).UTC(),
	Offset: -0.002682065, RootDelay: 0.049011745, RootDispersion: 0.001693060, Leap: "Normal",
}

// following is a daemon following four NTS servers, one of them on IPv6,
// an address that never answers, and a pool with a name still to resolve.
func following(t *testing.T) (*chronytest.Daemon, *chrony.Client) {
	t.Helper()
	d := chronytest.New(t)
	d.SetTracking(selected)
	nts := func(lastKE time.Duration) *chrony.Auth {
		return &chrony.Auth{Mode: "NTS", LastKE: lastKE, Cookies: 8}
	}
	d.SetSources(
		chronytest.Source{Name: "nts.netnod.se", Address: "194.58.205.196", State: "excluded",
			Stratum: 1, Poll: 64 * time.Second, Reach: 0o37, LastRx: 6 * time.Second, Offset: -0.002077126, Error: 0.070244834,
			Auth: nts(76 * time.Second)},
		chronytest.Source{Name: "nts.time.nl", Address: "2a01:3f7:3:51::4", State: "combined",
			Stratum: 2, Poll: 64 * time.Second, Reach: 0o37, LastRx: 7 * time.Second, Offset: -0.002527710, Error: 0.070126206,
			Auth: nts(77 * time.Second)},
		chronytest.Source{Name: "virginia.time.system76.com", Address: "3.220.42.39", State: "selected",
			Stratum: 2, Poll: 64 * time.Second, Reach: 0o37, LastRx: 6 * time.Second, Offset: 0.000897395, Error: 0.025176724,
			Auth: nts(77 * time.Second)},
		chronytest.Source{Name: "192.0.2.123", Address: "192.0.2.123", State: "unusable",
			Poll: 128 * time.Second, LastRx: -1},
		chronytest.Source{Name: "2.pool.ntp.org", Address: "23.186.168.126", State: "unusable",
			Stratum: 2, Poll: 64 * time.Second, Reach: 0o37, LastRx: 10 * time.Second, Offset: 0.002320803, Error: 0.057981949},
		chronytest.Source{Name: "2.pool.ntp.org", Unresolved: true},
		chronytest.Source{Name: "2.pool.ntp.org", Address: "104.232.0.123", State: "jittery",
			Stratum: 2, Poll: 64 * time.Second, Reach: 0o37, LastRx: 8 * time.Second, Offset: 0.000656150, Error: 0.034684535},
	)
	d.SetServerStats(chrony.ServerStats{NTPReceived: 1234, NTPDropped: 5})
	return d, &chrony.Client{Addr: d.Addr}
}

// near compares what went through chrony's 25-bit float with what went in.
func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9+math.Abs(b)/(1<<23) }

func TestTracking(t *testing.T) {
	t.Parallel()
	_, c := following(t)
	tr, err := c.Tracking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Synchronised() || tr.Address != selected.Address || tr.Stratum != 3 || !tr.RefTime.Equal(selected.RefTime) {
		t.Errorf("tracking = %+v", tr)
	}
	if !near(tr.Offset, selected.Offset) || !near(tr.RootDelay, selected.RootDelay) || !near(tr.RootDispersion, selected.RootDispersion) {
		t.Errorf("offset %v, delay %v, dispersion %v", tr.Offset, tr.RootDelay, tr.RootDispersion)
	}
}

func TestTrackingBeforeTheFirstSync(t *testing.T) {
	t.Parallel()
	d := chronytest.New(t)
	tr, err := (&chrony.Client{Addr: d.Addr}).Tracking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tr.Synchronised() || tr.Address != "" || !tr.RefTime.IsZero() || tr.Leap != "Not synchronised" {
		t.Errorf("tracking = %+v", tr)
	}
}

func TestSourcesCarryTheConfiguredNames(t *testing.T) {
	t.Parallel()
	_, c := following(t)
	got, err := c.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The name that has not resolved is left out, as chronyc leaves it.
	if len(got) != 6 {
		t.Fatalf("got %d sources, want 6: %+v", len(got), got)
	}
	sel := got[2]
	if sel.Name != "virginia.time.system76.com" || sel.Address != "3.220.42.39" || sel.State != "selected" {
		t.Errorf("selected source = %+v", sel)
	}
	if sel.Poll != 64*time.Second || sel.Reach != 0o37 || sel.LastRx != 6*time.Second || sel.Stratum != 2 {
		t.Errorf("selected source timing = %+v", sel)
	}
	if !near(sel.Offset, 0.000897395) || !near(sel.Error, 0.025176724) {
		t.Errorf("selected source offset = %v ± %v", sel.Offset, sel.Error)
	}
	if v6 := got[1]; v6.Address != "2a01:3f7:3:51::4" || v6.Name != "nts.time.nl" || v6.State != "combined" {
		t.Errorf("a source on IPv6 = %+v", v6)
	}
	if dead := got[3]; dead.State != "unusable" || dead.LastRx >= 0 || dead.Reach != 0 || dead.Name != "192.0.2.123" {
		t.Errorf("a source that never answered = %+v", dead)
	}
	// Every server of a pool goes by the pool's name.
	if got[4].Name != "2.pool.ntp.org" || got[5].Name != "2.pool.ntp.org" || got[5].State != "jittery" {
		t.Errorf("pool servers = %+v, %+v", got[4], got[5])
	}
}

func TestAuthShowsWhichSourcesAreSigned(t *testing.T) {
	t.Parallel()
	_, c := following(t)
	got, err := c.Auth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d rows, want 6: %+v", len(got), got)
	}
	nl := got[1]
	if nl.Name != "nts.time.nl" || nl.Address != "2a01:3f7:3:51::4" || nl.Mode != "NTS" || nl.Cookies != 8 ||
		nl.NAK || nl.Attempts != 0 || nl.LastKE != 77*time.Second {
		t.Errorf("an NTS source = %+v", nl)
	}
	if plain := got[3]; plain.Mode != "" || plain.LastKE >= 0 || plain.Name != "192.0.2.123" {
		t.Errorf("an unsigned source = %+v", plain)
	}
}

func TestServerStats(t *testing.T) {
	t.Parallel()
	_, c := following(t)
	st, err := c.ServerStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.NTPReceived != 1234 || st.NTPDropped != 5 {
		t.Errorf("server stats = %+v", st)
	}
}

// Before 4.7 chronyd cannot open authdata or serverstats to the command
// port. The rest still answers.
func TestClosedCommandsSayNotAuthorised(t *testing.T) {
	t.Parallel()
	d, c := following(t)
	d.Refuse("authdata", "serverstats")
	if _, err := c.Auth(context.Background()); !errors.Is(err, chrony.ErrNotAuthorised) {
		t.Errorf("authdata err = %v, want ErrNotAuthorised", err)
	}
	if _, err := c.ServerStats(context.Background()); !errors.Is(err, chrony.ErrNotAuthorised) {
		t.Errorf("serverstats err = %v, want ErrNotAuthorised", err)
	}
	if _, err := c.Sources(context.Background()); err != nil {
		t.Errorf("sources: %v", err)
	}
}

// Where the name cannot be read, as on a daemon that does not open it, a
// source keeps its address.
func TestASourceWithoutItsName(t *testing.T) {
	t.Parallel()
	d, c := following(t)
	d.Refuse("sourcename")
	got, err := c.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "194.58.205.196" {
		t.Errorf("name = %q", got[0].Name)
	}
}

// A lost datagram is sent again, as chronyc sends it, a second later.
func TestALostRequestIsSentAgain(t *testing.T) {
	t.Parallel()
	d, c := following(t)
	d.Drop(1)
	start := time.Now()
	tr, err := c.Tracking(context.Background())
	if err != nil || tr.Address != selected.Address {
		t.Fatalf("tracking = %+v, %v", tr, err)
	}
	if took := time.Since(start); took < time.Second || took > 3*time.Second {
		t.Errorf("took %v", took)
	}
	if n := d.Requests(); n != 2 {
		t.Errorf("sent %d requests, want 2", n)
	}
}

func TestNoDaemon(t *testing.T) {
	t.Parallel()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	_ = conn.Close()
	start := time.Now()
	_, err = (&chrony.Client{Addr: addr}).Tracking(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not listening") {
		t.Errorf("err = %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a closed port took %v to say so", took)
	}
}

// A daemon that takes the request and never answers holds the caller no
// longer than its context allows.
func TestADaemonThatNeverAnswers(t *testing.T) {
	t.Parallel()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &chrony.Client{Addr: conn.LocalAddr().String()}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Tracking(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v", took)
	}

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, err := c.Sources(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want cancelled", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a cancel took %v", took)
	}
}

func TestParseVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		out  string
		want chrony.Version
	}{
		{
			"chronyd (chrony) version 4.9 (+CMDMON +REFCLOCK +RTC +PRIVDROP +SCFILTER +SIGND +NTS +SECHASH +IPV6 +DEBUG)\n",
			chrony.Version{Major: 4, Minor: 9, NTS: true},
		},
		{
			"chronyd (chrony) version 4.6.1 (+CMDMON +NTP +REFCLOCK +RTC +PRIVDROP +SCFILTER +SIGND +ASYNCDNS +NTS +SECHASH +IPV6 -DEBUG)\n",
			chrony.Version{Major: 4, Minor: 6, Patch: 1, NTS: true},
		},
		{
			"chronyd (chrony) version 4.5 (+CMDMON +NTP +REFCLOCK +RTC -PRIVDROP -SCFILTER -SIGND +ASYNCDNS -NTS -SECHASH +IPV6 -DEBUG)\n",
			chrony.Version{Major: 4, Minor: 5},
		},
	} {
		got, err := chrony.ParseVersion(tc.out)
		if err != nil || got != tc.want {
			t.Errorf("ParseVersion(%q) = %+v, %v; want %+v", tc.out, got, err, tc.want)
		}
	}
	if _, err := chrony.ParseVersion("chronyd: command not found"); err == nil {
		t.Error("a reply with no version parsed")
	}
	v := chrony.Version{Major: 4, Minor: 6, Patch: 1}
	if v.AtLeast(4, 7) || !v.AtLeast(4, 6) || !v.AtLeast(3, 9) || v.String() != "4.6.1" {
		t.Errorf("4.6.1: AtLeast or String wrong")
	}
	if (chrony.Version{Major: 4, Minor: 7}).String() != "4.7" || (chrony.Version{}).String() != "" {
		t.Error("String of 4.7 or of nothing is wrong")
	}
}

// TestAgainstChronyd holds the client to what chronyc makes of a real
// daemon: one started as this user, following addresses that never answer
// and a name that never resolves, so nothing in it moves but the polling.
// It needs chronyd and chronyc, and a chronyd that may write its pid file
// in a temporary directory, which AppArmor refuses on some systems.
func TestAgainstChronyd(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a daemon")
	}
	chronyd, chronyc := find("chronyd"), find("chronyc")
	if chronyd == "" || chronyc == "" {
		t.Skip("chronyd or chronyc is not installed")
	}
	out, _ := exec.Command(chronyd, "-v").Output()
	v, err := chrony.ParseVersion(string(out))
	if err != nil {
		t.Skip(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)
	_ = conn.Close()
	args := []string{"-x", "-d", "-U", "pidfile " + filepath.Join(t.TempDir(), "chronyd.pid"), "port 0",
		"cmdport " + port, "bindcmdaddress 127.0.0.1", "bindcmdaddress /",
		"server 192.0.2.1 iburst", "pool pool.invalid iburst"}
	if v.NTS {
		args = append(args, "server 192.0.2.2 iburst nts")
	}
	if v.AtLeast(4, 7) {
		args = append(args, "opencommands activity authdata manual rtcdata serverstats smoothing sourcename sources sourcestats tracking")
	}
	daemon := exec.Command(chronyd, args...)
	var stderr bytes.Buffer
	daemon.Stderr = &stderr
	if err := daemon.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_ = daemon.Wait()
	})
	for start := time.Now(); !strings.Contains(chronycCSV(chronyc, port, "-n", "tracking"), ","); time.Sleep(100 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Skipf("chronyd did not start: %s", stderr.String())
		}
	}
	matchChronyc(t, chronyc, port)
}

func chronycCSV(chronyc, port string, args ...string) string {
	out, _ := exec.Command(chronyc, append([]string{"-h", "127.0.0.1", "-p", port, "-c"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// matchChronyc holds what the client reads from the daemon on port to
// what chronyc prints for it.
func matchChronyc(t *testing.T, chronyc, port string) {
	t.Helper()
	ctl := func(args ...string) string { return chronycCSV(chronyc, port, args...) }
	c := &chrony.Client{Addr: "127.0.0.1:" + port}
	ctx := context.Background()

	// chronyc before and after, since a poll may land between the reads.
	same := func(what string, read func() string, want func() string) {
		t.Helper()
		before := want()
		got := read()
		if after := want(); got != before && got != after {
			t.Errorf("%s: read %q, chronyc printed %q then %q", what, got, before, after)
		}
		t.Logf("%s:\n%s", what, got)
	}
	// The correction still to apply and the dispersion change as they are
	// read, so they only have to lie between chronyc's two readings.
	var moving [2][]float64
	same("tracking", func() string {
		tr, err := c.Tracking(ctx)
		if err != nil {
			return err.Error()
		}
		moving[0] = append(moving[0], -tr.Offset)
		moving[1] = append(moving[1], tr.RootDispersion)
		ref := "0.000000000"
		if !tr.RefTime.IsZero() {
			ref = fmt.Sprintf("%d.%09d", tr.RefTime.Unix(), tr.RefTime.Nanosecond())
		}
		return fmt.Sprintf("%s,%d,%s,%.9f,%s", tr.Address, tr.Stratum, ref, tr.RootDelay, tr.Leap)
	}, func() string {
		f := strings.Split(ctl("-n", "tracking"), ",")
		if f[0] == "00000000" {
			f[1] = ""
		}
		for i, col := range []int{4, 11} {
			v, _ := strconv.ParseFloat(f[col], 64)
			moving[i] = append(moving[i], v)
		}
		return strings.Join([]string{f[1], f[2], f[3], f[10], f[13]}, ",")
	})
	for i, what := range []string{"correction", "root dispersion"} {
		// chronyc before, the client, chronyc after, each to 9 places.
		if m := moving[i]; len(m) == 3 && (m[1] < min(m[0], m[2])-1e-9 || m[1] > max(m[0], m[2])+1e-9) {
			t.Errorf("%s: read %.9f, chronyc printed %.9f then %.9f", what, m[1], m[0], m[2])
		}
	}

	states := map[string]string{"selected": "*", "combined": "+", "excluded": "-", "unusable": "?", "falseticker": "x", "jittery": "~"}
	same("sources", func() string {
		got, err := c.Sources(ctx)
		if err != nil {
			return err.Error()
		}
		var rows []string
		for _, s := range got {
			last := "4294967295"
			if s.LastRx >= 0 {
				last = strconv.Itoa(int(s.LastRx / time.Second))
			}
			rows = append(rows, fmt.Sprintf("%s,%s,%s,%d,%d,%o,%s,%.9f,%.9f", states[s.State], s.Address, s.Name, s.Stratum,
				int(math.Round(math.Log2(s.Poll.Seconds()))), s.Reach, last, s.Offset, s.Error))
		}
		return strings.Join(rows, "\n")
	}, func() string {
		byAddr, byName := rows(ctl("-n", "sources")), rows(ctl("-N", "sources"))
		var out []string
		for i, f := range byAddr {
			out = append(out, strings.Join([]string{f[1], f[2], byName[i][2], f[3], f[4], f[5], f[6], f[7], f[9]}, ","))
		}
		return strings.Join(out, "\n")
	})

	same("authdata", func() string {
		got, err := c.Auth(ctx)
		if errors.Is(err, chrony.ErrNotAuthorised) {
			return "501 Not authorised"
		}
		if err != nil {
			return err.Error()
		}
		var rows []string
		for _, a := range got {
			mode, last := a.Mode, "4294967295"
			if mode == "" {
				mode = "-"
			}
			if a.LastKE >= 0 {
				last = strconv.Itoa(int(a.LastKE / time.Second))
			}
			nak := 0
			if a.NAK {
				nak = 1
			}
			rows = append(rows, fmt.Sprintf("%s,%s,%s,%s,%d,%d,%d", a.Address, a.Name, mode, last, a.Attempts, nak, a.Cookies))
		}
		return strings.Join(rows, "\n")
	}, func() string {
		n := ctl("-n", "authdata")
		if strings.HasPrefix(n, "501 ") {
			return "501 Not authorised"
		}
		byAddr, byName := rows(n), rows(ctl("-N", "authdata"))
		var out []string
		for i, f := range byAddr {
			out = append(out, strings.Join([]string{f[0], byName[i][0], f[1], f[5], f[6], f[7], f[8]}, ","))
		}
		return strings.Join(out, "\n")
	})

	same("serverstats", func() string {
		st, err := c.ServerStats(ctx)
		if errors.Is(err, chrony.ErrNotAuthorised) {
			return "501 Not authorised"
		}
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf("%d,%d", st.NTPReceived, st.NTPDropped)
	}, func() string {
		out := ctl("-n", "serverstats")
		if strings.HasPrefix(out, "501 ") {
			return "501 Not authorised"
		}
		f := strings.Split(out, ",")
		return f[0] + "," + f[1]
	})
}

func rows(csv string) [][]string {
	var out [][]string
	for line := range strings.Lines(csv) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, strings.Split(line, ","))
		}
	}
	return out
}

// find looks where a package puts chrony, which a test's PATH may miss.
func find(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/usr/sbin", "/usr/bin"} {
		if p, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return p
		}
	}
	return ""
}
