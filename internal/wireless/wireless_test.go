package wireless

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func ax210(t *testing.T) Phy {
	t.Helper()
	raw, err := os.ReadFile("testdata/ax210-phy.txt")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	p, err := ParsePhy(string(raw))
	if err != nil {
		t.Fatalf("ParsePhy: %v", err)
	}
	return p
}

func TestParsePhyReadsTheAX210(t *testing.T) {
	t.Parallel()
	p := ax210(t)
	if p.Name != "phy0" {
		t.Errorf("Name = %q, want phy0", p.Name)
	}
	if p.APLimit != 1 {
		t.Errorf("APLimit = %d, want 1", p.APLimit)
	}
	if len(p.Bands) != 3 {
		t.Fatalf("bands = %d, want 3", len(p.Bands))
	}
	for _, b := range model.Bands {
		if !p.Bands[b].HE {
			t.Errorf("band %s has no HE", b)
		}
	}
	five := p.Bands[model.Band5G]
	if five.MaxWidth != 160 {
		t.Errorf("5 GHz MaxWidth = %d, want 160", five.MaxWidth)
	}
	if got := p.Bands[model.Band2G].MaxWidth; got != 40 {
		t.Errorf("2.4 GHz MaxWidth = %d, want 40", got)
	}
	c36, ok := five.Channel(36)
	if !ok || c36.MHz != 5180 || c36.MaxDBm != 22 || c36.Radar {
		t.Errorf("channel 36 = %+v, %v", c36, ok)
	}
	if c52, _ := five.Channel(52); !c52.Radar {
		t.Errorf("channel 52 = %+v, want radar", c52)
	}
	if !p.Supports(model.Band6G) || p.Bands[model.Band6G].HT {
		t.Errorf("6 GHz band = %+v", p.Bands[model.Band6G])
	}

	wantHT := "[LDPC][SHORT-GI-20][SHORT-GI-40][TX-STBC][RX-STBC1][MAX-AMSDU-7935][DSSS_CCK-40]"
	if got := strings.Join(five.HTCapab, ""); got != wantHT {
		t.Errorf("ht_capab = %q, want %q", got, wantHT)
	}
	wantVHT := "[MAX-MPDU-11454][VHT160][RXLDPC][SHORT-GI-80][SHORT-GI-160][TX-STBC-2BY1][SU-BEAMFORMEE][MU-BEAMFORMEE]"
	if got := strings.Join(five.VHTCapab, ""); got != wantVHT {
		t.Errorf("vht_capab = %q, want %q", got, wantVHT)
	}
}

// The AX210 prints no regulatory flags on its frequencies; a card whose
// domain is set does.
func TestParsePhyReadsRegulatoryFlags(t *testing.T) {
	t.Parallel()
	p, err := ParsePhy(`Wiphy phy1
	Supported interface modes:
		 * managed
		 * AP
	Band 2:
		Capabilities: 0x19ef
			RX LDPC
		Frequencies:
			* 5180.0 MHz [36] (22.0 dBm)
			* 5260.0 MHz [52] (20.0 dBm) (no IR, radar detection)
			* 5885.0 MHz [177] (disabled)
	valid interface combinations:
		 * #{ managed } <= 1, #{ AP } <= 4, total <= 4, #channels <= 1
`)
	if err != nil {
		t.Fatalf("ParsePhy: %v", err)
	}
	if p.APLimit != 4 {
		t.Errorf("APLimit = %d, want 4", p.APLimit)
	}
	five := p.Bands[model.Band5G]
	if c, _ := five.Channel(52); !c.Radar || !c.NoIR || c.MaxDBm != 20 {
		t.Errorf("channel 52 = %+v", c)
	}
	if c, _ := five.Channel(177); !c.Disabled {
		t.Errorf("channel 177 = %+v", c)
	}
	if five.MaxWidth != 40 {
		t.Errorf("MaxWidth = %d, want 40", five.MaxWidth)
	}
}

func network(name, ssid string, sec model.Security, pass string) model.Interface {
	return model.Interface{
		Name: name, Enabled: true,
		Wireless: &model.WirelessNetwork{Radio: "wlp3s0", SSID: ssid, Security: sec, Passphrase: pass},
	}
}

