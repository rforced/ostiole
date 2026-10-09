package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"ostiole/internal/auth"
	"ostiole/internal/model"
)

// unusedDraft is a starter router with an alias nothing names and a rule
// switched off.
func unusedDraft() *model.Config {
	cfg := starter()
	cfg.Aliases = []model.Alias{{Name: "spare_hosts", Type: model.AliasHosts, Entries: []string{"203.0.113.0/24"}}}
	cfg.Rules = append(cfg.Rules, model.Rule{ID: "old-rule", Zone: "lan", Action: model.ActionDrop, Protocol: model.ProtocolAny})
	return cfg
}

// A viewer may ask what a configuration leaves unused: the body is the
// configuration, as the page holds its draft.
func TestUnusedListsWhatAConfigurationLeavesUnused(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	viewer := mintToken(t, srv, "look", string(auth.RoleViewer))

	resp, raw := sendAs(t, srv, "/api/v1/config/unused", viewer, unusedDraft())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unused: %d %s", resp.StatusCode, raw)
	}
	var got unusedResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	wantUnused := []model.Unused{{Kind: "alias", ID: "spare_hosts", Name: "spare_hosts", Path: "aliases[spare_hosts]", Why: "Nothing names it"}}
	wantDisabled := []model.Disabled{{Kind: "rule", ID: "old-rule", Name: "old-rule", Path: "rules[old-rule]"}}
	if !slices.EqualFunc(got.Unused, wantUnused, func(a, b model.Unused) bool {
		return a.Kind == b.Kind && a.ID == b.ID && a.Name == b.Name && a.Path == b.Path && a.Why == b.Why && len(a.Takes) == 0
	}) || !slices.Equal(got.Disabled, wantDisabled) {
		t.Errorf("unused = %s", raw)
	}

	// Empty groups are lists, so the page has one shape to read.
	resp, raw = sendAs(t, srv, "/api/v1/config/unused", viewer, starter())
	if resp.StatusCode != http.StatusOK || string(raw) != `{"unused":[],"disabled":[]}`+"\n" {
		t.Errorf("nothing unused: %d %q", resp.StatusCode, raw)
	}

	for _, body := range []any{map[string]any{"made_up": true}, map[string]any{"zones": "lan"}, "not a configuration"} {
		if resp, raw := sendAs(t, srv, "/api/v1/config/unused", viewer, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: %d %s", body, resp.StatusCode, raw)
		}
	}
	if got := probe(t, srv, http.MethodPost, "/api/v1/config/unused", ""); got != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", got)
	}
}

// The dashboard says how much the saved configuration leaves unused, and
// nothing once it is all used. What is switched off never warns.
func TestOverviewPointsAtTheUnusedItems(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	apply := func(cfg *model.Config) {
		t.Helper()
		if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
			t.Fatalf("apply: %d %s", resp.StatusCode, raw)
		}
	}

	cfg := unusedDraft()
	cfg.Schedules = []model.Schedule{{Name: "evenings", Start: "18:00", End: "23:00"}}
	apply(cfg)
	w := warning(getOverview(t, srv), "unused")
	if w == nil || w.Level != "info" || w.Title != "Unused configuration" ||
		w.Detail != "Nothing uses 2 items. Details under System › Configuration › Unused." {
		t.Errorf("two unused: %+v", w)
	}

	apply(unusedDraft())
	if w := warning(getOverview(t, srv), "unused"); w == nil || w.Detail != "Nothing uses 1 item. Details under System › Configuration › Unused." {
		t.Errorf("one unused: %+v", w)
	}

	cfg = unusedDraft()
	cfg.Aliases = nil
	apply(cfg)
	if w := warning(getOverview(t, srv), "unused"); w != nil {
		t.Errorf("only a switched-off rule warned: %+v", w)
	}
}
