package certs

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

func store(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "certs"))
}

func uploaded(t *testing.T, id string, hosts ...string) model.Certificate {
	t.Helper()
	certPEM, keyPEM, err := GenerateSelfSigned(hosts)
	if err != nil {
		t.Fatal(err)
	}
	return model.Certificate{
		ID: id, Enabled: true, Source: model.SourceUploaded,
		CertPEM: string(certPEM), KeyPEM: string(keyPEM),
	}
}

func TestStoreWritesAndReads(t *testing.T) {
	t.Parallel()
	s := store(t)
	if _, err := s.Read("missing"); !errors.Is(err, ErrNoCertificate) {
		t.Errorf("Read of nothing = %v, want ErrNoCertificate", err)
	}

	var told []string
	s.Subscribe(func(id string) { told = append(told, id) })
	cert, key, err := GenerateSelfSigned([]string{"fw.lan"})
	if err != nil {
		t.Fatal(err)
	}
	st := State{IssuedAt: time.Now().UTC().Truncate(time.Second), Names: []string{"fw.lan"}}
	if err := s.Write("web", Files{Cert: cert, Chain: []byte("# issuer\n"), Key: key, State: st}); err != nil {
		t.Fatal(err)
	}
	if len(told) != 1 || told[0] != "web" {
		t.Errorf("subscribers told %v, want one call for web", told)
	}
	if info, err := os.Stat(filepath.Join(s.Dir("web"), KeyFile)); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %o, want it readable by root alone", info.Mode().Perm())
	}
	f, err := s.Read("web")
	if err != nil {
		t.Fatal(err)
	}
	if string(f.FullChain) != string(cert)+"# issuer\n" {
		t.Errorf("fullchain = %q, want the leaf and its issuer", f.FullChain)
	}
	if !f.State.IssuedAt.Equal(st.IssuedAt) || len(f.State.Names) != 1 {
		t.Errorf("state = %+v, want %+v", f.State, st)
	}

	// State on its own tells nobody: nothing serving the certificate
	// cares when it was last attempted.
	told = nil
	st.LastError = "the CA said no"
	if err := s.WriteState("web", st); err != nil {
		t.Fatal(err)
	}
	if len(told) != 0 {
		t.Errorf("subscribers told %v about a state write", told)
	}
	if f, err := s.Read("web"); err != nil || f.State.LastError == "" {
		t.Errorf("Read = %+v, %v, want the error recorded", f, err)
	}
}

