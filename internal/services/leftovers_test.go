package services

import (
	"path/filepath"
	"slices"
	"testing"

	"ostiole/internal/install"
)

func TestUninstallFindsWhatServicesWrite(t *testing.T) {
	t.Parallel()
	left := install.DefaultLeftovers()
	for _, u := range []string{Unit, UnboundUnit, NTPUnit, UPnPUnit, TailscaleUnit, ProxyUnit, WirelessUnit, PPPoEUnit} {
		if !slices.Contains(left.Units, u) {
			t.Errorf("uninstall leaves %s: %v", u, left.Units)
		}
	}
	matches := func(globs []string, path string) bool {
		return slices.ContainsFunc(globs, func(g string) bool {
			ok, _ := filepath.Match(g, path)
			return ok
		})
	}
	files := []string{
		filepath.Join(DefaultDir, confName), filepath.Join(DefaultDir, hostsName), filepath.Join(DefaultDir, enabledName),
		filepath.Join(DefaultDir, BlockConfName), filepath.Join(DefaultDir, blockStateName),
		NewUnbound().ConfPath(), NewUPnP().ConfPath(), NewNTP().ConfPath(), filepath.Join(NTPDir, ntpCheckName),
		filepath.Join(PPPoEDir, PeerFile("wan0")), "/etc/sysusers.d/ostiole-proxy.conf",
	}
	for _, f := range files {
		if !matches(left.Files, f) {
			t.Errorf("uninstall leaves %s: %v", f, left.Files)
		}
	}
	for _, s := range []string{ProxyStateDir, LeaseFile} {
		if !matches(left.State, s) || matches(left.Files, s) {
			t.Errorf("%s is not removed by a purge alone: files %v, state %v", s, left.Files, left.State)
		}
	}
	if left.User != ProxyUser || !slices.Contains(left.Binaries, ProxyBinaryName) {
		t.Errorf("purge leaves the proxy's account or binary: %+v", left)
	}
	for _, u := range slices.Concat([]string{distroUnit, resolvedUnit, unboundDistroSvc, upnpDistroSvc, tailscaleDistroSvc, hostapdDistroSvc}, ntpCompetitors) {
		if !slices.Contains(left.Unmask, u) {
			t.Errorf("uninstall leaves %s masked: %v", u, left.Unmask)
		}
	}
	if left.Resolv != ResolvConf {
		t.Errorf("Resolv = %q, dnsmasq writes %q", left.Resolv, ResolvConf)
	}
}
