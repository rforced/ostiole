package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/acme"
	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// certServer is a logged-in server with a certificate manager and a
// store over a throwaway directory.
func certServer(t *testing.T) (*httptest.Server, *certs.Manager, *certs.Store) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := certs.New(filepath.Join(dir, "tls", "cert.pem"), filepath.Join(dir, "tls", "key.pem"))
	if _, err := m.EnsureSelfSigned([]string{"fw.lan", "192.168.1.1"}); err != nil {
		t.Fatal(err)
	}
	cs := certs.NewStore(filepath.Join(dir, "certs"))
	srv := httptest.NewServer(Handler(Deps{
		Engine: eng, Auth: as, Certs: m, CertStore: cs,
		Renewer:   &acme.Renewer{Store: cs, Config: eng.Effective},
		Tokens:    tokensFor(t, dir),
		CertHosts: func() []string { return []string{"fw.lan", "192.168.1.1", "10.0.0.1"} },
	}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, m, cs
}

func tokensFor(t *testing.T, dir string) *auth.Tokens {
	t.Helper()
	tk, err := auth.NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

type certificatesPage struct {
	BuiltIn *struct {
		Names      []string `json:"names"`
		Hosts      []string `json:"hosts"`
		SelfSigned bool     `json:"selfSigned"`
	} `json:"builtIn"`
	Certificates  []acme.Status `json:"certificates"`
	ProviderKinds []struct {
		Kind   string `json:"kind"`
		Fields []struct {
			Key    string `json:"key"`
			Secret bool   `json:"secret"`
		} `json:"fields"`
	} `json:"providerKinds"`
}

func TestCertificatesEndpoint(t *testing.T) {
	t.Parallel()
	srv, _, _ := certServer(t)

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/certificates", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET certificates: %d %s", resp.StatusCode, raw)
	}
	var got certificatesPage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.BuiltIn == nil || !got.BuiltIn.SelfSigned || len(got.BuiltIn.Names) != 2 {
		t.Errorf("built-in = %+v", got.BuiltIn)
	}
	// The hosts list is what the UI compares against to warn about an
	// address the certificate does not cover.
	if len(got.BuiltIn.Hosts) != 3 {
		t.Errorf("hosts = %v", got.BuiltIn.Hosts)
	}
	// The dialog builds its fields from this, so it has to come with the
	// page rather than being written out in the UI.
	if len(got.ProviderKinds) == 0 {
		t.Fatal("no provider kinds")
	}
	if len(got.ProviderKinds[0].Fields) == 0 {
		t.Errorf("provider %s has no fields", got.ProviderKinds[0].Kind)
	}
}

func TestRegenerateCertificateEndpoint(t *testing.T) {
	t.Parallel()
	srv, m, _ := certServer(t)
	before, err := m.Info()
	if err != nil {
		t.Fatal(err)
	}

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/certificates/self-signed", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("regenerate: %d %s", resp.StatusCode, raw)
	}
	after, err := m.Info()
	if err != nil {
		t.Fatal(err)
	}
	if after.Fingerprint == before.Fingerprint {
		t.Error("the certificate was not replaced")
	}
	// It covers every name the router currently answers to, which is the
	// reason to regenerate in the first place.
	if strings.Join(after.Names, ",") != "10.0.0.1,192.168.1.1,fw.lan" {
		t.Errorf("names = %v", after.Names)
	}
}

