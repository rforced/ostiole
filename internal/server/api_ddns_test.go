package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"ostiole/internal/ddns"
	"ostiole/internal/dnsprovider"
	"ostiole/internal/model"
)

// stubProvider holds records in memory, or refuses everything.
type stubProvider struct {
	mu   sync.Mutex
	held map[string][]netip.Addr
	fail error
}

func (p *stubProvider) Lookup(_ context.Context, _, name string, t dnsprovider.RecordType) ([]netip.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held[name+" "+string(t)], p.fail
}

func (p *stubProvider) Set(_ context.Context, _, name string, t dnsprovider.RecordType, addr netip.Addr) ([]netip.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail != nil {
		return nil, p.fail
	}
	was := p.held[name+" "+string(t)]
	p.held[name+" "+string(t)] = []netip.Addr{addr}
	return was, nil
}

func (p *stubProvider) Test(_ context.Context, domains []string) (*dnsprovider.TestResult, error) {
	out := &dnsprovider.TestResult{Zones: []string{"example.com"}}
	for _, d := range domains {
		res := dnsprovider.DomainTest{Domain: d}
		if d != "example.com" {
			res.Error = "Cloudflare has no zone " + d + " that this token can see"
		}
		out.Domains = append(out.Domains, res)
	}
	return out, nil
}

func ddnsDraft() *model.Config {
	cfg := starter()
	cfg.DNSProviders = []model.DNSProvider{{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"token": "t"}, Domains: []string{"example.com"}}}
	cfg.Services.DDNS.Records = []model.DDNSRecord{
		{ID: "ddns-home", Enabled: true, Name: "home.example.com", Interface: "eth0", IPv4: true},
		{ID: "ddns-off", Name: "off.example.com", Interface: "eth0", IPv4: true},
	}
	return cfg
}

// The page reads each record's state; Update now and Check go to the
// provider; a record that is off or unknown is refused; a failure is a
// dashboard warning.
func TestDDNSEndpoints(t *testing.T) {
	t.Parallel()
	prov := &stubProvider{held: map[string][]netip.Addr{}}
	var up *ddns.Updater
	srv := newTestServerWith(t, func(d *Deps) {
		up = &ddns.Updater{
			Config: d.Engine.Effective,
			Addresses: func(string) ([]ddns.Addr, error) {
				return []ddns.Addr{{IP: netip.MustParseAddr("203.0.113.7")}}, nil
			},
			Build: func(model.DNSProvider, dnsprovider.Options) (ddns.Provider, error) { return prov, nil },
		}
		d.DDNS = up
	})
	status := func() ddnsResponse {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/ddns", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d %s", resp.StatusCode, raw)
		}
		var out ddnsResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := status(); len(got.Records) != 0 || len(got.Kinds) == 0 || got.Kinds[0].Kind != "cloudflare" {
		t.Errorf("before an apply: %+v", got)
	}

	// Check reads with the draft's credentials and writes nothing.
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/ddns/check",
		ddnsCheckRequest{Config: (*draftConfig)(ddnsDraft()), Record: &ddnsDraft().Services.DDNS.Records[0]})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"action":"create"`) {
		t.Errorf("check: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/ddns/check", ddnsCheckRequest{Config: (*draftConfig)(ddnsDraft())}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("check without a record: %d %s", resp.StatusCode, raw)
	}

	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(ddnsDraft())}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	up.Pass(context.Background())
	got := status()
	if len(got.Records) != 2 || got.Records[0].State != ddns.StateCurrent || got.Records[0].Address != "203.0.113.7" || got.Records[1].State != ddns.StateOff {
		t.Errorf("after a pass: %+v", got.Records)
	}

	for path, want := range map[string]int{
		"/api/v1/ddns/ddns-home/update": http.StatusAccepted,
		"/api/v1/ddns/ddns-off/update":  http.StatusBadRequest,
		"/api/v1/ddns/ddns-gone/update": http.StatusNotFound,
	} {
		if resp, raw := do(t, srv, http.MethodPost, path, nil); resp.StatusCode != want {
			t.Errorf("%s: %d %s, want %d", path, resp.StatusCode, raw, want)
		}
	}
	if got := status(); got.Records[0].State != ddns.StateUpdating {
		t.Errorf("after Update now: %+v", got.Records[0])
	}

	prov.mu.Lock()
	prov.fail = errors.New("Cloudflare refused the token for example.com (10000 Authentication error)")
	prov.mu.Unlock()
	up.Pass(context.Background())
	_, raw = do(t, srv, http.MethodGet, "/api/v1/overview", nil)
	if !strings.Contains(string(raw), "Dynamic DNS record home.example.com (A) is not updating") {
		t.Errorf("no dashboard warning for a failing record: %s", raw)
	}
}

// A server with nothing keeping records says so rather than pretending.
func TestDDNSWithoutAnUpdater(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/ddns/ddns-home/update", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("update: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/ddns", nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"records":[]`) {
		t.Errorf("status: %d %s", resp.StatusCode, raw)
	}
}

// A provider is tested as the dialog holds it; a kind with no client of
// Ostiole's own is refused rather than tried.
func TestDNSProviderTest(t *testing.T) {
	t.Parallel()
	prov := &stubProvider{held: map[string][]netip.Addr{}}
	srv := newTestServerWith(t, func(d *Deps) {
		d.DDNS = &ddns.Updater{
			Config: d.Engine.Effective,
			Build:  func(model.DNSProvider, dnsprovider.Options) (ddns.Provider, error) { return prov, nil },
		}
	})
	test := func(p model.DNSProvider) (*http.Response, []byte) {
		return do(t, srv, http.MethodPost, "/api/v1/dns-providers/test", providerTestRequest{Provider: &p})
	}
	resp, raw := test(model.DNSProvider{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"token": "t"}, Domains: []string{"example.com", "example.net"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "no zone example.net") {
		t.Errorf("cloudflare: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := test(model.DNSProvider{ID: "h", Kind: "hetzner"}); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "testing a Hetzner provider is not built in; Cloudflare can be tested") {
		t.Errorf("hetzner: %d %s", resp.StatusCode, raw)
	}
}
