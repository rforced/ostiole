package server

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"ostiole/internal/engine"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
)

func readLogLimits(t *testing.T, cfg *model.Config, mem uint64) (map[string]json.RawMessage, map[string]int) {
	t.Helper()
	dir := t.TempDir()
	if cfg != nil {
		if _, err := store.New(dir).Save(cfg, ""); err != nil {
			t.Fatal(err)
		}
	}
	a := &api{
		engine:   engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler)),
		memTotal: func() uint64 { return mem },
	}
	rec := httptest.NewRecorder()
	if err := a.logLimits(rec, httptest.NewRequest(http.MethodGet, "/api/v1/logs/limits", nil)); err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if keys := slices.Sorted(maps.Keys(body)); !slices.Equal(keys, []string{"budget", "ceilings", "memTotal", "peakFactor", "reserve"}) {
		t.Fatalf("fields = %v", keys)
	}
	var ceilings map[string]int
	if err := json.Unmarshal(body["ceilings"], &ceilings); err != nil {
		t.Fatal(err)
	}
	return body, ceilings
}

func TestLogLimitsFollowTheMemory(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.Services.DNS.QueryLog.Entries = 5_000_000
	body, ceilings := readLogLimits(t, cfg, 950_000_000)
	for field, want := range map[string]string{"memTotal": "950000000", "reserve": "525000000", "budget": "425000000", "peakFactor": "1.5"} {
		if got := string(body[field]); got != want {
			t.Errorf("%s = %s, want %s", field, got, want)
		}
	}
	want := map[string]int{
		"firewall": 809_523, "queries": 1_888_888, "events": 184_461, "destinations": 1_416_666, "requests": 708_333,
		"dhcp": 944_444, "wireless": 1_000_000, "wireguard": 1_000_000, "tailscale": 1_000_000,
	}
	if !maps.Equal(ceilings, want) {
		t.Errorf("ceilings = %v, want %v", ceilings, want)
	}
}

// Memory it cannot read refuses nothing, so the ceilings are the most each
// log may ever keep.
func TestLogLimitsWithoutTheMemory(t *testing.T) {
	t.Parallel()
	want := map[string]int{
		"firewall": model.MaxFirewallLogEntries, "queries": model.MaxQueryLogEntries, "events": model.MaxProxyEventEntries,
		"destinations": model.MaxDestinationEntries, "requests": model.MaxRequestEntries, "dhcp": model.MaxDHCPLogEntries,
		"wireless": model.MaxWirelessLogEntries, "wireguard": model.MaxPeerLogEntries, "tailscale": model.MaxPeerLogEntries,
	}
	for name, cfg := range map[string]*model.Config{"saved": starter(), "none saved": nil} {
		body, ceilings := readLogLimits(t, cfg, 0)
		for _, field := range []string{"memTotal", "reserve", "budget"} {
			if got := string(body[field]); got != "0" {
				t.Errorf("%s: %s = %s, want 0", name, field, got)
			}
		}
		if !maps.Equal(ceilings, want) {
			t.Errorf("%s: ceilings = %v, want %v", name, ceilings, want)
		}
	}
}

func TestLogLimitsNameEveryLogInMemory(t *testing.T) {
	t.Parallel()
	logs := model.MemoryBudget{}.MemoryLogs(starter())
	if len(logs) != len(logNames) {
		t.Errorf("%d logs in memory, %d named", len(logs), len(logNames))
	}
	for _, l := range logs {
		if _, ok := logNames[l.Path]; !ok {
			t.Errorf("%s has no name", l.Path)
		}
	}
}
