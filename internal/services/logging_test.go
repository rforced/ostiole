package services

import (
	"slices"
	"testing"

	"github.com/rforced/ostiole/internal/logging"
	"github.com/rforced/ostiole/internal/model"
)

// internal/logging writes the unit names out rather than importing this
// package, because internal/install needs the same list and importing
// services from there would be a circle. This test is the one place that
// sees both, so a unit added here without a cap is caught.
func TestLoggingCapsEveryServiceUnit(t *testing.T) {
	t.Parallel()
	capped := make([]string, 0, len(logging.Units))
	for _, u := range logging.Units {
		capped = append(capped, u.Name)
	}
	for _, name := range []string{Unit, UnboundUnit, UPnPUnit, PPPoEUnit, WirelessUnit, NTPUnit} {
		if !slices.Contains(capped, name) {
			t.Errorf("%s writes to the journal uncapped; add it to logging.Units", name)
		}
	}
	// tailscaled is deliberately absent: it prints at one priority, so a
	// cap would hide its errors too. So is the proxy: its level is in its
	// own configuration, and a cap would drop its WAF events.
	for _, name := range []string{TailscaleUnit, ProxyUnit} {
		if slices.Contains(capped, name) {
			t.Errorf("%s is capped, which would hide its errors", name)
		}
	}
}

// Nothing caps the proxy, so its own configuration is all that holds it to
// the level the rest of the router runs at. That means the effective
// level: an unset level is warning here as everywhere else, and access
// logs follow the same rule as the other daemons' client records.
func TestProxyLogLevelFollowsTheEffectiveLevel(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	p := &Proxy{}
	if got := p.logConfig(cfg).Level; got != "WARN" {
		t.Errorf("unset level = %q, want WARN", got)
	}
	if cfg.System.Logging.Records() {
		t.Error("an unset level writes a line per request")
	}
	for level, want := range map[model.LogLevel]string{
		model.LogError:   "ERROR",
		model.LogWarning: "WARN",
		model.LogInfo:    "INFO",
		model.LogDebug:   "DEBUG",
	} {
		cfg.System.Logging.Level = level
		if got := p.logConfig(cfg).Level; got != want {
			t.Errorf("level %q = %q, want %q", level, got, want)
		}
	}
}
