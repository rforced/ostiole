package chrony

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Replies chronyd 4.9 sent a private daemon following the router's NTS
// servers, an address that never answers and a pool, beside what chronyc
// -c printed for the same state. Each reply is a whole datagram.
const (
	trackingReply = "0602000000210005000000000000000001020304000000000000000003dc2a2703dc2a27000000000000000000000000000100000003" +
		"0000000000006ab5e1590c452528f2afc560f1408d36f0bf72ca0ac5723ff2d923c80ce9fc6ffac8c08af0dde9b704d54820"
	nSourcesReply = "06020000000e000200000000000000000102030400000000000000000000000e"
	// chronyc: ^,-,194.58.205.196,1,6,37,6,-0.002077126,-0.002077126,0.070244834
	excludedReply = "06020000000f00030000000000000000010203040000000000000000c23acdc4000000000000000000000000" +
		"0001000000060001000500000000001f00000006f377df9cf377df9cfc8fdc86"
	// chronyc: ^,+,94.198.159.11,2,6,37,7,-0.002527710,-0.003988483,0.070126206
	combinedReply = "06020000000f000300000000000000000102030400000000000000005ec69f0b000000000000000000000000" +
		"0001000000060002000400000000001f00000007f57d4e2ef35a5810fc8f9e54"
	// chronyc: ^,*,3.220.42.39,2,6,37,6,0.000897395,-0.000563244,0.025176724
	selectedReply = "06020000000f0003000000000000000001020304000000000000000003dc2a27000000000000000000000000" +
		"0001000000060002000000000000001f00000006ef6c5956eeeb3f25f8ce3f6b"
	// chronyc: ^,?,192.0.2.123,0,7,0,4294967295,0.000000000,0.000000000,0.000000000
	silentReply = "06020000000f00030000000000000000010203040000000000000000c000027b000000000000000000000000" +
		"00010000000700000001000000000000ffffffff000000000000000000000000"
	// A name of a pool that has not resolved: chronyc leaves it out.
	unresolvedReply = "06020000000f000300000000000000000102030400000000000000000000000800000000000000000000000000030000" +
		"000600000001000000000000ffffffff000000000000000000000000"
	// The rest of the 284 bytes are zeros.
	nameReply = "0602000000410013000000000000000001020304000000000000000076697267696e69612e74696d652e73797374656d37362e636f6d"
	// chronyc: nts.netnod.se,NTS,1,15,256,76,0,0,8,100
	ntsAuthReply = "060200000043001400000000000000000102030400000000000000000002000f00000001010000000000004c0008006400000000"
	// chronyc: 192.0.2.123,-,0,0,0,4294967295,0,0,0,0
	plainAuthReply = "06020000004300140000000000000000010203040000000000000000000000000000000000000000ffffffff0000000000000000"
	// chronyc: 3,0,54,... before it and 3,0,171,... after: three NTP
	// requests served, none dropped, and the command count between them.
	serverStatsReply = "060200000036001900000000000000000102030400000000000000000000000000000003000000000000000000000000" +
		"0000005300000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000030000000000000003" +
		"000000000000000000000000000000000000000000000000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

// report checks a captured reply as the client checks one and returns
// its report.
func report(t *testing.T, cmd command, h string) []byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < replyHeader+cmd.rdata {
		b = append(b, make([]byte, replyHeader+cmd.rdata-len(b))...)
	}
	d, err := answer(cmd, b)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDecodeTrackingFromChronyd(t *testing.T) {
	t.Parallel()
	tr := decodeTracking(report(t, cmdTracking, trackingReply))
	if tr.Address != "3.220.42.39" || tr.Stratum != 3 || tr.Leap != "Normal" || !tr.Synchronised() {
		t.Errorf("tracking = %+v", tr)
	}
	if want := time.Unix(1790304601, 205858088).UTC(); !tr.RefTime.Equal(want) {
		t.Errorf("ref time = %v, want %v", tr.RefTime, want)
	}
	// chronyc said 0.002684387 s slow just before and 0.002681992 just
	// after; slow is negative here.
	if tr.Offset < -0.002684387 || tr.Offset > -0.002681992 {
		t.Errorf("offset = %.9f", tr.Offset)
	}
	if got := fmt.Sprintf("%.9f", tr.RootDelay); got != "0.049011745" {
		t.Errorf("root delay = %s", got)
	}
	if tr.RootDispersion < 0.001687340 || tr.RootDispersion > 0.001693209 {
		t.Errorf("root dispersion = %.9f", tr.RootDispersion)
	}
}

func TestDecodeSourcesFromChronyd(t *testing.T) {
	t.Parallel()
	if n := report(t, cmdNSources, nSourcesReply); n[3] != 14 {
		t.Errorf("sources = %d, want 14 with the unresolved", n[3])
	}
	chars := map[string]string{"selected": "*", "combined": "+", "excluded": "-", "unusable": "?", "falseticker": "x", "jittery": "~"}
	for _, tc := range []struct{ reply, csv string }{
		{excludedReply, "^,-,194.58.205.196,1,6,37,6,-0.002077126,-0.002077126,0.070244834"},
		{combinedReply, "^,+,94.198.159.11,2,6,37,7,-0.002527710,-0.003988483,0.070126206"},
		{selectedReply, "^,*,3.220.42.39,2,6,37,6,0.000897395,-0.000563244,0.025176724"},
		{silentReply, "^,?,192.0.2.123,0,7,0,4294967295,0.000000000,0.000000000,0.000000000"},
	} {
		s, ok := decodeSource(report(t, cmdSourceData, tc.reply))
		if !ok {
			t.Fatalf("%s left out", tc.csv)
		}
		last := "4294967295"
		if s.LastRx >= 0 {
			last = strconv.Itoa(int(s.LastRx / time.Second))
		}
		// Every column chronyc prints but the measured offset, which the
		// page does not show.
		want := strings.Split(tc.csv, ",")
		want = append(want[:8], want[9])
		got := []string{"^", chars[s.State], s.Address, strconv.Itoa(s.Stratum),
			strconv.Itoa(int(math.Log2(s.Poll.Seconds()))), strconv.FormatUint(uint64(s.Reach), 8), last,
			fmt.Sprintf("%.9f", s.Offset), fmt.Sprintf("%.9f", s.Error)}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("decoded %s, chronyc printed %s", strings.Join(got, ","), strings.Join(want, ","))
		}
	}
	if s, ok := decodeSource(report(t, cmdSourceData, unresolvedReply)); ok {
		t.Errorf("an unresolved name was listed: %+v", s)
	}
}

