package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/model"
)

// Every kind the UI offers has to be buildable, and nothing may be built
// in that the UI cannot describe.
func TestProviderKindsMatchWhatIsCompiledIn(t *testing.T) {
	t.Parallel()
	if err := checkProviders(); err != nil {
		t.Error(err)
	}
}

// fakeIssuer answers without a CA.
type fakeIssuer struct {
	mu       sync.Mutex
	requests []Request
	lifetime time.Duration
	// age is how far through its life the issued certificate already is.
	age      time.Duration
	window   *Window
	windowFn func() (*Window, error)
	err      error
	block    chan struct{}
}

func (f *fakeIssuer) Issue(_ context.Context, req Request) (*Issued, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	block := f.block
	err := f.err
	life, age := f.lifetime, f.age
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return nil, err
	}
	if life == 0 {
		life = 90 * 24 * time.Hour
	}
	certPEM, keyPEM := leafFor(req.Names, life, age)
	return &Issued{Cert: certPEM, FullChain: certPEM, Key: keyPEM, CertURL: "https://ca.test/cert/1"}, nil
}

func (f *fakeIssuer) RenewalInfo(context.Context, model.ACMEAccount, []byte) (*Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.windowFn != nil {
		return f.windowFn()
	}
	if f.window == nil {
		return nil, ErrNoARI
	}
	return f.window, nil
}

func (f *fakeIssuer) seen() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request{}, f.requests...)
}

// leafFor makes a certificate for names that lives for life and is
// already age old, so a test can put one in its last third.
func leafFor(names []string, life, age time.Duration) (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    now.Add(-age),
		NotAfter:     now.Add(life - age),
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func renewer(t *testing.T, cfg *model.Config, issuer Issuer) (*Renewer, *certs.Store) {
	t.Helper()
	store := certs.NewStore(filepath.Join(t.TempDir(), "certs"))
	return &Renewer{
		Store:     store,
		Issuer:    issuer,
		Config:    func() *model.Config { return cfg },
		Addresses: func(string) []string { return []string{"198.51.100.4", "192.168.1.1"} },
	}, store
}

func acmeConfig() *model.Config {
	return &model.Config{
		ACME: model.ACME{Accounts: []model.ACMEAccount{{ID: "le", Directory: "https://ca.test/dir"}}},
		Certificates: []model.Certificate{{
			ID: "router", Enabled: true, Source: model.SourceACME,
			Names: []string{"router.example.test"}, Account: "le", Challenge: model.ChallengeHTTP,
			InterfaceAddresses: []string{"wan0"},
		}},
	}
}

func TestPassIssuesWhatIsMissing(t *testing.T) {
	t.Parallel()
	cfg := acmeConfig()
	issuer := &fakeIssuer{}
	r, store := renewer(t, cfg, issuer)

	summary, err := r.Pass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary != "1 checked, 1 renewed, 0 failed" {
		t.Errorf("summary = %q", summary)
	}
	seen := issuer.seen()
	if len(seen) != 1 {
		t.Fatalf("asked for %d certificates, want 1", len(seen))
	}
	// The private address the interface also carries is not something a
	// CA would issue for.
	want := []string{"198.51.100.4", "router.example.test"}
	if got := seen[0].Names; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("asked for %v, want %v", got, want)
	}
	if _, err := store.Read("router"); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}

	// A second pass changes nothing: the names match and the window has
	// not started.
	issuer.window = &Window{Start: time.Now().Add(24 * time.Hour), End: time.Now().Add(48 * time.Hour)}
	if summary, err := r.Pass(context.Background()); err != nil || summary != "1 checked, 0 renewed, 0 failed" {
		t.Errorf("second pass = %q, %v", summary, err)
	}
}

func TestPassRenewsWhenTheAddressChanges(t *testing.T) {
	t.Parallel()
	cfg := acmeConfig()
	issuer := &fakeIssuer{}
	r, _ := renewer(t, cfg, issuer)
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.Addresses = func(string) []string { return []string{"203.0.113.9"} }
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen := issuer.seen()
	if len(seen) != 2 {
		t.Fatalf("asked %d times, want a re-issue on the new address", len(seen))
	}
	if got := seen[1].Names; got[0] != "203.0.113.9" {
		t.Errorf("asked for %v, want the new address", got)
	}
}

// panickingIssuer panics the way lego might on an answer it did not
// expect.
type panickingIssuer struct{ fakeIssuer }

func (p *panickingIssuer) Issue(context.Context, Request) (*Issued, error) {
	panic("unexpected answer from the CA")
}

