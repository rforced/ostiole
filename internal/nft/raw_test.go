package nft

import (
	"strings"
	"testing"

	"ostiole/internal/model"
)

func TestAllowingIPOptionsKeepsTheSourceCheck(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/policy.json")
	cfg.Protection.AllowIPOptions = true
	out, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{kindIPOptions, kindRoutingHeader, kindHopByHop} {
		if strings.Contains(out, kind) {
			t.Errorf("%s is still dropped with IP options allowed", kind)
		}
	}
	if !strings.Contains(out, `iifname "eth2" ip6 saddr != :: fib saddr . iif oif missing counter drop comment "block-spoofed"`) {
		t.Errorf("the source check went with the IP options drops:\n%s", out)
	}
}

// Addresses and TCP flags that never cross a link honestly are dropped
// whatever the IP options setting says, and their rows name no setting,
// since nothing turns them off.
func TestTheFixedRawDropsAnswerToNoSetting(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/minimal.json")
	cfg.Protection.AllowIPOptions = true
	rows, err := SystemRules(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, row := range rows {
		if row.Chain != RawChain {
			continue
		}
		if row.Setting != "" || len(row.Keys) != 1 {
			t.Errorf("a fixed drop's row = %+v, want no setting and one counter", row)
		}
		kept[row.Keys[0]] = true
	}
	if len(kept) != 2 || !kept[RawChain+"/"+kindImpossible] || !kept[RawChain+"/"+kindTCPFlags] {
		t.Errorf("raw rows with IP options allowed = %v, want the impossible addresses and the TCP flags", kept)
	}
}

// The bootstrap and fallback rulesets may open the management ports to
// anyone, so they carry every raw drop the full ruleset can.
func TestTheMinimalRulesetsCarryEveryRawDrop(t *testing.T) {
	t.Parallel()
	full, err := Render(loadConfig(t, "testdata/minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	boot := Bootstrap([]uint16{9443, 22})
	for _, d := range append(append([]rawDrop{}, rawFixed...), rawOptions...) {
		for _, match := range d.matches {
			if !strings.Contains(full, match+" counter drop") {
				t.Errorf("the ruleset does not drop %q", match)
			}
			if !strings.Contains(boot, match+" drop") {
				t.Errorf("the bootstrap ruleset does not drop %q", match)
			}
		}
	}
}

func TestAnInterfaceCanLetUnroutedIPv6In(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/policy.json")
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "eth2" {
			cfg.Interfaces[i].AllowUnroutedIPv6 = true
		}
	}
	out, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, kindSpoofed) {
		t.Errorf("an interface that lets unrouted IPv6 in was source-checked:\n%s", out)
	}
}

// A gateway's replies come in by its link from sources anywhere on the
// internet, so its link is never source-checked, whatever its zone.
func TestAGatewayLinkIsNotSourceChecked(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/policy.json")
	cfg.Gateways = append(cfg.Gateways, model.Gateway{Name: "inside_gw", Enabled: true, Interface: "eth2", Address: "2001:db8:2::1"})
	out, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, kindSpoofed) {
		t.Errorf("a link a gateway sends through was source-checked:\n%s", out)
	}
}
