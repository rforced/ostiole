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
	for _, kind := range []string{kindIPOptions, kindRoutingHeader} {
		if strings.Contains(out, kind) {
			t.Errorf("%s is still dropped with IP options allowed", kind)
		}
	}
	if !strings.Contains(out, `iifname "eth2" ip6 saddr != :: fib saddr . iif oif missing counter drop comment "block-spoofed"`) {
		t.Errorf("the source check went with the IP options drops:\n%s", out)
	}
}

func TestNothingToDropBeforeTrackingRendersNoRawChain(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/minimal.json")
	cfg.Protection.AllowIPOptions = true
	out, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, RawChain) {
		t.Errorf("a raw chain with nothing in it was rendered:\n%s", out)
	}
	rows, err := SystemRules(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Chain == RawChain {
			t.Errorf("a row was recorded for a chain that is not there: %+v", row)
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
