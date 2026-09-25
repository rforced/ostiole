package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/wireless"
)

// hostapd paths and names.
const (
	// WirelessUnit is a templated unit: one instance per radio, named
	// after the interface the kernel gave it.
	WirelessUnit = "ostiole-hostapd@.service"

	hostapdDistroSvc = "hostapd.service"
	wirelessConfExt  = ".conf"
	wirelessEnvExt   = ".env"
	// wirelessProbe is how long an iw call gets while a plan is rendered.
	wirelessProbe = 5 * time.Second
)

// Wireless serves the configured networks through hostapd, one instance
// per radio (ADR-0015). The interfaces hostapd creates are ordinary
// interfaces everywhere else in Ostiole: a bridge port or an interface in
// a zone with an address of its own.
type Wireless struct {
	// Dir holds the generated files, under the configuration directory.
	Dir string
	// Cmd runs systemctl, iw and journalctl; default execs them.
	Cmd network.Commander
	// Probe reads what a radio can do; nil asks iw.
	Probe func(ctx context.Context, radio string) (*wireless.Phy, error)
	// Reg sets the kernel's regulatory domain; nil runs iw.
	Reg func(ctx context.Context, country string) error
	// Bluetooth reports whether the module is loaded; nil looks in /sys.
	Bluetooth func() bool
	// List names the radios on this router; nil looks in /sys.
	List func() []string
}

var (
	_ network.Backend = (*Wireless)(nil)
	_ Preflighter     = (*Wireless)(nil)
)

// NewWireless returns a backend writing under configDir.
func NewWireless(configDir string) *Wireless {
	return &Wireless{Dir: WirelessDir(configDir), Cmd: execCommander{}}
}

// WirelessDir is where the generated files go for a given configuration
// directory. The unit reads both of a radio's files from here.
func WirelessDir(configDir string) string {
	if configDir == "" {
		configDir = store.DefaultDir
	}
	return filepath.Join(configDir, "wireless")
}

// WirelessUnitFor is the instance of the templated unit that serves one
// radio's networks.
func WirelessUnitFor(radio string) string {
	return strings.Replace(WirelessUnit, "@.", "@"+radio+".", 1)
}

func (w *Wireless) dir() string {
	if w.Dir == "" {
		return WirelessDir("")
	}
	return w.Dir
}

func (w *Wireless) cmd() network.Commander {
	if w.Cmd == nil {
		return execCommander{}
	}
	return w.Cmd
}

func (w *Wireless) path(name string) string { return filepath.Join(w.dir(), name) }

// learnCountry scans on the radio's own interface, which is how a
// self-managed card learns its country; the interface has to be up for
// it. What the scan taught the card is the next probe's to read.
func (w *Wireless) learnCountry(ctx context.Context, radio string) {
	_, _ = w.cmd().Run(ctx, "ip", "link", "set", radio, "up")
	_, _ = w.cmd().Run(ctx, "iw", "dev", radio, "scan")
}

func (w *Wireless) probe(ctx context.Context, radio string) (*wireless.Phy, error) {
	if w.Probe != nil {
		return w.Probe(ctx, radio)
	}
	return wireless.ProbePhy(ctx, w.cmd(), radio)
}

// bluetooth reports whether the module a router has no use for is loaded
// after all. The install blocks it; a radio and a Bluetooth controller on
// one card share an antenna and a firmware.
func (w *Wireless) bluetooth() bool {
	if w.Bluetooth != nil {
		return w.Bluetooth()
	}
	_, err := os.Stat("/sys/module/bluetooth")
	return err == nil
}

// Name implements network.Backend.
func (w *Wireless) Name() string { return "wireless" }

// Render implements network.Backend. The card is asked what it can do
// while the plan is made, so the configuration carries its capabilities;
// a radio that does not answer renders without them and the preflight is
// what refuses the apply.
func (w *Wireless) Render(cfg *model.Config) (network.Files, error) {
	files := network.Files{}
	radios := cfg.ActiveRadios()
	if len(radios) == 0 {
		return files, nil
	}
	master := cfg.MasterOf()
	ctx, cancel := context.WithTimeout(context.Background(), wirelessProbe)
	defer cancel()
	for _, r := range radios {
		nets := cfg.NetworksOn(r.Name)
		phy, err := w.probe(ctx, r.Name)
		if err != nil {
			phy = nil
		}
		files[r.Name+wirelessConfExt] = wireless.Render(cfg.Wireless.Country, r, nets, phy, master,
			cfg.System.Logging.EffectiveLevel())
		files[r.Name+wirelessEnvExt] = wireless.Env(cfg.Wireless.Country, r, phy, nets)
	}
	return files, nil
}

// Snapshot implements network.Backend.
func (w *Wireless) Snapshot() (network.Files, error) {
	files := network.Files{}
	entries, err := os.ReadDir(w.dir())
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, wirelessConfExt) && !strings.HasSuffix(name, wirelessEnvExt)) {
			continue
		}
		raw, err := os.ReadFile(w.path(name))
		if err != nil {
			return nil, err
		}
		files[name] = string(raw)
	}
	return files, nil
}