func TestRenderGolden(t *testing.T) {
	t.Parallel()
	phy := ax210(t)
	guest := network("ap1", "ostiole-guest", model.SecurityWPA3, "guests get their own")
	guest.Wireless.Isolate = true
	guest.Wireless.MaxClients = 32
	hidden := network("ap0", "ostiole-2g", model.SecurityWPA2, "correct horse battery")
	hidden.Wireless.Hidden = true

	cases := []struct {
		name   string
		radio  model.Radio
		nets   []model.Interface
		master map[string]string
	}{
		{
			name:  "2g",
			radio: model.Radio{Name: "wlp3s0", Enabled: true, Band: model.Band2G, Channel: 6, Width: 40, Standard: model.StandardN},
			nets:  []model.Interface{hidden, network("ap1", "ostiole-open", model.SecurityOpen, "")},
		},
		{
			name:   "5g",
			radio:  model.Radio{Name: "wlp3s0", Enabled: true, Band: model.Band5G, Channel: 36, Width: 80, Standard: model.StandardAX},
			nets:   []model.Interface{network("ap0", "ostiole-lan", model.SecurityMixed, "correct horse battery"), guest},
			master: map[string]string{"ap0": "br-lan"},
		},
		{
			name:  "5g-acs",
			radio: model.Radio{Name: "wlp3s0", Enabled: true, Band: model.Band5G, Width: 80, Standard: model.StandardAX},
			nets:  []model.Interface{network("ap0", "ostiole-open", model.SecurityOWE, "")},
		},
		{
			name:  "6g",
			radio: model.Radio{Name: "wlp3s0", Enabled: true, Band: model.Band6G, Channel: 37, Width: 80, Standard: model.StandardAX, Power: 12},
			nets:  []model.Interface{network("ap0", "ostiole-6", model.SecurityWPA3, "correct horse battery")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Render("US", tc.radio, tc.nets, &phy, tc.master) +
				"\n--- env ---\n" + Env("US", tc.radio, &phy, tc.nets)
			golden := filepath.Join("testdata", "render-"+tc.name+".conf")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("mismatch (run with -update to accept)\n--- got ---\n%s", got)
			}
		})
	}
}

func TestRenderWithoutAPhyLeavesOutCapabilities(t *testing.T) {
	t.Parallel()
	r := model.Radio{Name: "wlp3s0", Enabled: true, Band: model.Band5G, Channel: 36, Width: 80, Standard: model.StandardAX}
	got := Render("GB", r, []model.Interface{network("ap0", "ostiole", model.SecurityMixed, "correct horse battery")}, nil, nil)
	for _, s := range []string{"ht_capab", "vht_capab"} {
		if strings.Contains(got, s) {
			t.Errorf("%s rendered without a phy:\n%s", s, got)
		}
	}
	for _, s := range []string{"channel=36", "vht_oper_centr_freq_seg0_idx=42", "country_code=GB"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %s:\n%s", s, got)
		}
	}
	if env := Env("GB", r, nil, []model.Interface{network("ap0", "ostiole", model.SecurityMixed, "correct horse battery")}); !strings.Contains(env, "PHY=phy0\n") || !strings.Contains(env, "TXPOWER=auto\n") {
		t.Errorf("env = %q", env)
	}
}

func TestEnvRoundTrips(t *testing.T) {
	t.Parallel()
	r := model.Radio{Name: "wlp3s0", Enabled: true, Band: model.Band2G, Channel: 6, Width: 40, Standard: model.StandardN, Power: 12}
	nets := []model.Interface{network("ap0", "a", model.SecurityOpen, ""), network("ap1", "b", model.SecurityOpen, "")}
	got := ParseEnv(Env("DE", r, &Phy{Name: "phy1"}, nets))
	want := EnvPlan{PHY: "phy1", AP: "ap0", Country: "DE", Band: model.Band2G, Channel: 6, Width: 40, Standard: model.StandardN, Networks: 2}
	if got != want {
		t.Errorf("env = %+v, want %+v", got, want)
	}
}

func TestParseStations(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/station-dump.txt")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	got := ParseStations(string(raw))
	if len(got) != 2 {
		t.Fatalf("stations = %d, want 2", len(got))
	}
	first := got[0]
	want := Station{
		MAC: "02:1a:2b:3c:4d:5e", Interface: "ap0",
		ConnectedSeconds: 1287, InactiveMS: 10,
		RxBytes: 1043821, TxBytes: 18472913, RxPackets: 9274, TxPackets: 16204,
		SignalDBm:  -52,
		TxBitrate:  "866.7 MBit/s VHT-MCS 9 80MHz short GI VHT-NSS 2",
		RxBitrate:  "780.0 MBit/s VHT-MCS 8 80MHz short GI VHT-NSS 2",
		Authorized: true,
	}
	if first != want {
		t.Errorf("station = %+v\nwant     %+v", first, want)
	}
	if got[1].Authorized || got[1].SignalDBm != -71 {
		t.Errorf("second station = %+v", got[1])
	}
}

