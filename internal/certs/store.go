package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/atomicfile"
	"github.com/rforced/ostiole/internal/model"
)

// Store keeps every certificate's files under one directory, a
// subdirectory per certificate, and tells subscribers when one changes.
// A service that needs a certificate reads Dir(id) and subscribes.
type Store struct {
	Root string // <config-dir>/certs

	mu   sync.Mutex
	subs []func(id string)
}

// File names inside a certificate's directory.
const (
	CertFile      = "cert.pem"
	ChainFile     = "chain.pem"
	FullChainFile = "fullchain.pem"
	KeyFile       = "key.pem"
	stateFile     = "state.json"
)

// Files are one certificate as it is on disk.
type Files struct {
	Cert, Chain, FullChain, Key []byte
	State                       State
}

// State is what issuance left behind: state.json beside the PEM files.
type State struct {
	IssuedAt    time.Time  `json:"issuedAt"`
	NotAfter    time.Time  `json:"notAfter"`
	Names       []string   `json:"names"`
	CertURL     string     `json:"certUrl,omitempty"`
	RenewAfter  *time.Time `json:"renewAfter,omitempty"`
	CheckAfter  *time.Time `json:"checkAfter,omitempty"`
	LastAttempt *time.Time `json:"lastAttempt,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
}

// NewStore returns a store rooted at dir.
func NewStore(dir string) *Store { return &Store{Root: dir} }

// Dir is where one certificate's files live. A service that reads them
// needs nothing but the id, which has to be one the configuration could
// hold: everything else here refuses any other, so a path never forms
// from what a request said.
func (s *Store) Dir(id string) string { return filepath.Join(s.Root, id) }

// errBadID refuses an id that could never name a certificate.
func errBadID(id string) error { return fmt.Errorf("%q is not a certificate id", id) }

// Read returns what is on disk, or ErrNoCertificate when nothing is.
func (s *Store) Read(id string) (*Files, error) {
	if !model.ValidCertificateID(id) {
		return nil, ErrNoCertificate
	}
	dir := s.Dir(id)
	cert, err := os.ReadFile(filepath.Join(dir, CertFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoCertificate
	}
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(filepath.Join(dir, KeyFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoCertificate
	}
	if err != nil {
		return nil, err
	}
	f := &Files{Cert: cert, Key: key}
	// The chain is absent for a certificate a CA issued without one, and
	// for a self-signed upload.
	if chain, err := os.ReadFile(filepath.Join(dir, ChainFile)); err == nil {
		f.Chain = chain
	}
	if full, err := os.ReadFile(filepath.Join(dir, FullChainFile)); err == nil {
		f.FullChain = full
	} else {
		f.FullChain = cert
	}
	if raw, err := os.ReadFile(filepath.Join(dir, stateFile)); err == nil {
		_ = json.Unmarshal(raw, &f.State)
	}
	return f, nil
}

// Write replaces a certificate's files and notifies whoever is using it.
// The files are written beside their targets and renamed, so a reader
// never sees a certificate that does not match its key.
func (s *Store) Write(id string, f Files) error {
	if !model.ValidCertificateID(id) {
		return errBadID(id)
	}
	dir := s.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	full := f.FullChain
	if len(full) == 0 {
		full = append(append([]byte{}, f.Cert...), f.Chain...)
	}
	for _, w := range []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{
		{CertFile, f.Cert, 0o644},
		{ChainFile, f.Chain, 0o644},
		{FullChainFile, full, 0o644},
		{KeyFile, f.Key, 0o600},
	} {
		if err := atomicfile.Write(filepath.Join(dir, w.name), w.content, w.mode); err != nil {
			return err
		}
	}
	if err := s.WriteState(id, f.State); err != nil {
		return err
	}
	s.notify(id)
	return nil
}

// ReadState returns what the last pass recorded. A certificate the CA
// refused has a state and no files, which is how the next pass knows to
// leave it alone for a while.
func (s *Store) ReadState(id string) (State, error) {
	var st State
	if !model.ValidCertificateID(id) {
		return st, ErrNoCertificate
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir(id), stateFile))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(raw, &st)
}

// WriteState records what the last pass learned without touching the PEM
// files, and without telling anybody: nothing that serves the certificate
// cares when it was last attempted.
func (s *Store) WriteState(id string, st State) error {
	if !model.ValidCertificateID(id) {
		return errBadID(id)
	}
	dir := s.Dir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, stateFile), append(raw, '\n'), 0o600)
}

// Remove deletes a certificate's directory.
func (s *Store) Remove(id string) error {
	if !model.ValidCertificateID(id) {
		return errBadID(id)
	}
	if err := os.RemoveAll(s.Dir(id)); err != nil {
		return err
	}
	s.notify(id)
	return nil
}

// List names the certificates that have a directory, whether the
// configuration still mentions them or not.
func (s *Store) List() []string {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != accountsDir {
			out = append(out, e.Name())
		}
	}
	return out
}

// Subscribe calls fn whenever a certificate's files change. It is called
// from whichever goroutine wrote them, so it should not block.
func (s *Store) Subscribe(fn func(id string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, fn)
}

func (s *Store) notify(id string) {
	s.mu.Lock()
	subs := slices.Clone(s.subs)
	s.mu.Unlock()
	for _, fn := range subs {
		fn(id)
	}
}

// accountsDir holds one file per ACME account, the registration URI the
// CA gave back. It is state, not a certificate, so List skips it.
const accountsDir = "accounts"

// AccountsDir is where the ACME client caches its registrations.
func (s *Store) AccountsDir() string { return filepath.Join(s.Root, accountsDir) }

// pruneAccounts deletes the cached registration of an account the
// configuration no longer has, so a deleted account leaves nothing
// behind pointing at a CA.
func (s *Store) pruneAccounts(cfg *model.Config) {
	entries, err := os.ReadDir(s.AccountsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if _, held := cfg.ACMEAccount(id); !held {
			_ = os.Remove(filepath.Join(s.AccountsDir(), e.Name()))
		}
	}
}

// Apply materialises uploaded certificates from cfg, removes the
// directories of certificates no longer in it and the registrations of
// accounts no longer in it. It reports whether anything changed.
func (s *Store) Apply(cfg *model.Config) (bool, error) {
	if cfg == nil {
		return false, nil
	}
	changed := false
	for _, cert := range cfg.Certificates {
		if cert.Source != model.SourceUploaded {
			continue
		}
		// Writing on every tick would notify everything that serves the
		// certificate every five seconds.
		if have, err := s.Read(cert.ID); err == nil && string(have.Cert) == cert.CertPEM && string(have.Key) == cert.KeyPEM {
			continue
		}
		f := Files{
			Cert:      []byte(cert.CertPEM),
			FullChain: []byte(cert.CertPEM),
			Key:       []byte(cert.KeyPEM),
			State:     State{IssuedAt: time.Now().UTC(), Names: []string{}},
		}
		if info, err := Describe(f.Cert); err == nil {
			f.State.NotAfter, f.State.Names = info.NotAfter, info.Names
		}
		if err := s.Write(cert.ID, f); err != nil {
			return changed, err
		}
		changed = true
	}
	for _, id := range s.List() {
		if _, ok := cfg.Certificate(id); ok {
			continue
		}
		// A certificate taken out of the configuration takes its key with
		// it. A revert that brings it back finds nothing and the next pass
		// issues it again.
		if err := s.Remove(id); err != nil {
			return changed, err
		}
		changed = true
	}
	s.pruneAccounts(cfg)
	return changed, nil
}

// Pair is an issued certificate taken apart, for whatever wants the
// objects rather than the PEM: a PKCS#12 download, mostly.
type Pair struct {
	Leaf  *x509.Certificate
	Chain []*x509.Certificate
	Key   crypto.PrivateKey
}

// KeyPair parses a certificate's files.
func KeyPair(f *Files) (*Pair, error) {
	chain, err := parseChain(f.FullChain)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(f.Key)
	if block == nil {
		return nil, fmt.Errorf("private key: %w", ErrNotPEM)
	}
	key, err := parsePrivateKey(block)
	if err != nil {
		return nil, err
	}
	return &Pair{Leaf: chain[0], Chain: chain[1:], Key: key}, nil
}

// GenerateAccountKey returns a fresh P-256 key in PEM, which is what an
// ACME account is identified by.
func GenerateAccountKey() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func parsePrivateKey(block *pem.Block) (crypto.PrivateKey, error) {
	switch block.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	return nil, fmt.Errorf("%q is not a private key", block.Type)
}
