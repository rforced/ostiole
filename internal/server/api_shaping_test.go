package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/shaping"
	"github.com/rforced/ostiole/internal/store"
)

// fakeShaper answers with a fixed sample instead of reading the kernel.
type fakeShaper struct {
	report  *shaping.Report
	pkg     string
	missing bool
	seen    *model.Config
}

func (f *fakeShaper) Status(_ context.Context, cfg *model.Config) (*shaping.Report, error) {
	f.seen = cfg
	return f.report, nil
}

func (f *fakeShaper) Missing() (string, bool) { return f.pkg, f.missing }

func newShapingServer(t *testing.T, sh Shaper) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Shaping: sh}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv
}

func TestShapingStatus(t *testing.T) {
	t.Parallel()
	sh := &fakeShaper{report: &shaping.Report{
		Available: true, TC: "/usr/sbin/tc",
		Interfaces: []shaping.InterfaceStatus{{
			Name: "eth0", Zone: "wan", External: true, Present: true, Link: model.LinkEthernet,
			Download: shaping.DirectionStatus{Rate: 200_000_000, Installed: true, Stats: &shaping.CakeStats{
				Bits: 200_000_000, Bytes: 4096,
				Tins: []shaping.TinStats{{Tier: model.TierBulk}, {Tier: model.TierNormal, SentBytes: 4096, PeakDelayUs: 1200}},
			}},
			Upload: shaping.DirectionStatus{Rate: 20_000_000},
		}},
	}}
	srv := newShapingServer(t, sh)

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/shaping", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("shaping: %d %s", resp.StatusCode, raw)
	}
	var got struct {
		Available  bool              `json:"available"`
		TC         string            `json:"tc"`
		Tiers      []model.Tier      `json:"tiers"`
		SampledAt  string            `json:"sampledAt"`
		Interfaces []json.RawMessage `json:"interfaces"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if !got.Available || got.TC == "" {
		t.Errorf("available = %v, tc = %q", got.Available, got.TC)
	}
	// The tier names come from the router so the page cannot drift out of
	// step with what the queues actually carry.
	if len(got.Tiers) != len(model.Tiers) || got.Tiers[0] != model.TierBulk {
		t.Errorf("tiers = %v", got.Tiers)
	}
	if len(got.Interfaces) != 1 {
		t.Fatalf("interfaces = %s", raw)
	}
	for _, want := range []string{`"peakDelayUs":1200`, `"rate":200000000`, `"installed":true`, `"tier":"normal"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("response is missing %s:\n%s", want, raw)
		}
	}
}

// Without a shaper the endpoint still answers, so the page renders what is
// configured and says nothing about queues it cannot see.
func TestShapingStatusWithoutABackend(t *testing.T) {
	t.Parallel()
	srv := newShapingServer(t, nil)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/shaping", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("shaping: %d %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"interfaces":[]`) {
		t.Errorf("want an empty list rather than null:\n%s", raw)
	}
}

// A router with speeds set and no tc says so on the dashboard: the
// configuration looks right and nothing is holding the queue.
func TestOverviewWarnsWhenTCIsMissing(t *testing.T) {
	t.Parallel()
	sh := &fakeShaper{report: &shaping.Report{}, pkg: "iproute-tc", missing: true}
	srv := newShapingServer(t, sh)

	cfg := model.Starter(model.StarterOptions{
		Hostname: "fw", LAN: "ost-lan0", LANAddress: "192.168.9.1/24", WAN: "ost-wan0",
	})
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "ost-wan0" {
			cfg.Interfaces[i].Shaping = &model.Shaping{Download: 200_000_000}
		}
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	ov := getOverview(t, srv)
	w := warning(ov, "tc-missing")
	if w == nil {
		t.Fatalf("want a tc-missing warning, got %+v", ov.Warnings)
	}
	if !strings.Contains(w.Detail, "iproute-tc") {
		t.Errorf("warning does not name the package: %q", w.Detail)
	}

	// With tc there, nothing is wrong and nothing is said.
	sh.missing = false
	if w := warning(getOverview(t, srv), "tc-missing"); w != nil {
		t.Errorf("warned about tc on a router that has it: %+v", w)
	}
}

// The dashboard of a router with no configuration at all still answers:
// there is nothing shaped, so there is nothing to say about the command
// that would shape it.
func TestOverviewOnAnUnconfiguredRouterWithAShaper(t *testing.T) {
	t.Parallel()
	srv := newShapingServer(t, &fakeShaper{report: &shaping.Report{}, pkg: "iproute-tc", missing: true})
	ov := getOverview(t, srv)
	if ov.Status.Configured {
		t.Fatalf("status = %+v, want unconfigured", ov.Status)
	}
	if w := warning(ov, "tc-missing"); w != nil {
		t.Errorf("warned about tc on a router that shapes nothing: %+v", w)
	}
}
