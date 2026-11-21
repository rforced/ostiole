package cli

import (
	"log/slog"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
	"ostiole/internal/policy"
)

// `ostiole policy --clear` removes both Ostiole's current ip rules and
// anything left from the older 21000/22000 bands, while leaving Tailscale's
// 5210-5270 rules untouched.
func TestPolicyClearRemovesOstioleRulesAndLeavesTailscales(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	cfg := &model.Config{
		Version:  model.SchemaVersion,
		Gateways: []model.Gateway{{Name: "primary", Enabled: true, Interface: "wan0", Address: "203.0.113.1"}},
	}
	hops := map[string]policy.Hop{
		"primary": {Gateway: "primary", Address: "203.0.113.1", Interface: "wan0", Online: true},
	}
	if err := policy.NewInstaller(slog.New(slog.DiscardHandler)).Sync(policy.Plan(cfg, hops)); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Seed an older Ostiole rule and Tailscale's own rules.
	old := netlink.NewRule()
	old.Family, old.Priority, old.Mark, old.Mask, old.Table = unix.AF_INET, 22002, 0x10000, model.PolicyMarkMask, 2201
	ts := netlink.NewRule()
	ts.Family, ts.Priority, ts.Table = unix.AF_INET, 5270, 52
	for _, r := range []netlink.Rule{old, ts} {
		if err := netlink.AddRule(r); err != nil {
			t.Fatalf("seed %+v: %v", r, err)
		}
	}

	cmd := newPolicyCmd(&globals{})
	cmd.SetArgs([]string{"--clear"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("policy --clear: %v", err)
	}

	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		rules, err := netlink.Rules(fam)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rules {
			if r.Priority == policy.TailnetPriority || r.Priority == policy.MainPriority ||
				(r.Priority >= policy.TranslatePriorityBase && r.Priority < policy.MainPriority) ||
				(r.Priority >= 21000 && r.Priority < 22600) {
				t.Errorf("family %d: policy --clear left rule %+v", fam, r)
			}
		}
		if fam == unix.AF_INET && !slices.Contains(rules, ts) {
			t.Errorf("policy --clear disturbed Tailscale rule %+v", ts)
		}
	}
}