// A panic in an order fails that certificate like any failure, recorded
// where the page shows it, and gives its claim back: a claim kept would
// skip the certificate on every pass until it expired.
func TestAPanickingOrderFailsAndLetsGo(t *testing.T) {
	t.Parallel()
	r, store := renewer(t, acmeConfig(), &panickingIssuer{})
	summary, err := r.Pass(context.Background())
	if err == nil || summary != "1 checked, 0 renewed, 1 failed" {
		t.Fatalf("Pass = %q, %v", summary, err)
	}
	if r.isRunning("router") {
		t.Error("the certificate is still claimed")
	}
	if state, err := store.ReadState("router"); err != nil || state.LastError == "" {
		t.Errorf("the failure was not recorded: %+v, %v", state, err)
	}
	// Checked again rather than skipped as busy, and held back only by the
	// wait after any failure.
	if summary, err := r.Pass(context.Background()); err != nil || summary != "1 checked, 0 renewed, 0 failed" {
		t.Errorf("the next pass = %q, %v", summary, err)
	}
}

func TestPassBacksOffAfterAFailure(t *testing.T) {
	t.Parallel()
	cfg := acmeConfig()
	issuer := &fakeIssuer{err: errors.New("the CA said no")}
	r, store := renewer(t, cfg, issuer)

	summary, err := r.Pass(context.Background())
	if err == nil {
		t.Fatal("a refusal was not reported")
	}
	if summary != "1 checked, 0 renewed, 1 failed" {
		t.Errorf("summary = %q", summary)
	}
	f, rerr := store.Read("router")
	if !errors.Is(rerr, certs.ErrNoCertificate) {
		t.Fatalf("Read = %+v, %v, want nothing issued", f, rerr)
	}
	// The refusal is on the row even though there are no files, which is
	// the only thing that explains why the certificate is missing.
	if st := r.Status(cfg)[0]; st.Issued || st.LastError == "" || st.LastAttempt == nil {
		t.Errorf("status = %+v, want the failure reported", st)
	}

	// The next hour does not spend another validation.
	issuer.err = nil
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(issuer.seen()) != 1 {
		t.Errorf("asked %d times, want the failure to hold it back", len(issuer.seen()))
	}

	// Six hours later it tries again.
	state := certs.State{}
	old := time.Now().UTC().Add(-failureBackoff - time.Minute)
	state.LastAttempt, state.LastError = &old, "the CA said no"
	if err := store.WriteState("router", state); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(issuer.seen()) != 2 {
		t.Errorf("asked %d times, want it to try again", len(issuer.seen()))
	}
}

func TestPassFollowsTheRenewalWindow(t *testing.T) {
	t.Parallel()
	cfg := acmeConfig()
	issuer := &fakeIssuer{}
	r, store := renewer(t, cfg, issuer)
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A window that has not started, with a Retry-After: the next pass
	// does not even ask.
	asked := 0
	issuer.windowFn = func() (*Window, error) {
		asked++
		return &Window{
			Start:      time.Now().Add(2 * time.Hour),
			End:        time.Now().Add(4 * time.Hour),
			RetryAfter: time.Now().Add(time.Hour),
		}, nil
	}
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Errorf("asked the CA %d times, want the Retry-After respected", asked)
	}
	f, err := store.Read("router")
	if err != nil || f.State.RenewAfter == nil || f.State.CheckAfter == nil {
		t.Fatalf("state = %+v, %v, want the window recorded", f, err)
	}

	// Once the window has started it renews.
	state := f.State
	started := time.Now().UTC().Add(-time.Minute)
	past := time.Now().UTC().Add(-time.Minute)
	state.RenewAfter, state.CheckAfter = &started, &past
	issuer.windowFn = func() (*Window, error) { return &Window{Start: started, End: started.Add(time.Hour)}, nil }
	if err := store.WriteState("router", state); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(issuer.seen()) != 2 {
		t.Errorf("asked %d times, want a renewal inside the window", len(issuer.seen()))
	}
}

// Without ARI the certificate's own lifetime decides: the last third.
func TestPassFallsBackToTheLastThird(t *testing.T) {
	t.Parallel()
	cfg := acmeConfig()
	issuer := &fakeIssuer{lifetime: 4 * time.Hour, age: 3 * time.Hour}
	r, store := renewer(t, cfg, issuer)
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(issuer.seen()) != 2 {
		t.Errorf("asked %d times, want a renewal in the last third", len(issuer.seen()))
	}

	// A young one is left alone.
	issuer.lifetime, issuer.age = 90*24*time.Hour, 0
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := len(issuer.seen())
	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(issuer.seen()) != before {
		t.Errorf("a fresh certificate was renewed anyway")
	}
	if _, err := store.Read("router"); err != nil {
		t.Fatal(err)
	}
}

