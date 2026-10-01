// Package acme gets certificates from a CA over ACME (RFC 8555), with
// renewal windows (RFC 9773) and certificates for addresses (RFC 8738).
// Everything else in Ostiole talks to the Issuer interface and the
// certificate store.
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/dnsclient"
	"github.com/rforced/ostiole/internal/dnsprovider"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/version"
)

// ErrNoARI says the CA does not publish renewal windows, so the renewer
// falls back to the certificate's own lifetime.
var ErrNoARI = errors.New("this CA does not publish renewal information")

// errNoResolver refuses a dns-01 order on a router with nothing to ask
// whether the record has landed, rather than asking a resolver nobody
// chose.
var errNoResolver = errors.New("no resolver to check the record with: turn on the DNS service or set the system DNS servers")

// Issuer gets one certificate from a CA. Renewer decides when.
type Issuer interface {
	Issue(ctx context.Context, req Request) (*Issued, error)
	RenewalInfo(ctx context.Context, account model.ACMEAccount, leaf []byte) (*Window, error)
}

// Request is one order.
type Request struct {
	Account   model.ACMEAccount
	Provider  *model.DNSProvider
	Names     []string
	Challenge model.Challenge
	KeyType   string
	Profile   string
	// Replaces is the certificate this one renews, PEM, which the order
	// names to the CA when it publishes renewal windows (RFC 9773 §5).
	Replaces []byte
}

// Issued is what came back, PEM throughout.
type Issued struct {
	Cert, Chain, FullChain, Key []byte
	CertURL                     string
}

// Window is when the CA would like the certificate renewed.
type Window struct {
	Start, End time.Time
	RetryAfter time.Time
}

// Client talks to a CA.
type Client struct {
	// UserAgent is what the CA sees, the same string the feeds send.
	UserAgent string
	// Config is the configuration in force, read for the resolvers a
	// dns-01 order asks and for nothing else.
	Config func() *model.Config
	// AccountsDir caches the account URL each key resolved to, so an
	// hourly pass does not ask the CA twice.
	AccountsDir string
	// Addr is where the http-01 solver listens; nil is port 80.
	Addr func() string
	Log  *slog.Logger

	// solver answers http-01 for every order, so two orders at once share
	// the port rather than fighting over it.
	solverOnce sync.Once
	solver     *HTTP01
	// resolvConfPath is read when the configuration names no resolver; a
	// test points it at an empty file.
	resolvConfPath string

	// relaxPropagation drops the check that the record reached the
	// zone's own nameservers. Only the test against pebble sets it: the
	// challenge server it writes to is not a zone and answers no SOA.
	relaxPropagation bool
}

// NewClient returns a client that names itself Ostiole. RFC 8555 asks
// for the version too, a SHOULD, which version.Agent leaves out.
func NewClient(cfg func() *model.Config, accountsDir string) *Client {
	return &Client{
		UserAgent:   version.Agent,
		Config:      cfg,
		AccountsDir: accountsDir,
		Log:         slog.Default(),
	}
}

// Issue orders one certificate.
func (c *Client) Issue(ctx context.Context, req Request) (*Issued, error) {
	if req.Challenge == model.ChallengeDNS && len(c.resolvers()) == 0 {
		return nil, errNoResolver
	}
	key, err := parseKey(req.Account.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("account key: %w", err)
	}
	s, err := newSigner(key)
	if err != nil {
		return nil, fmt.Errorf("account key: %w", err)
	}
	ch, err := c.challenger(req)
	if err != nil {
		return nil, err
	}
	cn, err := c.dial(ctx, req.Account.Directory, req.Account.CACert, s)
	if err != nil {
		return nil, err
	}
	cached := c.cached(req.Account) != ""
	if err := c.account(ctx, cn, req.Account); err != nil {
		return nil, err
	}
	issued, err := c.place(ctx, cn, req, ch)
	// The CA no longer knows the account the cache named: find it again.
	if cached && is(err, "accountDoesNotExist") {
		c.forget(req.Account)
		cn.kid = ""
		if err = c.account(ctx, cn, req.Account); err == nil {
			issued, err = c.place(ctx, cn, req, ch)
		}
	}
	if err != nil {
		c.log().Warn("certificate order failed", "names", req.Names, "err", err)
		return nil, err
	}
	return issued, nil
}