func TestParseDevInfo(t *testing.T) {
	t.Parallel()
	got, err := ParseDevInfo(`Interface ap0
	ifindex 7
	wdev 0x2
	addr 02:1a:2b:3c:4d:f9
	ssid ostiole-lan
	type AP
	wiphy 0
	channel 40 (5200 MHz), width: 80 MHz, center1: 5210 MHz
	txpower 22.00 dBm
	multicast TXQ:
		qsz-byt	qsz-pkt	flows
		0	0	0
`)
	if err != nil {
		t.Fatalf("ParseDevInfo: %v", err)
	}
	want := DevInfo{Interface: "ap0", SSID: "ostiole-lan", Type: "AP", Channel: 40, MHz: 5200, Width: 80, TxPowerDBm: 22}
	if got != want {
		t.Errorf("dev = %+v, want %+v", got, want)
	}
	if _, err := ParseDevInfo("nothing here\n"); err == nil {
		t.Error("expected an error without an interface")
	}
}

func TestRegCountry(t *testing.T) {
	t.Parallel()
	global, phys := RegCountry(`global
country 00: DFS-UNSET
	(2402 - 2472 @ 40), (N/A, 20), (N/A)

phy#0 (self-managed)
country US: DFS-FCC
	(2402 - 2472 @ 40), (N/A, 30), (N/A)
`)
	if global != "00" {
		t.Errorf("global = %q, want 00", global)
	}
	if phys["phy0"] != "US" {
		t.Errorf("phys = %v, want phy0 US", phys)
	}
}

func TestChannelCentres(t *testing.T) {
	t.Parallel()
	cases := []struct {
		band          model.Band
		channel       int
		width         int
		want          int
		ok            bool
		wantSecondary string
	}{
		{model.Band5G, 36, 80, 42, true, "[HT40+]"},
		{model.Band5G, 40, 80, 42, true, "[HT40-]"},
		{model.Band5G, 149, 80, 155, true, "[HT40+]"},
		{model.Band5G, 161, 160, 163, true, "[HT40-]"},
		{model.Band5G, 100, 160, 114, true, "[HT40+]"},
		{model.Band5G, 36, 40, 0, false, "[HT40+]"},
		{model.Band5G, 0, 80, 0, false, "[HT40+]"},
		{model.Band6G, 1, 80, 7, true, "[HT40+]"},
		{model.Band6G, 37, 80, 39, true, "[HT40-]"},
		{model.Band6G, 1, 160, 15, true, "[HT40+]"},
		{model.Band2G, 6, 40, 0, false, "[HT40+]"},
		{model.Band2G, 11, 40, 0, false, "[HT40-]"},
	}
	for _, tc := range cases {
		got, ok := centre(tc.band, tc.channel, tc.width)
		if got != tc.want || ok != tc.ok {
			t.Errorf("centre(%s, %d, %d) = %d, %v; want %d, %v",
				tc.band, tc.channel, tc.width, got, ok, tc.want, tc.ok)
		}
		if s := secondary(tc.band, tc.channel); s != tc.wantSecondary {
			t.Errorf("secondary(%s, %d) = %s, want %s", tc.band, tc.channel, s, tc.wantSecondary)
		}
	}
}

// A band serves when a channel on it is neither disabled nor no-IR, except
// 6 GHz on an Intel card, which never opens for an access point.
func TestPhyServes(t *testing.T) {
	t.Parallel()
	shut := BandInfo{Channels: []Channel{{Number: 1, NoIR: true}, {Number: 5, Disabled: true}}}
	open := BandInfo{Channels: []Channel{{Number: 1, NoIR: true}, {Number: 5}}}
	if shut.Serves() || !open.Serves() {
		t.Errorf("Serves: shut=%v open=%v", shut.Serves(), open.Serves())
	}
	intel := Phy{Driver: "iwlwifi", Bands: map[model.Band]BandInfo{model.Band5G: open, model.Band6G: open}}
	if !intel.Serves(model.Band5G) || intel.Serves(model.Band6G) || intel.Serves(model.Band2G) {
		t.Errorf("intel serves 5g=%v 6g=%v 2g=%v", intel.Serves(model.Band5G), intel.Serves(model.Band6G), intel.Serves(model.Band2G))
	}
	other := Phy{Driver: "mt7915e", Bands: map[model.Band]BandInfo{model.Band6G: open}}
	if !other.Serves(model.Band6G) {
		t.Error("a card that may transmit on 6 GHz does not serve it")
	}
	p := ax210(t)
	p.Driver = "iwlwifi"
	if p.Serves(model.Band6G) || !p.Serves(model.Band5G) {
		t.Errorf("AX210 serves 6g=%v 5g=%v", p.Serves(model.Band6G), p.Serves(model.Band5G))
	}
}
