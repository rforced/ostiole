package nft

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// Only the chains that match on the hour go in again, whole and in the
// order the ruleset has them.
func TestScheduleReloadTakesTheChainsThatMatchOnTheHour(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/full.nft")
	if err != nil {
		t.Fatal(err)
	}
	ruleset := string(raw)
	script, ok := ScheduleReload(ruleset)
	if !ok {
		t.Fatal("no chain to reload in a ruleset with schedules")
	}
	head, body, found := strings.Cut(script, "table inet ostiole {\n")
	if !found {
		t.Fatalf("no table block:\n%s", script)
	}
	if head != "flush chain inet ostiole zone_lan\nflush chain inet ostiole zone_dmz\n" {
		t.Errorf("flushes:\n%s", head)
	}
	for _, name := range []string{"zone_lan", "zone_dmz"} {
		block := chainBlock(t, ruleset, name)
		if !strings.Contains(body, block) {
			t.Errorf("%s is not in the script as the ruleset has it:\n%s", name, script)
		}
	}
	if strings.Contains(body, "zone_wan") || strings.Contains(body, "set alias_") {
		t.Errorf("the script carries more than the scheduled chains:\n%s", script)
	}

	if _, ok := ScheduleReload(renderFile(t, "testdata/upnp.json")); ok {
		t.Error("a ruleset without schedules has chains to reload")
	}
}

// At a new offset the scheduled chains are read again, and what the port
// mapping daemon wrote into its own chains stays where it was.
func TestScheduleReloadKeepsThePortMappingsInKernel(t *testing.T) {
	namespaced(t)
	cfg := loadConfig(t, "testdata/upnp.json")
	cfg.Schedules = []model.Schedule{{Name: "day", Start: "08:30", End: "17:30"}}
	cfg.Rules = append(cfg.Rules, model.Rule{
		ID: "day-ssh", Enabled: true, Zone: "wan", Action: model.ActionAccept, Protocol: model.ProtocolTCP,
		Destination: model.Endpoint{Ports: []string{"22"}}, Schedule: "day",
	})
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	script, ok := ScheduleReload(ruleset)
	if !ok || !strings.Contains(script, "flush chain inet ostiole zone_wan") {
		t.Fatalf("zone_wan is not reloaded:\n%s", script)
	}
	// One namespace for the whole run. The ruleset goes in at UTC and the
	// reload at two hours east; the listing reads in UTC.
	sh := "TZ=UTC nft -f - <<'EOF'\n" + ruleset + "EOF\n" +
		"nft add rule inet ostiole upnp_forward ip daddr 192.168.1.10 tcp dport 8080 accept\n" +
		"nft add rule inet ostiole upnp_prerouting iifname eth0 tcp dport 8080 dnat ip to 192.168.1.10\n" +
		"TZ=Etc/GMT-2 nft -f - <<'EOF'\n" + script + "EOF\n" +
		"TZ=UTC nft -j list table inet ostiole\n"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "unshare", "-Urn", "sh", "-c", sh).CombinedOutput()
	if err != nil {
		t.Fatalf("load, map and reload: %v\n%s", err, out)
	}
	rules := rulesByChain(t, out)
	if len(rules["upnp_forward"]) != 1 || len(rules["upnp_prerouting"]) != 1 {
		t.Errorf("mappings after the reload: forward %d, prerouting %d; want one each",
			len(rules["upnp_forward"]), len(rules["upnp_prerouting"]))
	}
	wan := strings.Join(rules["zone_wan"], "\n")
	if strings.Count(wan, `"jump"`) != 1 || strings.Count(wan, `"meta":{"key":"hour"}`) != 1 {
		t.Errorf("zone_wan after the reload:\n%s", wan)
	}
	if !strings.Contains(wan, `"06:30"`) || !strings.Contains(wan, `"15:30"`) {
		t.Errorf("the hours were not read again at the new offset:\n%s", wan)
	}
}

func chainBlock(t *testing.T, ruleset, name string) string {
	t.Helper()
	start := strings.Index(ruleset, "\tchain "+name+" {\n")
	if start < 0 {
		t.Fatalf("no chain %s", name)
	}
	end := strings.Index(ruleset[start:], "\n\t}\n")
	return ruleset[start : start+end+len("\n\t}\n")]
}

func renderFile(t *testing.T, path string) string {
	t.Helper()
	ruleset, err := Render(loadConfig(t, path))
	if err != nil {
		t.Fatal(err)
	}
	return ruleset
}

// rulesByChain reads `nft -j list table` into each chain's rules, as JSON.
func rulesByChain(t *testing.T, raw []byte) map[string][]string {
	t.Helper()
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	out := map[string][]string{}
	for _, item := range doc.Nftables {
		body, ok := item["rule"]
		if !ok {
			continue
		}
		var r struct {
			Chain string `json:"chain"`
		}
		var compact bytes.Buffer
		if err := json.Unmarshal(body, &r); err != nil || json.Compact(&compact, body) != nil {
			t.Fatalf("rule: %v\n%s", err, body)
		}
		out[r.Chain] = append(out[r.Chain], compact.String())
	}
	return out
}
