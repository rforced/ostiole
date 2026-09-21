package acme

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

// A dns-01 order on a router with nothing to resolve with is refused
// before a CA is contacted, rather than handed to lego's Google fallback.
func TestIssueRefusesDNS01WithoutAResolver(t *testing.T) {
	t.Parallel()
	empty := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(empty, []byte("# nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &model.Config{}
	c := &Client{Config: func() *model.Config { return cfg }, resolvConfPath: empty}
	req := Request{Account: model.ACMEAccount{PrivateKey: "junk"}, Challenge: model.ChallengeDNS, Names: []string{"a.example.test"}}
	if _, err := c.Issue(context.Background(), req); !errors.Is(err, errNoResolver) {
		t.Errorf("Issue without a resolver = %v, want errNoResolver", err)
	}
	if got := c.resolvers(); len(got) != 0 {
		t.Errorf("resolvers = %v, want none", got)
	}

	// With the DNS service on, the order goes ahead and fails on the next
	// thing, the account key, without reaching the network.
	cfg.Services.DNS.Enabled = true
	if got := c.resolvers(); len(got) != 1 || got[0] != "127.0.0.1:53" {
		t.Errorf("resolvers = %v, want the local service", got)
	}
	_, err := c.Issue(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "account key") {
		t.Errorf("Issue with a resolver = %v, want to fail on the account key", err)
	}
	if _, err := c.Issue(context.Background(), Request{Account: req.Account, Challenge: model.ChallengeHTTP}); err == nil || !strings.Contains(err.Error(), "account key") {
		t.Errorf("an http-01 order needs no resolver: %v", err)
	}
}

// Every order shares one http-01 solver, so two at once do not fight
// over the port, and the solver follows the client's address.
func TestClientSharesOneHTTP01Solver(t *testing.T) {
	t.Parallel()
	c := &Client{}
	first, second := c.http01(), c.http01()
	if first != second {
		t.Error("two solvers were made")
	}
	if got := c.http01().Addr(); got != "" {
		t.Errorf("addr without a setting = %q, want empty (port 80)", got)
	}
	c.Addr = func() string { return "127.0.0.1:8402" }
	if got := c.http01().Addr(); got != "127.0.0.1:8402" {
		t.Errorf("addr = %q, want the client's", got)
	}
}

// Only an EC or RSA key can sign for an account.
func TestParseKeyRefusesOtherAlgorithms(t *testing.T) {
	t.Parallel()
	if _, err := parseKey(ed25519PKCS8(t)); err == nil || !strings.Contains(err.Error(), "EC or RSA") {
		t.Errorf("parseKey(ed25519) = %v", err)
	}
}
