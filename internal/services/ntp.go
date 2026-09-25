package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/chrony"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// Time service paths and names.
const (
	NTPUnit = "ostiole-chronyd.service"
	// NTPDir holds the generated configuration. It is chrony's own
	// directory: the AppArmor profile Debian and Ubuntu ship lets chronyd
	// read nothing else under /etc, and SELinux labels it as chronyd_t
	// expects.
	NTPDir       = "/etc/chrony"
	ntpConfName  = "ostiole.conf"
	ntpCheckName = ".ostiole-check.conf"
	// ntpLeapList is the IERS list of leap seconds that tzdata ships.
	ntpLeapList = "/usr/share/zoneinfo/leap-seconds.list"
	// ntpLocalActivate is the root distance, in seconds, the clock must
	// have come within once before the router answers the LAN from its own
	// clock when every server is lost. A clock that never synchronised has
	// an infinite one, so it never answers with that.
	ntpLocalActivate = "0.5"
)

// ntpCompetitors are the other time services a distribution may run. Each
// that exists is stopped and masked when Ostiole's takes over, because
// two daemons steering one clock fight, and both want udp/123.
var ntpCompetitors = []string{
	"chronyd.service", "chrony.service", "chronyd-restricted.service", "systemd-timesyncd.service",
	"ntpd.service", "ntpsec.service", "openntpd.service", "ntpd-rs.service",
}

// ntpMonitor is every command chronyd answers on its command port. Naming
// any replaces the default set rather than adding to it, so the defaults
// are listed along with the two the page reads besides: authdata for the
// NTS state and serverstats for what the LAN asked.
const ntpMonitor = "activity authdata manual rtcdata serverstats smoothing sourcename sources sourcestats tracking"

// NTP runs chronyd, which keeps the router's clock and can answer the
// LAN's time requests. It implements network.Backend so the engine
// applies and reverts it with everything else.
//
// Its unit is Ostiole's own, like every other daemon's, and whatever the
// distribution kept the time with is masked, but only once this unit
// exists and is being started: Setup never starts a daemon, and a router
// is never left with nothing keeping its time.
type NTP struct {
	// Dir holds the generated configuration; default NTPDir.
	Dir string
	// Cmd runs systemctl and chronyd; default execs them.
	Cmd network.Commander
	// Binary is chronyd; empty looks it up.
	Binary string
	// Features pins what the build is taken to support; nil asks it.
	Features *NTPFeatures

	mu     sync.Mutex
	probed NTPFeatures
	// probedFrom is the binary's modification time at the last probe,
	// so an upgraded chronyd is asked again.
	probedFrom time.Time
}

// NTPFeatures is what this router's chronyd can be told.
type NTPFeatures struct {
	Version chrony.Version
	// LeapList says the list of leap seconds is on disk.
	LeapList bool
}

var _ network.Backend = (*NTP)(nil)

// NewNTP returns a backend with production defaults.
func NewNTP() *NTP {
	return &NTP{Dir: NTPDir, Cmd: execCommander{}}
}

func (n *NTP) dir() string {
	if n.Dir == "" {
		return NTPDir
	}
	return n.Dir
}

func (n *NTP) cmd() network.Commander {
	if n.Cmd == nil {
		return execCommander{}
	}
	return n.Cmd
}

func (n *NTP) binary() string {
	if n.Binary != "" {
		return n.Binary
	}
	return lookPath("chronyd")
}

// Name implements network.Backend.
func (n *NTP) Name() string { return "ntp" }

// ConfPath is the generated configuration file.
func (n *NTP) ConfPath() string { return filepath.Join(n.dir(), ntpConfName) }