func TestStoreApplyFollowsTheConfiguration(t *testing.T) {
	t.Parallel()
	s := store(t)
	var told []string
	s.Subscribe(func(id string) { told = append(told, id) })

	cfg := &model.Config{Certificates: []model.Certificate{
		uploaded(t, "mail", "mail.example.test"),
		{ID: "web", Enabled: true, Source: model.SourceACME, Names: []string{"example.test"}},
	}}
	changed, err := s.Apply(cfg)
	if err != nil || !changed {
		t.Fatalf("Apply = %v, %v", changed, err)
	}
	f, err := s.Read("mail")
	if err != nil {
		t.Fatal(err)
	}
	if f.State.NotAfter.IsZero() || len(f.State.Names) != 1 {
		t.Errorf("state = %+v, want it described from the certificate", f.State)
	}
	// An ACME certificate is issued, not materialised.
	if _, err := s.Read("web"); !errors.Is(err, ErrNoCertificate) {
		t.Errorf("Read(web) = %v, want nothing on disk yet", err)
	}

	// A tick that changed nothing notifies nobody.
	told = nil
	if changed, err := s.Apply(cfg); err != nil || changed {
		t.Fatalf("second Apply = %v, %v, want no change", changed, err)
	}
	if len(told) != 0 {
		t.Errorf("subscribers told %v when nothing moved", told)
	}

	// Replacing the PEM does.
	cfg.Certificates[0] = uploaded(t, "mail", "smtp.example.test")
	if changed, err := s.Apply(cfg); err != nil || !changed {
		t.Fatalf("Apply after a new upload = %v, %v", changed, err)
	}
	if len(told) != 1 {
		t.Errorf("subscribers told %v, want one call", told)
	}

	// An account's cached registration goes when the account does.
	if err := os.MkdirAll(s.AccountsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(s.AccountsDir(), "le.json")
	if err := os.WriteFile(cached, []byte(`{"uri":"https://ca.test/acct/1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.ACME.Accounts = []model.ACMEAccount{{ID: "le"}}
	if _, err := s.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cached); err != nil {
		t.Errorf("the registration of an account that is still there went: %v", err)
	}

	// And a certificate taken out of the configuration takes its files
	// with it.
	cfg.ACME.Accounts = nil
	cfg.Certificates = nil
	if changed, err := s.Apply(cfg); err != nil || !changed {
		t.Fatalf("Apply after a removal = %v, %v", changed, err)
	}
	if _, err := os.Stat(s.Dir("mail")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory survived the removal: %v", err)
	}
	if _, err := os.Stat(cached); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the registration survived the account: %v", err)
	}
}

// A revert or an expired confirmation window does not re-run the
// appliers, so the watcher is what puts the listener back.
func TestWatcherFollowsTheEffectiveConfiguration(t *testing.T) {
	t.Parallel()
	m := manager(t)
	if _, err := m.EnsureSelfSigned([]string{"fw.lan"}); err != nil {
		t.Fatal(err)
	}
	s := store(t)
	m.Store, m.Selected = s, func() string { return "" }
	s.Subscribe(func(string) { _ = m.Reload() })

	cfg := &model.Config{Certificates: []model.Certificate{uploaded(t, "web", "web.example.test")}}
	cfg.System.Management.Certificate = "web"
	m.Selected = func() string { return cfg.System.Management.Certificate }
	w := &Watcher{Store: s, Manager: m, Source: func() *model.Config { return cfg }}

	w.tick()
	if got := m.Serving(); got != "web" {
		t.Fatalf("serving %q, want web", got)
	}
	pair, err := m.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "web.example.test" {
		t.Errorf("the listener has %v, want the uploaded certificate", leaf.DNSNames)
	}

	// Reverted: back on the built-in pair, and the files are gone.
	cfg.Certificates, cfg.System.Management.Certificate = nil, ""
	w.tick()
	if got := m.Serving(); got != "" {
		t.Errorf("serving %q, want the built-in pair", got)
	}
	if _, err := os.Stat(s.Dir("web")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the directory survived the revert: %v", err)
	}
}

// A selection that has not been issued yet leaves the UI up.
func TestReloadKeepsTheBuiltInWhenNothingIsIssued(t *testing.T) {
	t.Parallel()
	m := manager(t)
	if _, err := m.EnsureSelfSigned([]string{"fw.lan"}); err != nil {
		t.Fatal(err)
	}
	m.Store, m.Selected = store(t), func() string { return "web" }
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload = %v", err)
	}
	if got := m.Serving(); got != "" {
		t.Errorf("serving %q, want the built-in pair", got)
	}
	if _, err := m.GetCertificate(nil); err != nil {
		t.Errorf("nothing is being served: %v", err)
	}
}

func TestWatcherRunStopsWithTheContext(t *testing.T) {
	t.Parallel()
	s := store(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		(&Watcher{Store: s, Source: func() *model.Config { return nil }, Interval: time.Minute}).Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop")
	}
}

// An id that could never name a certificate never becomes a path: the
// API hands the store whatever was in the URL.
func TestStoreRefusesAnIDThatIsNotOne(t *testing.T) {
	t.Parallel()
	s := store(t)
	cert, key, err := GenerateSelfSigned([]string{"fw.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(s.Root), "tls"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{CertFile, KeyFile} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(s.Root), "tls", name), cert, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"../tls", "..", ".", "Web", "a/b", "", "-x"} {
		if _, err := s.Read(id); !errors.Is(err, ErrNoCertificate) {
			t.Errorf("Read(%q) = %v, want ErrNoCertificate", id, err)
		}
		if _, err := s.ReadState(id); !errors.Is(err, ErrNoCertificate) {
			t.Errorf("ReadState(%q) = %v, want ErrNoCertificate", id, err)
		}
		if err := s.Write(id, Files{Cert: cert, Key: key}); err == nil {
			t.Errorf("Write(%q) succeeded", id)
		}
		if err := s.WriteState(id, State{}); err == nil {
			t.Errorf("WriteState(%q) succeeded", id)
		}
		if err := s.Remove(id); err == nil {
			t.Errorf("Remove(%q) succeeded", id)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Root), "tls", CertFile)); err != nil {
		t.Errorf("the neighbouring directory was touched: %v", err)
	}
}
