package model

import (
	"strings"
	"testing"
)

// Management from the WAN is the wan zone's anti-lockout, not rules of
// its own: it is one switch the operator can find and take back on the
// zone, rather than two rules to hunt down under Firewall.
func TestStarterManagementFromWANIsTheZoneSwitch(t *testing.T) {
	t.Parallel()
	on := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0", ManagementFromWAN: true})
	wan, ok := on.Zone("wan")
	if !ok || !wan.AntiLockout {
		t.Fatalf("wan zone = %+v, want anti-lockout on", wan)
	}
	for _, r := range on.Rules {
		if strings.Contains(r.ID, "from-wan") {
			t.Errorf("a management rule was written as well: %s", r.ID)
		}
	}
	if err := on.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}

	off := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	if wan, _ := off.Zone("wan"); wan.AntiLockout {
		t.Error("the wan zone got anti-lockout without being asked")
	}
}

// A new router is measured from the start: its WAN gets a gateway that
// learns its next hops, which with one WAN never moves a route.
func TestStarterWatchesTheWAN(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "enp2s0"})
	if len(cfg.Gateways) != 1 {
		t.Fatalf("gateways = %+v", cfg.Gateways)
	}
	if g := cfg.Gateways[0]; g.Name != "gw_enp2s0" || !g.Enabled || g.Interface != "enp2s0" || g.Address != "" || g.Monitor != "" {
		t.Errorf("gateway = %+v", g)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
	if cfg.CanFailover() {
		t.Error("one WAN fails over")
	}
	if lan := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24"}); len(lan.Gateways) != 0 {
		t.Errorf("a router without a WAN got %+v", lan.Gateways)
	}
}

func TestGatewayNameFor(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"eth0": "gw_eth0", "WAN.10": "gw_wan_10", "1x": "gw_gw", "": "gw_gw"} {
		if got := GatewayNameFor(in); got != want {
			t.Errorf("GatewayNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}