// challenger is what answers the order's challenges.
func (c *Client) challenger(req Request) (challenger, error) {
	switch req.Challenge {
	case model.ChallengeDNS:
		if req.Provider == nil {
			return nil, errors.New("no DNS provider for a dns-01 challenge")
		}
		p := *req.Provider
		kind := dnsprovider.Kinds[p.Kind]
		client, err := dnsprovider.Build(p, dnsprovider.Options{UserAgent: c.UserAgent, Resolvers: c.resolvers()})
		if err != nil {
			return nil, err
		}
		return &dns01{
			client: client, spec: kind, provider: p,
			resolver: dnsclient.Resolver{Servers: c.resolvers()},
			wait:     waitOr(p, kind.Wait), relax: c.relaxPropagation,
		}, nil
	case model.ChallengeHTTP:
		return http01Answer{c.http01()}, nil
	}
	return nil, fmt.Errorf("unknown challenge %q", req.Challenge)
}

// httpClient is what talks to the CA: two minutes a request and thirty
// seconds for the answer to start, the proxy the environment names, HTTPS
// all the way, and the account's own CA certificate trusted when it has
// one, for a private or test CA.
func (c *Client) httpClient(caCert string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	if caCert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caCert)) {
			return nil, errors.New("the CA certificate is not PEM")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Timeout:   2 * time.Minute,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("the CA sent the request on to %s, which is not HTTPS", req.URL)
			}
			if len(via) >= 10 {
				return errors.New("the CA sent the request on ten times")
			}
			return nil
		},
	}, nil
}

// http01 is the one solver every order shares.
func (c *Client) http01() *HTTP01 {
	c.solverOnce.Do(func() {
		c.solver = &HTTP01{Addr: func() string {
			if c.Addr != nil {
				return c.Addr()
			}
			return ""
		}}
	})
	return c.solver
}

// resolvers are what a dns-01 order asks about names: the DNS service when
// it is on, else the system's DNS servers, else resolv.conf's.
func (c *Client) resolvers() []netip.AddrPort {
	cfg := c.Config()
	if cfg == nil {
		return c.resolvConf()
	}
	if cfg.Services.DNS.Enabled {
		return []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:53")}
	}
	var out []netip.AddrPort
	for _, s := range cfg.System.DNSServers {
		if a, ok := parseResolver(s); ok {
			out = append(out, a)
		}
	}
	if len(out) > 0 {
		return out
	}
	return c.resolvConf()
}

func (c *Client) resolvConf() []netip.AddrPort {
	path := c.resolvConfPath
	if path == "" {
		path = "/etc/resolv.conf"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []netip.AddrPort
	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if a, ok := parseResolver(fields[1]); ok {
			out = append(out, a)
		}
	}
	return out
}

// parseResolver reads an address, with a port or without one.
func parseResolver(s string) (netip.AddrPort, bool) {
	if a, err := netip.ParseAddrPort(s); err == nil {
		return a, true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return netip.AddrPortFrom(a, 53), true
	}
	return netip.AddrPort{}, false
}

// forget drops the account URL the cache holds.
func (c *Client) forget(a model.ACMEAccount) {
	if c.AccountsDir != "" {
		_ = os.Remove(c.accountPath(a.ID))
	}
}

func (c *Client) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func parseKey(pemKey string) (crypto.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("this is not a PEM private key")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		switch key.(type) {
		case *ecdsa.PrivateKey, *rsa.PrivateKey:
			return key, nil
		}
		return nil, fmt.Errorf("a CA account key is EC or RSA, not %T", key)
	}
	return nil, fmt.Errorf("%q is not a private key", block.Type)
}

func parseLeaf(pemCert []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemCert)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("this is not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// splitLeaf cuts a bundle into its first certificate and the rest.
func splitLeaf(bundle []byte) (leaf, rest []byte) {
	block, remainder := pem.Decode(bundle)
	if block == nil {
		return bundle, nil
	}
	return pem.EncodeToMemory(block), remainder
}
