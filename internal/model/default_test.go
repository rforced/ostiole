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