func TestAccountKeyEndpoint(t *testing.T) {
	t.Parallel()
	srv, _, _ := certServer(t)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/certificates/keys", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("key: %d %s", resp.StatusCode, raw)
	}
	var got struct {
		PrivateKey string `json:"privateKey"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.PrivateKey, "BEGIN EC PRIVATE KEY") {
		t.Errorf("key = %q", got.PrivateKey)
	}
}

func TestCertificateFilesEndpoints(t *testing.T) {
	t.Parallel()
	srv, _, cs := certServer(t)

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/certificates/web/files", nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(raw), "no certificate called") {
		t.Errorf("files of an unknown certificate = %d %s, want 404 saying so", resp.StatusCode, raw)
	}
	// The mux unescapes the id, so a path through it must be refused
	// before it reaches the store: the built-in key lives one directory up.
	for _, id := range []string{"..%2Ftls", "..%2F..%2Ftls", "%2E%2E%2Ftls"} {
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/certificates/"+id+"/files/key.pem", nil)
		if resp.StatusCode != http.StatusNotFound || strings.Contains(string(raw), "PRIVATE KEY") {
			t.Errorf("files/%s = %d %q", id, resp.StatusCode, raw)
		}
	}

	certPEM, keyPEM, err := certs.GenerateSelfSigned([]string{"web.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Write("web", certs.Files{Cert: certPEM, FullChain: certPEM, Key: keyPEM}); err != nil {
		t.Fatal(err)
	}

	resp, raw = do(t, srv, http.MethodGet, "/api/v1/certificates/web/files", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("files: %d %s", resp.StatusCode, raw)
	}
	var bundle struct {
		Certificate string `json:"certificate"`
		Key         string `json:"key"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Certificate != string(certPEM) || bundle.Key != string(keyPEM) || bundle.Fingerprint == "" {
		t.Errorf("bundle = %+v", bundle)
	}

	resp, raw = do(t, srv, http.MethodGet, "/api/v1/certificates/web/files/fullchain.pem", nil)
	if resp.StatusCode != http.StatusOK || string(raw) != string(certPEM) {
		t.Errorf("fullchain = %d %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-pem-file" {
		t.Errorf("content type = %q", got)
	}
	resp, _ = do(t, srv, http.MethodGet, "/api/v1/certificates/web/files/private.key", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an invented file name = %d, want 400", resp.StatusCode)
	}

	resp, raw = do(t, srv, http.MethodPost, "/api/v1/certificates/web/pkcs12", map[string]string{"password": "hunter2"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pkcs12: %d %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-pkcs12" {
		t.Errorf("content type = %q", got)
	}
	if len(raw) == 0 {
		t.Error("the PKCS#12 file is empty")
	}
}

// A token limited to one certificate fetches that certificate and
// nothing else on the API, whatever its role says.
func TestRestrictedTokenReachesOnlyItsCertificate(t *testing.T) {
	t.Parallel()
	srv, _, cs := certServer(t)
	certPEM, keyPEM, err := certs.GenerateSelfSigned([]string{"web.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Write("web", certs.Files{Cert: certPEM, FullChain: certPEM, Key: keyPEM}); err != nil {
		t.Fatal(err)
	}

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/tokens", map[string]any{
		"name": "proxy", "role": "admin", "certificates": []string{"web"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: %d %s", resp.StatusCode, raw)
	}
	var created struct {
		Secret       string   `json:"secret"`
		Certificates []string `json:"certificates"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Certificates) != 1 {
		t.Errorf("token = %+v, want it limited", created)
	}

	for path, want := range map[string]int{
		"/api/v1/certificates/web/files":  http.StatusOK,
		"/api/v1/certificates/mail/files": http.StatusForbidden,
		// An admin token would read these; a restricted one does not.
		"/api/v1/config":       http.StatusForbidden,
		"/api/v1/certificates": http.StatusForbidden,
		"/api/v1/tokens":       http.StatusForbidden,
	} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+created.Secret)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s with a restricted token = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// Without TLS there is nothing built in to describe, and the page still
// has to answer: that is the whole certificates page on a dev run.
func TestCertificatesEndpointWithoutTLS(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/certificates", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", resp.StatusCode, raw)
	}
	var got certificatesPage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.BuiltIn != nil {
		t.Errorf("built-in = %+v, want none", got.BuiltIn)
	}
	resp, _ = do(t, srv, http.MethodPost, "/api/v1/certificates/self-signed", map[string]any{})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("regenerate without TLS = %d, want 503", resp.StatusCode)
	}
}
