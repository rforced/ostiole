//go:build acme

// Issuance against pebble, the Let's Encrypt test CA. It is behind a tag
// because it needs two containers; scripts/ci/acme-test.sh starts them.
package acme

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	c := NewClient(func() *model.Config { return cfg }, t.TempDir())
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
	// The domain is the zone: the challenge server answers no SOA.
	return model.DNSProvider{
		ID: "challtestsrv", Kind: "exec",
		Settings: map[string]string{"program": path},
		Domains:  []string{"example.test"},
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

// A wildcard and its apex put two values at one name. They go up
// together, so the challenge server has to keep both, and both are
// checked before either comes down.
func TestPebbleIssuesAWildcardWithItsApex(t *testing.T) {
	c, account := pebbleClient(t)
	provider := execProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req := Request{Account: account, Provider: &provider, Names: []string{"*.apex.example.test", "apex.example.test"}, Challenge: model.ChallengeDNS}
	ch, err := c.challenger(req)
	if err != nil {
		t.Fatal(err)
	}
	// The program kind answers one challenge a minute; the kinds that
	// answer together are the case here.
	ch.(*dns01).spec.Sequential = 0
	cn, err := c.dial(ctx, account.Directory, account.CACert, mustSigner(t, account))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.account(ctx, cn, account); err != nil {
		t.Fatal(err)
	}
	issued, err := c.place(ctx, cn, req, ch)
	if err != nil {
		t.Fatal(err)
	}
	if got := parse(t, issued.Cert).DNSNames; !slices.Equal(got, []string{"*.apex.example.test", "apex.example.test"}) {
		t.Errorf("names = %v", got)
	}
}

// An RSA account key signs with RS256, and an RSA certificate key comes
// back as PKCS #1.
func TestPebbleTakesAnRSAAccount(t *testing.T) {
	c, account := pebbleClient(t)
	c.Addr = func() string { return ":5002" }
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	account.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	issued, err := c.Issue(ctx, Request{Account: account, Names: []string{"rsa.example.test"}, Challenge: model.ChallengeHTTP, KeyType: "rsa2048"})
	if err != nil {
		t.Fatal(err)
	}
	if block, _ := pem.Decode(issued.Key); block == nil || block.Type != "RSA PRIVATE KEY" {
		t.Error("the certificate key is not PKCS #1")
	}
}

// The second order signs with the account URL the first one cached, and a
// client without the cache finds the same account by its key.
func TestPebbleFindsTheAccountAgain(t *testing.T) {
	c, account := pebbleClient(t)
	c.Addr = func() string { return ":5002" }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, name := range []string{"cache1.example.test", "cache2.example.test"} {
		if _, err := c.Issue(ctx, Request{Account: account, Names: []string{name}, Challenge: model.ChallengeHTTP}); err != nil {
			t.Fatal(err)
		}
	}
	uri := c.cached(account)
	if uri == "" {
		t.Fatal("nothing cached")
	}
	fresh := NewClient(c.Config, t.TempDir())
	fresh.Addr = c.Addr
	if _, err := fresh.Issue(ctx, Request{Account: account, Names: []string{"cache3.example.test"}, Challenge: model.ChallengeHTTP}); err != nil {
		t.Fatal(err)
	}
	if got := fresh.cached(account); got != uri {
		t.Errorf("the key found %q, want the account %q", got, uri)
	}
}

// A challenge the CA cannot check fails the order with the CA's reason.
func TestPebbleSaysAChallengeFailed(t *testing.T) {
	c, account := pebbleClient(t)
	// pebble looks on 5002, where nothing answers now.
	c.Addr = func() string { return ":5003" }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := c.Issue(ctx, Request{Account: account, Names: []string{"fail.example.test"}, Challenge: model.ChallengeHTTP})
	if err == nil || !strings.HasPrefix(err.Error(), "the CA could not check fail.example.test: ") {
		t.Errorf("err = %v", err)
	}
}

// A CA that wants an external account binding refuses an account without
// one and takes one with it.
func TestPebbleBindsAnExternalAccount(t *testing.T) {
	directory := os.Getenv("PEBBLE_EAB_DIRECTORY")
	if directory == "" {
		t.Skip("PEBBLE_EAB_DIRECTORY is unset; run scripts/ci/acme-test.sh")
	}
	c, account := pebbleClient(t)
	c.Addr = func() string { return ":5002" }
	account.ID, account.Directory = "pebble-eab", directory
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req := Request{Account: account, Names: []string{"eab.example.test"}, Challenge: model.ChallengeHTTP}
	if _, err := c.Issue(ctx, req); err == nil || !strings.Contains(err.Error(), "externalAccountRequired") {
		t.Errorf("without a binding: %v", err)
	}
	// One of the keys pebble's own EAB configuration holds.
	req.Account.EABKeyID, req.Account.EABHMAC = "kid-1", "zWNDZM6eQGHWpSRTPal5eIUYFTu7EajVIoguysqZ9wG44nMEtx3MUAsUDkMTQ12W"
	if _, err := c.Issue(ctx, req); err != nil {
		t.Fatal(err)
	}
}
