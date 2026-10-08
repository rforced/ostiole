package acme

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"ostiole/internal/certs"
	"ostiole/internal/dnsclient/dnstest"
	"ostiole/internal/model"
)

func fakeAccount(t *testing.T, f *fakeCA) model.ACMEAccount {
	t.Helper()
	key, err := certs.GenerateAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	return model.ACMEAccount{ID: "fake", Directory: f.url("/dir"), Email: "admin@example.test", PrivateKey: string(key), CACert: f.trust()}
}

func fakeClient(t *testing.T) *Client {
	t.Helper()
	c := NewClient(func() *model.Config { return &model.Config{} }, t.TempDir())
	// The fake does not fetch the answer; the solver just needs a port.
	c.Addr = func() string { return "127.0.0.1:0" }
	c.Log = slog.New(slog.DiscardHandler)
	return c
}

func httpOrder(account model.ACMEAccount, names ...string) Request {
	return Request{Account: account, Names: names, Challenge: model.ChallengeHTTP}
}

// An order runs through: an account made, both names checked, the key
// written as SEC 1, and the chain split from the leaf. The second
// order finds the account in the cache and asks nobody who it is.
func TestIssueFromTheFakeCA(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	c := fakeClient(t)
	account := fakeAccount(t, f)
	issued, err := c.Issue(context.Background(), httpOrder(account, "www.example.test", "example.test"))
	if err != nil {
		t.Fatal(err)
	}
	leaf := parse(t, issued.Cert)
	if leaf.Subject.CommonName != "www.example.test" || !slices.Equal(leaf.DNSNames, []string{"www.example.test", "example.test"}) {
		t.Errorf("certificate for %q %v", leaf.Subject.CommonName, leaf.DNSNames)
	}
	if block, _ := pem.Decode(issued.Key); block == nil || block.Type != "EC PRIVATE KEY" {
		t.Errorf("key is not SEC 1 PEM: %q", issued.Key)
	}
	if chain := parse(t, issued.Chain); chain.Subject.CommonName != "fake CA" {
		t.Errorf("chain starts with %q", chain.Subject.CommonName)
	}
	if !strings.HasPrefix(string(issued.FullChain), string(issued.Cert)) || !strings.HasPrefix(issued.CertURL, f.url("/cert/")) {
		t.Errorf("full chain or URL wrong: %q", issued.CertURL)
	}
	if n := f.saw("POST /account"); n != 2 {
		t.Errorf("%d account requests, want a lookup and a create", n)
	}
	if _, err := c.Issue(context.Background(), httpOrder(account, "example.test")); err != nil {
		t.Fatal(err)
	}
	if n := f.saw("POST /account"); n != 2 || f.created != 1 {
		t.Errorf("the second order asked for the account again: %d requests, %d created", n, f.created)
	}
}

// A run of refused nonces is ridden out with fresh ones.
func TestIssueRidesOutRefusedNonces(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	f.badNonces = 3
	if _, err := fakeClient(t).Issue(context.Background(), httpOrder(fakeAccount(t, f), "example.test")); err != nil {
		t.Fatal(err)
	}
}

// Any other refusal ends the request, and says what the CA said.
func TestIssueSaysWhatTheCARefused(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	f.rateLimited = true
	_, err := fakeClient(t).Issue(context.Background(), httpOrder(fakeAccount(t, f), "example.test"))
	if err == nil || err.Error() != "placing the order: too many certificates already issued for example.test (rateLimited)" {
		t.Errorf("err = %v", err)
	}
	if n := f.saw("POST /order"); n != 1 {
		t.Errorf("rate limited order sent %d times", n)
	}
}

// A challenge the CA could not check fails the order, and the order's
// authorizations that are not valid are given up.
func TestAFailedChallengeGivesUpTheOrder(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	f.failing["b.example.test"] = true
	_, err := fakeClient(t).Issue(context.Background(), httpOrder(fakeAccount(t, f), "a.example.test", "b.example.test", "c.example.test"))
	want := "the CA could not check b.example.test: The key authorization file from the server did not match this challenge (incorrectResponse)"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	if got := f.deactivatedNames(); !slices.Equal(got, []string{"b.example.test", "c.example.test"}) {
		t.Errorf("deactivated %v, want what was not valid", got)
	}
}

