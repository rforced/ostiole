package feeds

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
)

// A refreshed list has to reach a ruleset that is already loaded, without
// taking the rules out while it does. This loads a real ruleset into a
// kernel, applies the refresh fragment on top, and checks the set changed
// while the rule using it stayed exactly where it was.
func TestRefreshedListReachesTheKernel(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not installed")
	}

	cfg := config(model.Alias{
		Name: "drop", Type: model.AliasHosts,
		URL: "http://example.invalid/list", Entries: []string{"203.0.113.1"},
	})
	cfg.Rules = []model.Rule{{
		ID: "drop-listed", Enabled: true, Zone: "wan", Action: model.ActionDrop,
		Protocol: model.ProtocolAny, Source: model.Endpoint{Alias: "drop"},
	}}

	// Yesterday's list, then today's.
	before := map[string][]string{"drop": {"192.0.2.0/24"}}
	ruleset, err := nft.RenderWithFeeds(cfg, before)
	if err != nil {
		t.Fatal(err)
	}
	fragment := SetFragment(cfg, map[string][]string{"drop": {"198.51.100.0/24", "2001:db8::/32"}})

	script := "nft -f - <<'RULESET'\n" + ruleset + "RULESET\n" +
		"nft -f - <<'FRAGMENT'\n" + fragment + "FRAGMENT\n" +
		"nft -j list table inet ostiole\n"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "unshare", "-Urn", "sh", "-c", script).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "uid_map") || strings.Contains(string(out), "Operation not permitted") {
			t.Skipf("unprivileged namespaces are not allowed here: %s", strings.TrimSpace(string(out)))
		}
		t.Fatalf("apply failed: %v\n%s", err, out)
	}

	var dump struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		t.Fatalf("parse dump: %v\n%s", err, out)
	}
	sets := map[string]string{}
	rules := 0
	for _, item := range dump.Nftables {
		if raw, ok := item["set"]; ok {
			var s struct {
				Name string          `json:"name"`
				Elem json.RawMessage `json:"elem"`
			}
			if err := json.Unmarshal(raw, &s); err == nil {
				sets[s.Name] = string(s.Elem)
			}
		}
		if _, ok := item["rule"]; ok {
			rules++
		}
	}

	v4 := sets["alias_drop_v4"]
	if strings.Contains(v4, "192.0.2.0") {
		t.Errorf("the old list is still in the set: %s", v4)
	}
	for _, want := range []string{"198.51.100.0", "203.0.113.1"} {
		if !strings.Contains(v4, want) {
			t.Errorf("set is missing %q: %s", want, v4)
		}
	}
	if v6 := sets["alias_drop_v6"]; !strings.Contains(v6, "2001:db8::") {
		t.Errorf("the IPv6 set was not filled, so a list that gains IPv6 would be ignored: %s", v6)
	}
	// The rules are still there: a list update is not a firewall reload.
	if rules == 0 {
		t.Error("the ruleset lost its rules")
	}
	if _, ok := sets["alias_drop_v6"]; !ok {
		t.Error("a fetched alias should always have both sets to fill")
	}
}
