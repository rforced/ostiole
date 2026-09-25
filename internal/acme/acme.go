// Package acme gets certificates from a CA. It is the only place lego is
// imported: everything else in Ostiole talks to the Issuer interface and
// the certificate store.
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
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/acme/api"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/registration"

	"github.com/rforced/ostiole/internal/atomicfile"
	"github.com/rforced/ostiole/internal/model"
)

// ChallengePort is where the http-01 solver listens on loopback when
// something else owns port 80.
const ChallengePort = 8402

// ErrNoARI says the CA does not publish renewal windows, so the renewer
// falls back to the certificate's own lifetime.
var ErrNoARI = errors.New("this CA does not publish renewal information")

// errNoResolver refuses a dns-01 order on a router with nothing to ask
// whether the record has landed. lego would ask Google instead.
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

// Client talks to a CA with lego.
type Client struct {
	// UserAgent is what the CA sees, the same string the feeds send.
	UserAgent string
	// Config is the configuration in force, read for the resolvers a
	// dns-01 pre-check asks and for nothing else.
	Config func() *model.Config
	// AccountsDir caches the registration each account key resolved to,
	// so an hourly pass does not ask the CA twice.
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

// NewClient returns a client that identifies itself as this Ostiole.
func NewClient(version string, cfg func() *model.Config, accountsDir string) *Client {
	return &Client{
		UserAgent:   "ostiole/" + version,
		Config:      cfg,
		AccountsDir: accountsDir,
		Log:         slog.Default(),
	}
}

// legoLog sends lego's lines to slog, so a failed order is in the journal
// with everything else rather than on stderr. The logger is looked up on
// every line, because the default is replaced once the log level is known.
type legoLog struct{}

func (legoLog) Fatal(args ...any)                 { slog.Error(strings.TrimSpace(fmt.Sprintln(args...))) }
func (l legoLog) Fatalln(args ...any)             { l.Fatal(args...) }
func (legoLog) Fatalf(format string, args ...any) { slog.Error(line(format, args)) }
func (legoLog) Print(args ...any)                 { emit(strings.TrimSpace(fmt.Sprintln(args...))) }
func (l legoLog) Println(args ...any)             { l.Print(args...) }
func (legoLog) Printf(format string, args ...any) { emit(line(format, args)) }

// emit keeps lego's own level: it writes "[WARN] " in front of a line
// that deserves it.
func emit(s string) {
	if rest, ok := strings.CutPrefix(s, "[WARN] "); ok {
		slog.Warn(rest)
		return
	}
	slog.Info(s)
}

func line(format string, args []any) string {
	return strings.TrimSpace(strings.TrimPrefix(fmt.Sprintf(format, args...), "[INFO] "))
}

func init() { legolog.Logger = legoLog{} }

// user is one account as lego wants it.
type user struct {
	email string
	key   crypto.PrivateKey
	reg   *registration.Resource
}

func (u *user) GetEmail() string                        { return u.email }
func (u *user) GetRegistration() *registration.Resource { return u.reg }
func (u *user) GetPrivateKey() crypto.PrivateKey        { return u.key }

// client builds a lego client for one account and makes sure the account
// exists at the CA.
func (c *Client) client(a model.ACMEAccount, wantKey string, noCommonName bool) (*lego.Client, error) {
	key, err := parseKey(a.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("account key: %w", err)
	}
	u := &user{email: a.Email, key: key, reg: c.cached(a)}
	cfg := lego.NewConfig(u)
	cfg.CADirURL = a.Directory
	cfg.UserAgent = c.UserAgent
	cfg.Certificate.KeyType = keyType(wantKey)
	cfg.Certificate.DisableCommonName = noCommonName
	if a.CACert != "" {
		httpClient, err := clientTrusting(a.CACert)
		if err != nil {
			return nil, err
		}
		cfg.HTTPClient = httpClient
	}
	cl, err := lego.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	if u.reg != nil {
		return cl, nil
	}
	reg, err := c.register(cl, a)
	if err != nil {
		return nil, err
	}
	u.reg = reg
	c.cache(a, reg)
	// The client signs with the account URL it was built with, so it has
	// to be rebuilt once the account is known.
	return lego.NewClient(cfg)
}

// register finds the account at the CA, or creates it. Saving an account
// is what agrees to the CA's terms; the dialog says so.
func (c *Client) register(cl *lego.Client, a model.ACMEAccount) (*registration.Resource, error) {
	reg, err := cl.Registration.ResolveAccountByKey()
	if err == nil {
		return reg, nil
	}
	// Only a CA saying the account is not there is a reason to make one;
	// any other refusal is the answer.
	var problem *acme.ProblemDetails
	if !errors.As(err, &problem) || !strings.HasSuffix(problem.Type, ":accountDoesNotExist") {
		return nil, err
	}
	if a.EABKeyID != "" {
		return cl.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
			TermsOfServiceAgreed: true,
			Kid:                  a.EABKeyID,
			HmacEncoded:          a.EABHMAC,
		})
	}
	return cl.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
}

