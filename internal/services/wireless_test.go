package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/wireless"
)

// ax210 is the card the parser's fixture came from: one access point at a
// time, three bands.
func ax210(t *testing.T) *wireless.Phy {
	t.Helper()
	raw, err := os.ReadFile("../wireless/testdata/ax210-phy.txt")
	if err != nil {
		t.Fatal(err)
	}
	p, err := wireless.ParsePhy(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	p.Name, p.Driver = "phy0", "iwlwifi"
	return &p
}

// newWireless is a backend writing into a temporary directory, with the
// card and the regulatory domain faked.
func newWireless(t *testing.T, phy *wireless.Phy) (*Wireless, *fakeCmd, *string) {
	t.Helper()
	cmd := &fakeCmd{installed: true}
	country := new(string)
	w := &Wireless{
		Dir: filepath.Join(t.TempDir(), "wireless"),
		Cmd: cmd,
		Probe: func(context.Context, string) (*wireless.Phy, error) {
			if phy == nil {
				return nil, errors.New("no such device")
			}
			return phy, nil
		},
		Reg:       func(_ context.Context, c string) error { *country = c; return nil },
		Bluetooth: func() bool { return false },
	}
	return w, cmd, country
}

// withBand is a copy of the card with one band altered; the tests share
// the parsed card, so none of them may write to it.
func withBand(phy *wireless.Phy, band model.Band, alter func(*wireless.BandInfo)) *wireless.Phy {
	cut := *phy
	cut.Bands = map[model.Band]wireless.BandInfo{}
	for b, info := range phy.Bands {
		cut.Bands[b] = info
	}
	info := cut.Bands[band]
	alter(&info)
	cut.Bands[band] = info
	return &cut
}

// oneNetwork drops the guest network from the fixture, which has two on a
// card that serves one.
func oneNetwork(t *testing.T) *model.Config {
	t.Helper()
	cfg := loadConfig(t, "testdata/wireless.json")
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "ap1" {
			cfg.Interfaces[i].Enabled = false
		}
	}
	return cfg
}

func TestWirelessRenderWritesAConfAndAnEnvPerRadio(t *testing.T) {
	t.Parallel()
	w, _, _ := newWireless(t, ax210(t))
	files, err := w.Render(loadConfig(t, "testdata/wireless.json"))
	if err != nil {
		t.Fatal(err)
	}
	conf, ok := files["wlp3s0.conf"]
	if !ok {
		t.Fatalf("files = %v", files.Names())
	}
	for _, want := range []string{"interface=ap0", "bridge=br-lan", "bss=ap1", "country_code=US", "channel=36"} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q:\n%s", want, conf)
		}
	}
	if env := files["wlp3s0.env"]; !strings.Contains(env, "PHY=phy0\n") || !strings.Contains(env, "NETWORKS=2\n") {
		t.Errorf("env = %q", env)
	}
}