// features asks the build what it is, again whenever the binary changes.
// A router with no chronyd renders for the oldest build it could get.
func (n *NTP) features() NTPFeatures {
	if n.Features != nil {
		return *n.Features
	}
	bin := n.binary()
	if bin == "" {
		return NTPFeatures{}
	}
	info, err := os.Stat(bin)
	if err != nil {
		return NTPFeatures{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if info.ModTime().Equal(n.probedFrom) {
		return n.probed
	}
	f := NTPFeatures{}
	_, err = os.Stat(ntpLeapList)
	f.LeapList = err == nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	out, err := n.cmd().Run(ctx, bin, "-v")
	cancel()
	if err != nil {
		// Asked again next time: a build that would not say this once may
		// say it the next.
		return f
	}
	if f.Version, err = chrony.ParseVersion(string(out)); err != nil {
		return f
	}
	n.probed, n.probedFrom = f, info.ModTime()
	return f
}

// Version is what the router's chronyd says it is; zero when there is
// none or it would not say.
func (n *NTP) Version() chrony.Version { return n.features().Version }

// Render implements network.Backend. There is always a configuration:
// the router keeps time whatever else it does.
func (n *NTP) Render(cfg *model.Config) (network.Files, error) {
	return network.Files{ntpConfName: renderNTP(cfg, n.features())}, nil
}

func renderNTP(cfg *model.Config, f NTPFeatures) string {
	var b strings.Builder
	b.WriteString(fileHeader)
	// Nothing is read from elsewhere: no confdir, no sourcedir. What a
	// distribution or an ISP's DHCP drops beside this file never reaches
	// the clock.
	servers, nts := 0, false
	for _, s := range cfg.NTPServers() {
		kind := "server"
		servers++
		if s.Pool {
			// chronyd takes up to four servers from a pool.
			kind = "pool"
			servers += 3
		}
		fmt.Fprintf(&b, "%s %s iburst", kind, s.Host)
		if s.NTS {
			b.WriteString(" nts")
			nts = true
		}
		b.WriteString("\n")
	}
	if servers >= 3 {
		// One server on its own cannot move the clock. With fewer than
		// three the clock would never be set at all.
		b.WriteString("minsources 2\n")
	}
	if nts {
		// New keys daily rather than every four weeks. A server can link
		// the requests made under one set of keys, so this bounds how long
		// it can follow the router across a change of WAN address.
		b.WriteString("ntsrefresh 86400\n")
		b.WriteString("ntsdumpdir /var/lib/chrony\n")
	}
	b.WriteString("driftfile /var/lib/chrony/drift\n")
	b.WriteString("makestep 1.0 3\n")
	b.WriteString("rtcsync\n")
	// tzdata puts the list on disk whatever the build, but a build older
	// than 4.6 refuses the line.
	if f.LeapList && f.Version.AtLeast(4, 6) {
		fmt.Fprintf(&b, "leapseclist %s\n", ntpLeapList)
	}
	if f.Version.AtLeast(4, 7) {
		fmt.Fprintf(&b, "opencommands %s\n", ntpMonitor)
	}
	if cfg.NTPServing() {
		// The firewall decides which interfaces reach this. Prefixes on
		// the LAN side can be delegated and change under the router, and
		// an NTP answer is the size of its question, so there is nothing
		// here worth amplifying.
		b.WriteString("allow all\n")
		// Once synchronised, keep answering the LAN from the router's own
		// clock through an outage of every server, at a stratum any real
		// server beats. Older builds cannot hold that back until the
		// first sync, so they do not answer from their own clock at all.
		if f.Version.AtLeast(4, 6) {
			fmt.Fprintf(&b, "local stratum 10 activate %s\n", ntpLocalActivate)
		}
	}
	return b.String()
}

// Snapshot implements network.Backend.
func (n *NTP) Snapshot() (network.Files, error) {
	files := network.Files{}
	raw, err := os.ReadFile(n.ConfPath())
	switch {
	case err == nil:
		files[ntpConfName] = string(raw)
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	return files, nil
}

// Preflight implements the bundle's Preflighter: chronyd reads the
// configuration before anything is applied, so a build that does not
// know a line refuses it here rather than failing to start.
func (n *NTP) Preflight(ctx context.Context, files network.Files) error {
	conf, wanted := files[ntpConfName]
	if !wanted || !n.Installed(ctx) {
		return nil
	}
	f := n.features()
	if f.Version != (chrony.Version{}) && !f.Version.NTS && strings.Contains(conf, " nts\n") {
		return &PreflightError{Message: "This router's time service was built without NTS: list servers without it"}
	}
	bin := n.binary()
	if bin == "" {
		return nil
	}
	path := filepath.Join(n.dir(), ntpCheckName)
	if err := writeFile(path, conf); err != nil {
		return err
	}
	defer os.Remove(path)
	out, err := n.cmd().Run(ctx, bin, "-p", "-f", path)
	if err != nil {
		return &PreflightError{Message: "The time service refuses its configuration: " + chronydFatal(string(out), err)}
	}
	return nil
}

// chronydFatal picks chronyd's reason out of what `chronyd -p` printed,
// which is the configuration it read and then the line it stopped at.
func chronydFatal(out string, err error) string {
	for _, line := range strings.Split(out, "\n") {
		if _, why, ok := strings.Cut(line, "Fatal error : "); ok {
			return strings.TrimSpace(why)
		}
	}
	return err.Error()
}

// Apply implements network.Backend: write the configuration, take the
// clock over from whatever kept it, and start or restart the unit.
func (n *NTP) Apply(ctx context.Context, files network.Files) error {
	conf, wanted := files[ntpConfName]
	switch {
	case !wanted:
		// A revert to a snapshot taken before this router kept its own
		// time. Whatever keeps it now carries on: stopping it would leave
		// the clock with nothing at all.
		return nil
	case !n.Installed(ctx):
		// Until `ostiole repair` writes the unit, whatever the
		// distribution keeps the time with carries on, untouched.
		return nil
	}
	current, err := n.Snapshot()
	if err != nil {
		return err
	}
	changed := current[ntpConfName] != conf
	if changed {
		if err := os.MkdirAll(n.dir(), 0o755); err != nil { //nolint:gosec // chronyd reads this
			return err
		}
		if err := writeFile(n.ConfPath(), conf); err != nil {
			return err
		}
	}
	if err := n.takeOver(ctx); err != nil {
		return err
	}
	running := n.Active(ctx)
	if out, err := n.cmd().Run(ctx, "systemctl", "enable", "--now", NTPUnit); err != nil {
		return fmt.Errorf("enable %s: %w: %s", NTPUnit, err, strings.TrimSpace(string(out)))
	}
	// chronyd reads its configuration once. A restart costs the LAN a
	// second or two of answers, so only a change pays for one.
	if changed && running {
		if out, err := n.cmd().Run(ctx, "systemctl", "restart", NTPUnit); err != nil {
			return fmt.Errorf("restart %s: %w: %s", NTPUnit, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Start takes the clock over at daemon start, from the configuration the
// router runs, when the unit is there and not running yet. That is the
// first start after `ostiole repair` wrote it: waiting for the next apply
// would leave the distribution's service masked by nothing and ours not
// started, or the other way round, for as long as nobody applied.
func (n *NTP) Start(ctx context.Context, cfg *model.Config) error {
	if !n.Installed(ctx) || n.Active(ctx) {
		return nil
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	files, err := n.Render(cfg)
	if err != nil {
		return err
	}
	if err := n.Preflight(ctx, files); err != nil {
		return err
	}
	return n.Apply(ctx, files)
}

// takeOver stops and masks every other time service the router has. Each
// is named by its id, because Debian's chronyd.service is an alias of
// chrony.service and only the real name can be masked. Nothing is
// disabled: masking is enough to keep it from starting, and disabling a
// unit with an init script fails inside the daemon's read-only sandbox.
func (n *NTP) takeOver(ctx context.Context) error {
	args := append([]string{"show", "--property=Id,LoadState"}, ntpCompetitors...)
	out, err := n.cmd().Run(ctx, "systemctl", args...)
	if err != nil {
		return fmt.Errorf("look for other time services: %w: %s", err, strings.TrimSpace(string(out)))
	}
	seen := map[string]bool{}
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		props := map[string]string{}
		for _, line := range strings.Split(block, "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				props[k] = v
			}
		}
		id := props["Id"]
		switch props["LoadState"] {
		case "", "not-found", "masked":
			continue
		}
		if id == "" || id == NTPUnit || seen[id] {
			continue
		}
		seen[id] = true
		if out, err := n.cmd().Run(ctx, "systemctl", "mask", "--now", id); err != nil {
			return fmt.Errorf("stop %s, which would steer the clock too: %w: %s", id, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Installed reports whether the ostiole-chronyd unit exists.
func (n *NTP) Installed(ctx context.Context) bool {
	_, err := n.cmd().Run(ctx, "systemctl", "cat", NTPUnit)
	return err == nil
}

// Active reports whether the unit is running.
func (n *NTP) Active(ctx context.Context) bool {
	out, err := n.cmd().Run(ctx, "systemctl", "is-active", NTPUnit)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// Skipped reports whether systemd declined to start the unit because the
// router cannot set its clock: a container, where the host keeps the time.
func (n *NTP) Skipped(ctx context.Context) bool {
	out, err := n.cmd().Run(ctx, "systemctl", "show", "--property=ConditionResult", "--value", NTPUnit)
	return err == nil && strings.TrimSpace(string(out)) == "no"
}

// ActiveSince is when the unit last started; zero when it is not running
// or systemd would not say.
func (n *NTP) ActiveSince(ctx context.Context) time.Time {
	out, err := n.cmd().Run(ctx, "systemctl", "show", "--timestamp=unix", "--property=ActiveEnterTimestamp", "--value", NTPUnit)
	if err != nil {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(string(out)), "@"), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// NTPUnitContent renders the ostiole-chronyd unit. The hardening follows
// what Fedora ships for chronyd. It runs nowhere a clock cannot be set:
// in a container the host keeps the time, as systemd-timesyncd assumes.
func NTPUnitContent(binary, conf string) string {
	return fmt.Sprintf(`[Unit]
Description=Ostiole time service (chrony)
Documentation=https://github.com/rforced/ostiole
After=network.target ostiole-firewall.service
Wants=ostiole-firewall.service
Conflicts=%[3]s
ConditionCapability=CAP_SYS_TIME
ConditionVirtualization=!container

[Service]
Type=simple
ExecStart=%[1]s -n -f %[2]s
Restart=on-failure
RestartSec=2
ProtectSystem=strict
ReadWritePaths=/run /var/lib/chrony
ProtectHome=yes
PrivateTmp=yes
ProtectControlGroups=yes
ProtectKernelModules=yes
ProtectKernelTunables=yes
ProtectKernelLogs=yes
ProtectHostname=yes
RestrictNamespaces=yes
RestrictSUIDSGID=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
LockPersonality=yes
MemoryDenyWriteExecute=yes
DevicePolicy=closed
DeviceAllow=char-rtc rw
DeviceAllow=char-pps rw
DeviceAllow=char-ptp rw
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`, binary, conf, strings.Join(ntpCompetitors, " "))
}