// errWirelessMissing is what an apply says when hostapd is not here. The
// script installs it when it finds a card or is asked for one (ADR-0013).
var errWirelessMissing = errors.New(
	"hostapd is not on this router: run `ostiole repair --wireless` once as root")

// errBluetoothLoaded refuses to bring a radio up next to the module the
// install removed.
var errBluetoothLoaded = errors.New(
	"the Bluetooth module is loaded on this router; run `ostiole repair` as root")

// Preflight implements Preflighter: every refusal a card can give is
// given here, before a rule has been written.
func (w *Wireless) Preflight(ctx context.Context, files network.Files) error {
	if len(files) == 0 {
		return nil
	}
	if !w.Installed(ctx) {
		return errWirelessMissing
	}
	if w.bluetooth() {
		return errBluetoothLoaded
	}
	radios := radiosIn(files)
	// A card the kernel governs reports the world's channels until the
	// country is set, and the world allows nothing above 2.4 GHz. The apply
	// sets it again; setting it here is what makes the probe honest.
	if country := wireless.ParseEnv(files[radios[0]+wirelessEnvExt]).Country; country != "" {
		if err := w.setReg(ctx, country); err != nil {
			return err
		}
	}
	for _, radio := range radios {
		if err := w.preflightRadio(ctx, radio, wireless.ParseEnv(files[radio+wirelessEnvExt])); err != nil {
			return err
		}
	}
	return nil
}

func (w *Wireless) preflightRadio(ctx context.Context, radio string, plan wireless.EnvPlan) error {
	phy, err := w.probe(ctx, radio)
	if err != nil {
		return fmt.Errorf("radio %s is not on this router", radio)
	}
	if !slices.Contains(phy.Modes, "AP") {
		return fmt.Errorf("radio %s cannot serve a network", radio)
	}
	if plan.Networks > phy.APLimit {
		return fmt.Errorf("radio %s serves at most %d network(s)", radio, phy.APLimit)
	}
	band, ok := phy.Bands[plan.Band]
	if !ok {
		return fmt.Errorf("radio %s has no %s band", radio, plan.Band)
	}
	if !phy.Serves(plan.Band) && phy.Driver == "iwlwifi" && plan.Band != model.Band6G && band.NoIROnly() {
		// A self-managed card calls every channel no-IR until it has
		// scanned and learned the country from the networks around it,
		// which is what the unit does before starting hostapd. Done here
		// first, a fresh card is judged on what it can do rather than on
		// what it has not learned yet.
		w.learnCountry(ctx, radio)
		if again, err := w.probe(ctx, radio); err == nil {
			phy = again
			if b, ok := again.Bands[plan.Band]; ok {
				band = b
			}
		}
	}
	if !phy.Serves(plan.Band) {
		return fmt.Errorf("radio %s may not transmit on %s here: no channel allows it", radio, plan.Band)
	}
	if plan.Channel != 0 {
		// A card is not asked about no-IR here: a self-managed one calls
		// every channel that until it has scanned, which the unit does.
		c, known := band.Channel(plan.Channel)
		switch {
		case !known:
			return fmt.Errorf("radio %s has no channel %d", radio, plan.Channel)
		case c.Disabled:
			return fmt.Errorf("channel %d is not allowed in %s", plan.Channel, plan.Country)
		case c.Radar:
			return fmt.Errorf("channel %d needs radar detection, which radio %s has not got", plan.Channel, radio)
		}
	}
	if plan.Width > band.MaxWidth {
		return fmt.Errorf("radio %s does not go above %d MHz on %s", radio, band.MaxWidth, plan.Band)
	}
	switch {
	case plan.Standard == model.StandardAX && !band.HE:
		return fmt.Errorf("radio %s does not do ax on %s", radio, plan.Band)
	case plan.Standard == model.StandardAC && !band.VHT:
		return fmt.Errorf("radio %s does not do ac on %s", radio, plan.Band)
	}
	return nil
}

