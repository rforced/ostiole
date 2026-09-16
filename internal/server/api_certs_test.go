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

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// certServer is a logged-in server with a certificate manager over a
// throwaway directory.
func certServer(t *testing.T) (*httptest.Server, *certs.Manager) {
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
	srv := httptest.NewServer(Handler(Deps{
		Engine: eng, Auth: as, Certs: m,
		CertHosts: func() []string { return []string{"fw.lan", "192.168.1.1", "10.0.0.1"} },
	}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, m
}

func TestCertificateEndpoint(t *testing.T) {
	t.Parallel()
	srv, _ := certServer(t)

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/certificate", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET certificate: %d %s", resp.StatusCode, raw)
	}
	var got struct {
		Names      []string `json:"names"`
		Hosts      []string `json:"hosts"`
		SelfSigned bool     `json:"selfSigned"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.SelfSigned || len(got.Names) != 2 {
		t.Errorf("certificate = %+v", got)
	}
	// The hosts list is what the UI compares against to warn about an
	// address the certificate does not cover.
	if len(got.Hosts) != 3 {
		t.Errorf("hosts = %v", got.Hosts)
	}
}

func TestInstallCertificateEndpoint(t *testing.T) {
	t.Parallel()
	srv, m := certServer(t)

	certPEM, keyPEM, err := certs.GenerateSelfSigned([]string{"new.example"})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"certificate": string(certPEM), "key": string(keyPEM)}
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/certificate", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("install: %d %s", resp.StatusCode, raw)
	}
	info, err := m.Info()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(info.Names, ",") != "new.example" {
		t.Errorf("installed names = %v", info.Names)
	}

	// A key that belongs to another certificate is refused, and the box
	// keeps serving what it had.
	_, otherKey, _ := certs.GenerateSelfSigned([]string{"other.example"})
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/certificate",
		map[string]string{"certificate": string(certPEM), "key": string(otherKey)})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched pair: %d %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "does not match") {
		t.Errorf("error = %s", raw)
	}
	after, err := m.Info()
	if err != nil || strings.Join(after.Names, ",") != "new.example" {
		t.Errorf("the working certificate was disturbed: %+v, %v", after, err)
	}
}

func TestRegenerateCertificateEndpoint(t *testing.T) {
	t.Parallel()
	srv, m := certServer(t)
	before, err := m.Info()
	if err != nil {
		t.Fatal(err)
	}

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/certificate/self-signed", map[string]any{})
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
	// It covers every name the box currently answers to, which is the
	// reason to regenerate in the first place.
	if strings.Join(after.Names, ",") != "10.0.0.1,192.168.1.1,fw.lan" {
		t.Errorf("names = %v", after.Names)
	}
}

// Without TLS there is nothing to manage, and saying so beats a stack
// trace in the browser console.
func TestCertificateEndpointWithoutTLS(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	resp, _ := do(t, srv, http.MethodGet, "/api/v1/certificate", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}