func TestWirelessRendersNothingWithoutARadio(t *testing.T) {
	t.Parallel()
	w, cmd, _ := newWireless(t, ax210(t))
	files, err := w.Render(loadConfig(t, "testdata/full.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("rendered %v", files.Names())
	}
	// A router that has never had a radio gets no directory and no
	// systemctl out of an apply.
	if err := w.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 0 {
		t.Errorf("ran %v", cmd.calls)
	}
	if _, err := os.Stat(w.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory was created anyway: %v", err)
	}
}

func TestWirelessApplyStartsChangesAndStops(t *testing.T) {
	t.Parallel()
	w, cmd, country := newWireless(t, ax210(t))
	cfg := oneNetwork(t)
	files, err := w.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if *country != "US" {
		t.Errorf("regulatory domain = %q", *country)
	}
	if !cmd.has("systemctl", "enable", "--now", "ostiole-hostapd@wlp3s0.service") {
		t.Errorf("calls = %v", cmd.calls)
	}
	if cmd.has("systemctl", "restart", "ostiole-hostapd@wlp3s0.service") {
		t.Errorf("a fresh apply restarted what it had just started: %v", cmd.calls)
	}
	raw, err := os.ReadFile(filepath.Join(w.Dir, "wlp3s0.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(w.Dir, "wlp3s0.conf")); info.Mode().Perm() != 0o600 {
		t.Errorf("the passphrase is readable: %v", info.Mode())
	}
	if !strings.Contains(string(raw), "ssid=ostiole-lan") {
		t.Errorf("conf = %s", raw)
	}

	// Nothing changed: no restart.
	cmd.calls = nil
	if err := w.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 2 {
		t.Errorf("an apply that changed nothing ran %v", cmd.calls)
	}

	// The SSID changed: restart.
	cmd.calls = nil
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "ap0" {
			cfg.Interfaces[i].Wireless.SSID = "ostiole-home"
		}
	}
	changed, err := w.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Apply(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "restart", "ostiole-hostapd@wlp3s0.service") {
		t.Errorf("calls = %v", cmd.calls)
	}

	// The radio went away: stop it and take both files with it.
	cmd.calls = nil
	if err := w.Apply(t.Context(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "disable", "--now", "ostiole-hostapd@wlp3s0.service") {
		t.Errorf("calls = %v", cmd.calls)
	}
	for _, name := range []string{"wlp3s0.conf", "wlp3s0.env"} {
		if _, err := os.Stat(filepath.Join(w.Dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still here: %v", name, err)
		}
	}
}

func TestWirelessPreflightRefusals(t *testing.T) {
	t.Parallel()
	phy := ax210(t)
	render := func(t *testing.T, w *Wireless, cfg *model.Config) network.Files {
		t.Helper()
		files, err := w.Render(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return files
	}

	t.Run("nothing wanted", func(t *testing.T) {
		t.Parallel()
		w, cmd, _ := newWireless(t, phy)
		cmd.installed = false
		if err := w.Preflight(t.Context(), network.Files{}); err != nil {
			t.Errorf("an apply with no radio was refused: %v", err)
		}
	})

	t.Run("not installed", func(t *testing.T) {
		t.Parallel()
		w, cmd, _ := newWireless(t, phy)
		cmd.installed = false
		err := w.Preflight(t.Context(), render(t, w, oneNetwork(t)))
		if err == nil || !strings.Contains(err.Error(), "repair --wireless") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("bluetooth loaded", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		w.Bluetooth = func() bool { return true }
		err := w.Preflight(t.Context(), render(t, w, oneNetwork(t)))
		if err == nil || !strings.Contains(err.Error(), "Bluetooth") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("no such radio", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		files := render(t, w, oneNetwork(t))
		w.Probe = func(context.Context, string) (*wireless.Phy, error) { return nil, errors.New("gone") }
		err := w.Preflight(t.Context(), files)
		if err == nil || !strings.Contains(err.Error(), "wlp3s0 is not on this router") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("too many networks", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		err := w.Preflight(t.Context(), render(t, w, loadConfig(t, "testdata/wireless.json")))
		if err == nil || !strings.Contains(err.Error(), "at most 1 network(s)") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("band the card has not got", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		cfg := oneNetwork(t)
		cfg.Wireless.Radios[0].Band = model.Band6G
		cfg.Wireless.Radios[0].Channel = 37
		files := render(t, w, cfg)
		w.Probe = func(context.Context, string) (*wireless.Phy, error) {
			cut := *phy
			cut.Bands = map[model.Band]wireless.BandInfo{model.Band5G: phy.Bands[model.Band5G]}
			return &cut, nil
		}
		err := w.Preflight(t.Context(), files)
		if err == nil || !strings.Contains(err.Error(), "no 6g band") {
			t.Errorf("err = %v", err)
		}
	})

	// The AX210 has a 6 GHz band and marks every channel on it no-IR, as
	// it does on a real router after learning its country.
	t.Run("a band the card may not transmit on", func(t *testing.T) {
		t.Parallel()
		w, _, country := newWireless(t, phy)
		cfg := oneNetwork(t)
		cfg.Wireless.Radios[0].Band = model.Band6G
		cfg.Wireless.Radios[0].Channel = 0
		for i := range cfg.Interfaces {
			if cfg.Interfaces[i].Wireless != nil {
				cfg.Interfaces[i].Wireless.Security = model.SecurityWPA3
			}
		}
		err := w.Preflight(t.Context(), render(t, w, cfg))
		if err == nil || !strings.Contains(err.Error(), "may not transmit on 6g") {
			t.Errorf("err = %v", err)
		}
		if *country != "US" {
			t.Errorf("the preflight probed before setting the country: %q", *country)
		}
	})

	t.Run("wider than the card goes", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		cfg := oneNetwork(t)
		cfg.Wireless.Radios[0].Width = 160
		files := render(t, w, cfg)
		w.Probe = func(context.Context, string) (*wireless.Phy, error) {
			return withBand(phy, model.Band5G, func(b *wireless.BandInfo) { b.MaxWidth = 80 }), nil
		}
		err := w.Preflight(t.Context(), files)
		if err == nil || !strings.Contains(err.Error(), "does not go above 80 MHz") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a standard the card has not got", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		files := render(t, w, oneNetwork(t))
		w.Probe = func(context.Context, string) (*wireless.Phy, error) {
			return withBand(phy, model.Band5G, func(b *wireless.BandInfo) { b.HE = false }), nil
		}
		err := w.Preflight(t.Context(), files)
		if err == nil || !strings.Contains(err.Error(), "does not do ax on 5g") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("radar channel", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		cfg := oneNetwork(t)
		cfg.Wireless.Radios[0].Channel = 52
		err := w.Preflight(t.Context(), render(t, w, cfg))
		if err == nil || !strings.Contains(err.Error(), "radar detection") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a card that serves none", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		files := render(t, w, oneNetwork(t))
		w.Probe = func(context.Context, string) (*wireless.Phy, error) {
			cut := *phy
			cut.Modes = []string{"managed"}
			return &cut, nil
		}
		err := w.Preflight(t.Context(), files)
		if err == nil || !strings.Contains(err.Error(), "cannot serve a network") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("a good one", func(t *testing.T) {
		t.Parallel()
		w, _, _ := newWireless(t, phy)
		if err := w.Preflight(t.Context(), render(t, w, oneNetwork(t))); err != nil {
			t.Errorf("a radio this card can serve was refused: %v", err)
		}
	})
}

func TestWirelessUnitNamesTheInstance(t *testing.T) {
	t.Parallel()
	if got := WirelessUnitFor("wlp3s0"); got != "ostiole-hostapd@wlp3s0.service" {
		t.Errorf("unit = %q", got)
	}
	unit := WirelessUnitContent("/usr/sbin/hostapd", "/usr/sbin/iw", "/usr/sbin/ip", "/etc/ostiole/wireless")
	for _, want := range []string{
		"EnvironmentFile=/etc/ostiole/wireless/%i.env",
		"ExecStart=/usr/sbin/hostapd /etc/ostiole/wireless/%i.conf",
		"ExecStartPre=-/usr/sbin/iw dev %i scan",
		"ExecStartPre=-/usr/sbin/iw phy ${PHY} set txpower $TXPOWER",
		"RuntimeDirectory=hostapd/%i",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("missing %q:\n%s", want, unit)
		}
	}
}