// Apply implements network.Backend: stop the radios that have gone, write
// what changed, and keep an instance of the unit per radio in step.
func (w *Wireless) Apply(ctx context.Context, files network.Files) error {
	current, err := w.Snapshot()
	if err != nil {
		return err
	}
	// A router with no radio, and never any, must not so much as create a
	// directory: an apply that has nothing to do with wireless cannot be
	// allowed to fail on it.
	if len(files) == 0 && len(current) == 0 {
		return nil
	}
	if len(files) > 0 && !w.Installed(ctx) {
		return errWirelessMissing
	}

	for _, radio := range radiosIn(current) {
		if _, keep := files[radio+wirelessConfExt]; keep {
			continue
		}
		if _, err := w.cmd().Run(ctx, "systemctl", "disable", "--now", WirelessUnitFor(radio)); err != nil {
			return fmt.Errorf("stop the networks on %s: %w", radio, err)
		}
		for _, ext := range []string{wirelessConfExt, wirelessEnvExt} {
			if err := os.Remove(w.path(radio + ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(w.dir(), 0o700); err != nil {
		return err
	}

	radios := radiosIn(files)
	// The kernel keeps one regulatory domain, so it is set once for the
	// router rather than per radio.
	if country := wireless.ParseEnv(files[radios[0]+wirelessEnvExt]).Country; country != "" {
		if err := w.setReg(ctx, country); err != nil {
			return err
		}
	}
	for _, radio := range radios {
		changed := false
		for _, ext := range []string{wirelessConfExt, wirelessEnvExt} {
			name := radio + ext
			if current[name] == files[name] {
				continue
			}
			// 0600: the passphrases are in here.
			if err := writeSecretFile(w.path(name), files[name]); err != nil {
				return err
			}
			changed = true
		}
		unit := WirelessUnitFor(radio)
		active := w.active(ctx, unit)
		if !changed && active {
			continue
		}
		if out, err := w.cmd().Run(ctx, "systemctl", "enable", "--now", unit); err != nil {
			return fmt.Errorf("start the networks on %s: %w: %s", radio, err, strings.TrimSpace(string(out)))
		}
		// A fresh start has just read the files; an instance that was
		// already running has not.
		if changed && active {
			if out, err := w.cmd().Run(ctx, "systemctl", "restart", unit); err != nil {
				return fmt.Errorf("restart the networks on %s: %w: %s", radio, err, strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}

func (w *Wireless) setReg(ctx context.Context, country string) error {
	if w.Reg != nil {
		return w.Reg(ctx, country)
	}
	if out, err := w.cmd().Run(ctx, "iw", "reg", "set", country); err != nil {
		return fmt.Errorf("set the regulatory domain to %s: %w: %s", country, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// radiosIn names the radios a set of files covers, sorted.
func radiosIn(files network.Files) []string {
	var out []string
	for name := range files {
		if radio, ok := strings.CutSuffix(name, wirelessConfExt); ok {
			out = append(out, radio)
		}
	}
	slices.Sort(out)
	return out
}

// Radios names the wifi devices on this router.
func (w *Wireless) Radios() []string {
	if w.List != nil {
		return w.List()
	}
	return wireless.Radios()
}

// Phy reads what a radio can do.
func (w *Wireless) Phy(ctx context.Context, radio string) (*wireless.Phy, error) {
	return w.probe(ctx, radio)
}

// Regulatory reads the kernel's domain and the domains of the cards that
// keep one of their own.
func (w *Wireless) Regulatory(ctx context.Context) (global string, phys map[string]string) {
	out, err := w.cmd().Run(ctx, "iw", "reg", "get")
	if err != nil {
		return "", map[string]string{}
	}
	return wireless.RegCountry(string(out))
}

// Dev reads what one interface is doing.
func (w *Wireless) Dev(ctx context.Context, iface string) (wireless.DevInfo, error) {
	out, err := w.cmd().Run(ctx, "iw", "dev", iface, "info")
	if err != nil {
		return wireless.DevInfo{}, err
	}
	return wireless.ParseDevInfo(string(out))
}

// Stations lists the clients on one network.
func (w *Wireless) Stations(ctx context.Context, iface string) ([]wireless.Station, error) {
	out, err := w.cmd().Run(ctx, "iw", "dev", iface, "station", "dump")
	if err != nil {
		return nil, err
	}
	return wireless.ParseStations(string(out)), nil
}

// Installed reports whether the templated unit exists.
func (w *Wireless) Installed(ctx context.Context) bool {
	_, err := w.cmd().Run(ctx, "systemctl", "cat", WirelessUnit)
	return err == nil
}

// LastLog is the newest line hostapd logged for a radio's instance that
// reads like a reason, or the newest line at all. A stopped radio says
// why it gave up just before it does, and that line is what the status
// card wants; a card that only said "stopped" sent people to the journal.
func (w *Wireless) LastLog(ctx context.Context, radio string) string {
	out, err := w.cmd().Run(ctx, "journalctl", "-u", WirelessUnitFor(radio), "-n", "20", "-o", "cat", "--no-pager")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := ""
	for _, line := range slices.Backward(lines) {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if last == "" {
			last = l
		}
		if readsLikeAReason(l) {
			return l
		}
	}
	return last
}

// readsLikeAReason picks hostapd's complaints out of its event lines.
func readsLikeAReason(l string) bool {
	lower := strings.ToLower(l)
	for _, w := range []string{"fail", "error", "could not", "unable", "invalid", "not supported", "not allowed", "no such", "busy", "timeout", "denied", "refused"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// Active reports whether a radio's instance is running.
func (w *Wireless) Active(ctx context.Context, radio string) bool {
	return w.active(ctx, WirelessUnitFor(radio))
}

func (w *Wireless) active(ctx context.Context, unit string) bool {
	out, err := w.cmd().Run(ctx, "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}