// A finalize the CA is still working on is read again until it is done.
func TestIssueWaitsForAFinalizeStillProcessing(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	f.processing = 2
	if _, err := fakeClient(t).Issue(context.Background(), httpOrder(fakeAccount(t, f), "example.test")); err != nil {
		t.Fatal(err)
	}
	if n := f.saw("POST /order/"); n < 2 {
		t.Errorf("the order was read %d times", n)
	}
}

// A renewal names the certificate it replaces; one the CA has already
// seen replaced is renewed plainly.
func TestRenewalNamesWhatItReplaces(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	c := fakeClient(t)
	account := fakeAccount(t, f)
	first, err := c.Issue(context.Background(), httpOrder(account, "example.test"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := certID(parse(t, first.Cert))
	req := httpOrder(account, "example.test")
	req.Replaces = first.Cert
	if _, err := c.Issue(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	f.alreadyReplaced = true
	if _, err := c.Issue(context.Background(), req); err != nil {
		t.Fatalf("an order the CA says was replaced: %v", err)
	}
	if !slices.Equal(f.replaces, []string{id, id}) || f.saw("POST /order") != 4 {
		t.Errorf("replaces %v over %d orders", f.replaces, f.saw("POST /order"))
	}
}

// A certificate moved to another account at the same CA is renewed
// plainly: Let's Encrypt lets only the account that ordered a certificate
// replace it, and a renewal it refuses never happens.
func TestARenewalByAnotherAccountIsPlacedPlainly(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	c := fakeClient(t)
	first, err := c.Issue(context.Background(), httpOrder(fakeAccount(t, f), "example.test"))
	if err != nil {
		t.Fatal(err)
	}
	other := fakeAccount(t, f)
	other.ID = "other"
	req := httpOrder(other, "example.test")
	req.Replaces = first.Cert
	if _, err := c.Issue(context.Background(), req); err != nil {
		t.Fatalf("a renewal by another account: %v", err)
	}
	id, _ := certID(parse(t, first.Cert))
	if !slices.Equal(f.replaces, []string{id}) || f.saw("POST /order") != 3 {
		t.Errorf("replaces %v over %d orders, want one refused and placed again", f.replaces, f.saw("POST /order"))
	}
}

// An account the CA has forgotten, and the cache still names, is found
// again and the order placed once more.
func TestAForgottenAccountIsFoundAgain(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	c := fakeClient(t)
	account := fakeAccount(t, f)
	if _, err := c.Issue(context.Background(), httpOrder(account, "example.test")); err != nil {
		t.Fatal(err)
	}
	f.forgetAccounts = true
	if _, err := c.Issue(context.Background(), httpOrder(account, "example.test")); err != nil {
		t.Fatal(err)
	}
	if f.created != 2 {
		t.Errorf("%d accounts created, want the forgotten one made again", f.created)
	}
}

// A CA that wants an external account binding gets one.
func TestIssueBindsTheAccount(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	f.eabKID = "kid-1"
	f.eabKey, _ = decodeMAC("zWNDZM6eQGHWpSRTPal5eIUYFTu7EajVIoguysqZ9wG44nMEtx3MUAsUDkMTQ12W")
	account := fakeAccount(t, f)
	_, err := fakeClient(t).Issue(context.Background(), httpOrder(account, "example.test"))
	if err == nil || !strings.Contains(err.Error(), "externalAccountRequired") {
		t.Errorf("without a binding: %v", err)
	}
	account.EABKeyID, account.EABHMAC = "kid-1", "zWNDZM6eQGHWpSRTPal5eIUYFTu7EajVIoguysqZ9wG44nMEtx3MUAsUDkMTQ12W"
	if _, err := fakeClient(t).Issue(context.Background(), httpOrder(account, "example.test")); err != nil {
		t.Fatal(err)
	}
}

// An RSA account key signs with RS256.
func TestIssueWithAnRSAAccountKey(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	account := fakeAccount(t, f)
	account.PrivateKey = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	req := httpOrder(account, "example.test")
	req.KeyType = "rsa2048"
	issued, err := fakeClient(t).Issue(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if block, _ := pem.Decode(issued.Key); block == nil || block.Type != "RSA PRIVATE KEY" {
		t.Errorf("certificate key is not PKCS #1 PEM")
	}
}

// Every name through dns-01 goes up before the first is checked, and all
// of them come down at the end.
func TestDNS01RecordsGoUpTogether(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	c := fakeClient(t)
	c.relaxPropagation = true
	// Each CNAME chase is asked of a server that holds nothing, so the
	// records go at _acme-challenge.
	srv := dnstest.New(t)
	cfg := &model.Config{System: model.System{DNSServers: []string{srv.Addr.String()}}}
	c.Config = func() *model.Config { return cfg }
	log := t.TempDir() + "/calls"
	program := t.TempDir() + "/hook.sh"
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho \"$1 $2\" >> "+log+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The program kind answers one challenge at a time; with that turned
	// off below, it answers them together as the others do.
	provider := model.DNSProvider{ID: "p", Kind: "exec", Settings: map[string]string{"program": program}, Domains: []string{"example.test"}}
	req := Request{Account: fakeAccount(t, f), Names: []string{"a.example.test", "b.example.test"}, Challenge: model.ChallengeDNS, Provider: &provider}
	ch, err := c.challenger(req)
	if err != nil {
		t.Fatal(err)
	}
	d := ch.(*dns01)
	d.spec.Sequential = 0
	cn, err := c.dial(context.Background(), req.Account.Directory, req.Account.CACert, mustSigner(t, req.Account))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.account(context.Background(), cn, req.Account); err != nil {
		t.Fatal(err)
	}
	if _, err := c.place(context.Background(), cn, req, d); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(log)
	want := "present _acme-challenge.a.example.test.\npresent _acme-challenge.b.example.test.\ncleanup _acme-challenge.a.example.test.\ncleanup _acme-challenge.b.example.test.\n"
	if string(raw) != want {
		t.Errorf("program ran:\n%s\nwant:\n%s", raw, want)
	}
}

func mustSigner(t *testing.T, a model.ACMEAccount) *signer {
	t.Helper()
	key, err := parseKey(a.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The CA's renewal window, and when to ask again.
func TestRenewalInfoReadsTheWindow(t *testing.T) {
	t.Parallel()
	f := newFakeCA(t)
	c := fakeClient(t)
	account := fakeAccount(t, f)
	issued, err := c.Issue(context.Background(), httpOrder(account, "example.test"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := c.RenewalInfo(context.Background(), account, issued.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if w.Start != time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) || w.End != time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) {
		t.Errorf("window = %+v", w)
	}
	if d := time.Until(w.RetryAfter); d < 5*time.Hour || d > 6*time.Hour {
		t.Errorf("retry after %s", d)
	}
}

// Nothing but HTTPS reaches a CA.
func TestOnlyHTTPS(t *testing.T) {
	t.Parallel()
	account := model.ACMEAccount{ID: "plain", Directory: "http://ca.example.test/dir", PrivateKey: fakeKey(t)}
	_, err := fakeClient(t).Issue(context.Background(), httpOrder(account, "example.test"))
	if err == nil || !strings.Contains(err.Error(), "is not HTTPS") {
		t.Errorf("err = %v", err)
	}
}

func fakeKey(t *testing.T) string {
	t.Helper()
	key, err := certs.GenerateAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	return string(key)
}

func parse(t *testing.T, pemCert []byte) *x509.Certificate {
	t.Helper()
	leaf, err := parseLeaf(pemCert)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}