func TestDecodeNameFromChronyd(t *testing.T) {
	t.Parallel()
	if name, ok := decodeName(report(t, cmdSourceName, nameReply)); !ok || name != "virginia.time.system76.com" {
		t.Errorf("name = %q, %v", name, ok)
	}
	for _, bad := range []string{"", "a name", "no\x01end", strings.Repeat("x", 256)} {
		d := make([]byte, 256)
		copy(d, bad)
		if name, ok := decodeName(d); ok {
			t.Errorf("%q read as %q", bad, name)
		}
	}
}

func TestDecodeAuthFromChronyd(t *testing.T) {
	t.Parallel()
	nts := decodeAuth(report(t, cmdAuthData, ntsAuthReply))
	if nts.Mode != "NTS" || nts.LastKE != 76*time.Second || nts.Attempts != 0 || nts.NAK || nts.Cookies != 8 {
		t.Errorf("an NTS source = %+v", nts)
	}
	if plain := decodeAuth(report(t, cmdAuthData, plainAuthReply)); plain.Mode != "" || plain.LastKE >= 0 || plain.Cookies != 0 {
		t.Errorf("an unsigned source = %+v", plain)
	}
}

func TestDecodeServerStatsFromChronyd(t *testing.T) {
	t.Parallel()
	if st := decodeServerStats(report(t, cmdServerStats, serverStatsReply)); st.NTPReceived != 3 || st.NTPDropped != 0 {
		t.Errorf("server stats = %+v", st)
	}
}

// The first of every datagram chronyd sends back is checked before its
// report is read.
func TestAnswerRefusesWhatDoesNotFit(t *testing.T) {
	t.Parallel()
	ok, err := hex.DecodeString(trackingReply)
	if err != nil {
		t.Fatal(err)
	}
	change := func(f func(b []byte)) []byte {
		b := append([]byte(nil), ok...)
		f(b)
		return b
	}
	for name, b := range map[string][]byte{
		"not opened":      change(func(b []byte) { b[9] = statusUnauthorised }),
		"no such source":  change(func(b []byte) { b[9] = statusNoSuchSource }),
		"another version": change(func(b []byte) { b[0] = 5 }),
		"another report":  change(func(b []byte) { b[7] = 6 }),
		"short":           ok[:len(ok)-1],
	} {
		if _, err := answer(cmdTracking, b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := answer(cmdTracking, change(func(b []byte) { b[9] = statusUnauthorised })); !strings.Contains(err.Error(), ErrNotAuthorised.Error()) {
		t.Errorf("not opened: %v", err)
	}
}

// Padded to the reply, or chronyd drops the request unanswered; these are
// the lengths chronyd 4.9 requires, from offsetof over candm.h.
func TestRequestLengths(t *testing.T) {
	t.Parallel()
	for cmd, want := range map[command]int{
		cmdNSources: 32, cmdSourceData: 76, cmdTracking: 104,
		cmdServerStats: 196, cmdSourceName: 284, cmdAuthData: 52,
	} {
		if got := cmd.length(); got != want {
			t.Errorf("%s (%d) = %d bytes, want %d", cmd.name, cmd.code, got, want)
		}
	}
}

func TestDecodeFloat(t *testing.T) {
	t.Parallel()
	for word, want := range map[uint32]float64{
		0x00000000: 0,
		0x02000000: 0,  // an exponent with no coefficient is still zero
		0x04800000: 1,  // 2^23 × 2^(2−25)
		0x03000000: -1, // −2^24 × 2^(1−25)
		0xf377df9c: -0.002077126,
		0xfc8fdc86: 0.070244834,
	} {
		b := []byte{byte(word >> 24), byte(word >> 16), byte(word >> 8), byte(word)}
		if got := decodeFloat(b); math.Abs(got-want) > 5e-10 {
			t.Errorf("%08x = %.9f, want %.9f", word, got, want)
		}
	}
}