func TestIssueNowLocksPerCertificate(t *testing.T) {
	t.Parallel()
	cfg := acmeConfig()
	issuer := &fakeIssuer{block: make(chan struct{})}
	r, _ := renewer(t, cfg, issuer)

	if err := r.IssueNow(context.Background(), "router"); err != nil {
		t.Fatal(err)
	}
	// The order is still in flight.
	for range 100 {
		if len(issuer.seen()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.IssueNow(context.Background(), "router"); !errors.Is(err, ErrIssuing) {
		t.Errorf("second IssueNow = %v, want ErrIssuing", err)
	}
	if st := r.Status(cfg); !st[0].Running {
		t.Error("the status does not say it is running")
	}
	// And a pass leaves it alone rather than ordering a second one.
	if summary, err := r.Pass(context.Background()); err != nil || summary != "0 checked, 0 renewed, 0 failed" {
		t.Errorf("pass during an order = %q, %v", summary, err)
	}
	close(issuer.block)

	if err := r.IssueNow(context.Background(), "nothing"); !errors.Is(err, ErrUnknownCertificate) {
		t.Errorf("IssueNow of an unknown id = %v", err)
	}
}

func TestStatusMergesTheConfigurationAndTheFiles(t *testing.T) {
	t.Parallel()
	cfg := acmeConfig()
	cfg.System.Management.Certificate = "router"
	cfg.Certificates = append(cfg.Certificates, model.Certificate{
		ID: "mail", Source: model.SourceUploaded, Enabled: true,
	})
	issuer := &fakeIssuer{}
	r, _ := renewer(t, cfg, issuer)

	st := r.Status(cfg)
	if len(st) != 2 {
		t.Fatalf("%d statuses, want one per certificate", len(st))
	}
	if st[0].Issued || !st[0].Serving {
		t.Errorf("router = %+v, want it unissued and the UI's", st[0])
	}
	if got := st[0].Wanted; len(got) != 2 {
		t.Errorf("wanted = %v, want the name and the public address", got)
	}

	if _, err := r.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = r.Status(cfg)
	if !st[0].Issued || st[0].NotAfter.IsZero() || st[0].Fingerprint == "" {
		t.Errorf("router = %+v, want it described from its files", st[0])
	}
	if st[0].Issuer != "test" {
		t.Errorf("issuer = %q", st[0].Issuer)
	}
}

func TestPublicAddresses(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"198.51.100.4":   true,
		"2001:db8::1":    true,
		"192.168.1.1":    false,
		"10.0.0.1":       false,
		"100.64.0.1":     false,
		"127.0.0.1":      false,
		"169.254.1.1":    false,
		"fe80::1":        false,
		"fd00::1":        false,
		"::1":            false,
		"not-an-address": false,
	} {
		if got := Public(addr); got != want {
			t.Errorf("Public(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestIssueNowRefusesAnUploadedCertificate(t *testing.T) {
	t.Parallel()
	cfg := acmeConfig()
	cfg.Certificates = append(cfg.Certificates, model.Certificate{ID: "pasted", Enabled: true, Source: model.SourceUploaded})
	r, _ := renewer(t, cfg, &fakeIssuer{})
	if err := r.IssueNow(context.Background(), "pasted"); !errors.Is(err, ErrUploaded) {
		t.Errorf("IssueNow(uploaded) = %v, want ErrUploaded", err)
	}
	if err := r.IssueNow(context.Background(), "nope"); !errors.Is(err, ErrUnknownCertificate) {
		t.Errorf("IssueNow(unknown) = %v, want ErrUnknownCertificate", err)
	}
}

// Names are trimmed before they reach an order, as they are before they
// are validated.
func TestWantedTrimsNames(t *testing.T) {
	t.Parallel()
	r, _ := renewer(t, acmeConfig(), &fakeIssuer{})
	got := r.wanted(model.Certificate{Names: []string{" a.example.test", "b.example.test ", " "}})
	if len(got) != 2 || got[0] != "a.example.test" || got[1] != "b.example.test" {
		t.Errorf("wanted = %q", got)
	}
}

// ed25519PKCS8 is a key no CA account can use.
func ed25519PKCS8(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}
