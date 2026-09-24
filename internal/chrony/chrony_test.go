package chrony

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtures answers like chronyc from the captures in testdata, which came
// from chrony 4.9 following four NTS servers, an address that never
// answers and a pool.
func fixtures(t *testing.T, files map[string]string) *Client {
	t.Helper()
	return &Client{Bin: "chronyc", Run: func(_ context.Context, bin string, args ...string) ([]byte, error) {
		if bin != "chronyc" || len(args) != 5 || args[0] != "-h" || args[1] != "127.0.0.1" || args[3] != "-c" {
			t.Fatalf("unexpected command %s %v", bin, args)
		}
		name, ok := files[args[4]+" "+args[2]]
		if !ok {
			return []byte("501 Not authorised\n"), errors.New("exit status 1")
		}
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return raw, nil
	}}
}

var all = map[string]string{
	"tracking -n":    "tracking.csv",
	"sources -n":     "sources-n.csv",
	"sources -N":     "sources-N.csv",
	"authdata -n":    "authdata-n.csv",
	"authdata -N":    "authdata-N.csv",
	"serverstats -n": "serverstats.csv",
}

func TestTracking(t *testing.T) {
	t.Parallel()
	tr, err := fixtures(t, all).Tracking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Synchronised() || tr.Address != "3.220.42.39" || tr.Stratum != 3 {
		t.Errorf("tracking = %+v", tr)
	}
	// chronyc says the clock is 3 ms slow; ahead is the positive direction.
	if tr.Offset > -0.003 || tr.Offset < -0.0031 {
		t.Errorf("offset = %v, want about -0.00306", tr.Offset)
	}
	if want := time.Date(2026, 9, 24, 22, 2, 50, 868255935, time.UTC); tr.RefTime.Sub(want).Abs() > time.Microsecond {
		t.Errorf("ref time = %v, want %v", tr.RefTime, want)
	}
	if tr.RootDelay != 0.049001947 || tr.RootDispersion != 0.019441204 {
		t.Errorf("root delay and dispersion = %v, %v", tr.RootDelay, tr.RootDispersion)
	}
}

func TestTrackingBeforeTheFirstSync(t *testing.T) {
	t.Parallel()
	tr, err := fixtures(t, map[string]string{"tracking -n": "tracking-unsynchronised.csv"}).Tracking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tr.Synchronised() || tr.Address != "" || !tr.RefTime.IsZero() || tr.Leap != "Not synchronised" {
		t.Errorf("tracking = %+v", tr)
	}
}

func TestSourcesCarryTheConfiguredNames(t *testing.T) {
	t.Parallel()
	got, err := fixtures(t, all).Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("got %d sources, want 7", len(got))
	}
	sel := got[3]
	if sel.Name != "virginia.time.system76.com" || sel.Address != "3.220.42.39" || sel.State != "selected" {
		t.Errorf("selected source = %+v", sel)
	}
	if sel.Poll != 64*time.Second || sel.Reach != 0o17 || sel.LastRx != 28*time.Second || sel.Stratum != 2 {
		t.Errorf("selected source timing = %+v", sel)
	}
	if sel.Offset != -0.000095551 || sel.Error != 0.026133047 {
		t.Errorf("selected source offset = %v ± %v", sel.Offset, sel.Error)
	}
	dead := got[4]
	if dead.State != "unusable" || dead.LastRx >= 0 || dead.Reach != 0 {
		t.Errorf("a source that never answered = %+v", dead)
	}
	// Every server of a pool goes by the pool's name.
	if got[5].Name != "2.pool.ntp.org" || got[6].Name != "2.pool.ntp.org" || got[5].Address == got[6].Address {
		t.Errorf("pool servers = %+v, %+v", got[5], got[6])
	}
}

func TestAuthShowsWhichSourcesAreSigned(t *testing.T) {
	t.Parallel()
	got, err := fixtures(t, all).Auth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("got %d rows, want 7", len(got))
	}
	nl := got[1]
	if nl.Name != "nts.time.nl" || nl.Mode != "NTS" || nl.Cookies != 8 || nl.NAK || nl.Attempts != 0 || nl.LastKE != 75*time.Second {
		t.Errorf("an NTS source = %+v", nl)
	}
	if plain := got[4]; plain.Mode != "" || plain.LastKE >= 0 {
		t.Errorf("an unsigned source = %+v", plain)
	}
}

func TestServerStats(t *testing.T) {
	t.Parallel()
	st, err := fixtures(t, all).ServerStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.NTPReceived != 1234 || st.NTPDropped != 5 {
		t.Errorf("server stats = %+v", st)
	}
}

// Before 4.7 chronyd cannot open authdata or serverstats to the command
// port, and chronyc says so on stdout.
func TestClosedCommandsSayNotAuthorised(t *testing.T) {
	t.Parallel()
	c := fixtures(t, map[string]string{"tracking -n": "tracking.csv"})
	if _, err := c.Auth(context.Background()); !errors.Is(err, ErrNotAuthorised) {
		t.Errorf("authdata err = %v, want ErrNotAuthorised", err)
	}
	if _, err := c.ServerStats(context.Background()); !errors.Is(err, ErrNotAuthorised) {
		t.Errorf("serverstats err = %v, want ErrNotAuthorised", err)
	}
}

func TestNoTool(t *testing.T) {
	t.Parallel()
	if _, err := (&Client{}).Tracking(context.Background()); !errors.Is(err, ErrNoTool) {
		t.Errorf("err = %v, want ErrNoTool", err)
	}
}

func TestADaemonThatIsNotRunning(t *testing.T) {
	t.Parallel()
	c := &Client{Bin: "chronyc", Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte("506 Cannot talk to daemon\n"), errors.New("exit status 1")
	}}
	_, err := c.Tracking(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Cannot talk to daemon") {
		t.Errorf("err = %v", err)
	}
}

func TestParseVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		out  string
		want Version
	}{
		{
			"chronyd (chrony) version 4.9 (+CMDMON +REFCLOCK +RTC +PRIVDROP +SCFILTER +SIGND +NTS +SECHASH +IPV6 +DEBUG)\n",
			Version{Major: 4, Minor: 9, NTS: true},
		},
		{
			"chronyd (chrony) version 4.6.1 (+CMDMON +NTP +REFCLOCK +RTC +PRIVDROP +SCFILTER +SIGND +ASYNCDNS +NTS +SECHASH +IPV6 -DEBUG)\n",
			Version{Major: 4, Minor: 6, Patch: 1, NTS: true},
		},
		{
			"chronyd (chrony) version 4.5 (+CMDMON +NTP +REFCLOCK +RTC -PRIVDROP -SCFILTER -SIGND +ASYNCDNS -NTS -SECHASH +IPV6 -DEBUG)\n",
			Version{Major: 4, Minor: 5},
		},
	} {
		got, err := ParseVersion(tc.out)
		if err != nil || got != tc.want {
			t.Errorf("ParseVersion(%q) = %+v, %v; want %+v", tc.out, got, err, tc.want)
		}
	}
	if _, err := ParseVersion("chronyd: command not found"); err == nil {
		t.Error("a reply with no version parsed")
	}
	v := Version{Major: 4, Minor: 6, Patch: 1}
	if v.AtLeast(4, 7) || !v.AtLeast(4, 6) || !v.AtLeast(3, 9) || v.String() != "4.6.1" {
		t.Errorf("4.6.1: AtLeast or String wrong")
	}
	if (Version{Major: 4, Minor: 7}).String() != "4.7" || (Version{}).String() != "" {
		t.Error("String of 4.7 or of nothing is wrong")
	}
}
