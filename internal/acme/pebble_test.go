//go:build acme

// Issuance against pebble, the Let's Encrypt test CA. It is behind a tag
// because it needs two containers; scripts/ci/acme-test.sh starts them.
package acme

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/model"
)

func pebbleClient(t *testing.T) (*Client, model.ACMEAccount) {
	t.Helper()
	directory, ca := os.Getenv("PEBBLE_DIRECTORY"), os.Getenv("PEBBLE_CA")
	if directory == "" || ca == "" {
		t.Skip("PEBBLE_DIRECTORY and PEBBLE_CA are unset; run scripts/ci/acme-test.sh")
	}
	root, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	key, err := certs.GenerateAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	account := model.ACMEAccount{
		ID: "pebble", Directory: directory, Email: "admin@example.test",
		PrivateKey: string(key), CACert: string(root),
	}
	// The propagation pre-check asks the challenge server, which is the
	// only resolver that knows about the records the test writes.
	cfg := &model.Config{System: model.System{DNSServers: []string{"127.0.0.1:8053"}}}
	c := NewClient("test", func() *model.Config { return cfg }, t.TempDir())
	c.relaxPropagation = true
	return c, account
}

// execProvider writes a script that puts the challenge record into the
// challenge server, which is what a real exec provider does with a real
// zone.
func execProvider(t *testing.T) model.DNSProvider {
	t.Helper()
	management := os.Getenv("CHALLTESTSRV")
	if management == "" {
		t.Skip("CHALLTESTSRV is unset")
	}
	path := filepath.Join(t.TempDir(), "challenge.sh")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "$1" in
  present) path=/set-txt ;;
  cleanup) path=/clear-txt ;;
  *) exit 1 ;;
esac
exec curl -sf -X POST -d "{\"host\":\"$2\",\"value\":\"$3\"}" %s$path
`, management)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return model.DNSProvider{
		ID: "challtestsrv", Kind: "exec",
		Settings: map[string]string{"program": path},
	}
}

func TestPebbleIssuesOverDNS01(t *testing.T) {
	c, account := pebbleClient(t)
	provider := execProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	issued, err := c.Issue(ctx, Request{
		Account:  account,
		Provider: &provider,
		// One name: the exec provider solves sequentially, with a minute
		// between challenges.
		Names:     []string{"*.example.test"},
		Challenge: model.ChallengeDNS,
		KeyType:   "ec256",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaf := parse(t, issued.Cert)
	if got := leaf.DNSNames; len(got) != 1 || got[0] != "*.example.test" {
		t.Errorf("names = %v, want the wildcard", got)
	}
	if len(issued.Key) == 0 || len(issued.Chain) == 0 {
		t.Errorf("issued = %+v, want a key and a chain", issued)
	}

	// The CA publishes renewal windows, which is what decides when the
	// hourly pass renews.
	window, err := c.RenewalInfo(ctx, account, issued.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if !window.End.After(window.Start) {
		t.Errorf("window = %+v", window)
	}
}

func TestPebbleIssuesOverHTTP01(t *testing.T) {
	c, account := pebbleClient(t)
	// The challenge server answers every name with 127.0.0.1, so the CA
	// comes back to the solver here.
	c.Addr = func() string { return ":5002" }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	issued, err := c.Issue(ctx, Request{
		Account:   account,
		Names:     []string{"http.example.test"},
		Challenge: model.ChallengeHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := parse(t, issued.Cert).DNSNames; len(got) != 1 || got[0] != "http.example.test" {
		t.Errorf("names = %v", got)
	}
}

// An address is a certificate like any other, over http-01 and under the
// short-lived profile, which is the only way a CA issues one.
func TestPebbleIssuesAnAddress(t *testing.T) {
	c, account := pebbleClient(t)
	c.Addr = func() string { return ":5002" }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	issued, err := c.Issue(ctx, Request{
		Account:   account,
		Names:     []string{"127.0.0.1"},
		Challenge: model.ChallengeHTTP,
		Profile:   model.ProfileShortlived,
	})
	if err != nil {
		t.Fatal(err)
	}
	leaf := parse(t, issued.Cert)
	if len(leaf.IPAddresses) != 1 || leaf.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("addresses = %v, want the one asked for", leaf.IPAddresses)
	}
	if life := leaf.NotAfter.Sub(leaf.NotBefore); life > 7*24*time.Hour {
		t.Errorf("lifetime = %v, want the short-lived profile", life)
	}
	// Let's Encrypt refuses a CSR with an address in the common name.
	if cn := leaf.Subject.CommonName; cn != "" {
		t.Errorf("common name = %q, want none", cn)
	}
}

func parse(t *testing.T, pemCert []byte) *x509.Certificate {
	t.Helper()
	leaf, err := parseLeaf(pemCert)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}