// Issue orders one certificate.
func (c *Client) Issue(ctx context.Context, req Request) (*Issued, error) {
	if req.Challenge == model.ChallengeDNS && len(c.resolvers()) == 0 {
		return nil, errNoResolver
	}
	// A CA refuses a CSR with an address in the common name, which is
	// where lego puts the first name by default.
	cl, err := c.client(req.Account, req.KeyType, slices.ContainsFunc(req.Names, isAddress))
	if err != nil {
		return nil, err
	}
	switch req.Challenge {
	case model.ChallengeDNS:
		if req.Provider == nil {
			return nil, errors.New("no DNS provider for a dns-01 challenge")
		}
		build, ok := Providers[req.Provider.Kind]
		if !ok {
			return nil, fmt.Errorf("this build cannot write to %s", req.Provider.Kind)
		}
		p, err := build(*req.Provider)
		if err != nil {
			return nil, err
		}
		if err := cl.Challenge.SetDNS01Provider(p, c.dnsOptions()...); err != nil {
			return nil, err
		}
	case model.ChallengeHTTP:
		if err := cl.Challenge.SetHTTP01Provider(c.http01()); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown challenge %q", req.Challenge)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := cl.Certificate.Obtain(certificate.ObtainRequest{
		Domains: req.Names,
		Bundle:  true,
		Profile: req.Profile,
	})
	if err != nil {
		return nil, err
	}
	leaf, chain := splitLeaf(res.Certificate)
	if len(res.IssuerCertificate) > 0 {
		chain = res.IssuerCertificate
	}
	return &Issued{
		Cert:      leaf,
		Chain:     chain,
		FullChain: res.Certificate,
		Key:       res.PrivateKey,
		CertURL:   res.CertURL,
	}, nil
}

// RenewalInfo asks the CA when it would like this certificate renewed.
func (c *Client) RenewalInfo(ctx context.Context, account model.ACMEAccount, leaf []byte) (*Window, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cert, err := parseLeaf(leaf)
	if err != nil {
		return nil, err
	}
	cl, err := c.client(account, "", false)
	if err != nil {
		return nil, err
	}
	info, err := cl.Certificate.GetRenewalInfo(certificate.RenewalInfoRequest{Cert: cert})
	if err != nil {
		if errors.Is(err, api.ErrNoARI) {
			return nil, ErrNoARI
		}
		return nil, err
	}
	w := &Window{Start: info.SuggestedWindow.Start.UTC(), End: info.SuggestedWindow.End.UTC()}
	if info.RetryAfter > 0 {
		w.RetryAfter = time.Now().UTC().Add(info.RetryAfter)
	}
	return w, nil
}

// dnsOptions point the pre-check at this router's own resolvers. lego
// falls back to Google Public DNS when resolv.conf is empty, which is a
// third party this router never chose to talk to; Issue refuses a dns-01
// order before it gets that far.
func (c *Client) dnsOptions() []dns01.ChallengeOption {
	opts := []dns01.ChallengeOption{dns01.AddRecursiveNameservers(c.resolvers())}
	if c.relaxPropagation {
		opts = append(opts, dns01.DisableAuthoritativeNssPropagationRequirement())
	}
	return opts
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

func (c *Client) resolvers() []string {
	cfg := c.Config()
	if cfg == nil {
		return c.resolvConf()
	}
	if cfg.Services.DNS.Enabled {
		return []string{"127.0.0.1:53"}
	}
	var out []string
	for _, s := range cfg.System.DNSServers {
		out = append(out, withPort(s))
	}
	if len(out) > 0 {
		return out
	}
	return c.resolvConf()
}

func (c *Client) resolvConf() []string {
	path := c.resolvConfPath
	if path == "" {
		path = "/etc/resolv.conf"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			out = append(out, withPort(fields[1]))
		}
	}
	return out
}

func withPort(addr string) string {
	if strings.HasSuffix(addr, "]") || !strings.Contains(addr, ":") {
		return addr + ":53"
	}
	if strings.Contains(addr, "]:") {
		return addr
	}
	// A bare IPv6 address has several colons and no brackets.
	if strings.Count(addr, ":") > 1 {
		return "[" + addr + "]:53"
	}
	return addr
}

// keyType maps what the UI offers onto what lego generates.
func keyType(s string) certcrypto.KeyType {
	switch s {
	case "ec384":
		return certcrypto.EC384
	case "rsa2048":
		return certcrypto.RSA2048
	case "rsa4096":
		return certcrypto.RSA4096
	}
	return certcrypto.EC256
}

// registration is the account URL a key resolved to, kept so that an
// hourly pass does not ask the CA who it is every time.
type cachedRegistration struct {
	Key string `json:"key"`
	URI string `json:"uri"`
}

func (c *Client) cached(a model.ACMEAccount) *registration.Resource {
	if c.AccountsDir == "" {
		return nil
	}
	raw, err := os.ReadFile(c.accountPath(a.ID))
	if err != nil {
		return nil
	}
	var got cachedRegistration
	if err := json.Unmarshal(raw, &got); err != nil || got.URI == "" {
		return nil
	}
	// A new key is a new account, whatever the file says.
	if got.Key != fingerprint(a.PrivateKey) {
		_ = os.Remove(c.accountPath(a.ID))
		return nil
	}
	return &registration.Resource{URI: got.URI}
}

func (c *Client) cache(a model.ACMEAccount, reg *registration.Resource) {
	if c.AccountsDir == "" || reg == nil || reg.URI == "" {
		return
	}
	raw, err := json.Marshal(cachedRegistration{Key: fingerprint(a.PrivateKey), URI: reg.URI})
	if err != nil {
		return
	}
	if err := os.MkdirAll(c.AccountsDir, 0o700); err != nil {
		return
	}
	_ = atomicfile.Write(c.accountPath(a.ID), raw, 0o600)
}

func (c *Client) accountPath(id string) string {
	return filepath.Join(c.AccountsDir, id+".json")
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

func isAddress(name string) bool {
	_, err := netip.ParseAddr(strings.TrimSpace(name))
	return err == nil
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

// clientTrusting is an HTTP client that trusts a CA browsers do not know,
// for a private or test ACME server.
func clientTrusting(caPEM string) (*http.Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("the CA certificate is not PEM")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: 2 * time.Minute, Transport: transport}, nil
}
