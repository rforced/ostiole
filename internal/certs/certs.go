// Package certs manages the certificate the web UI serves. A router starts
// with a self-signed one so the first connection is at least encrypted;
// this package lets that be replaced with a real one, describes whatever
// is installed, and hands the running server a certificate that can be
// swapped without a restart.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/atomicfile"
)

// SelfSignedYears is how long a generated certificate lasts. Nobody
// renews the certificate on a router they never log into, so it outlives the
// hardware rather than expiring quietly.
const SelfSignedYears = 10

// Errors callers distinguish.
var (
	ErrNoCertificate = errors.New("no certificate is installed")
	ErrNotPEM        = errors.New("this is not a PEM file")
	ErrMismatch      = errors.New("the private key does not match the certificate")
)

// Info describes an installed certificate for the UI.
type Info struct {
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	SelfSign  bool      `json:"selfSigned"`
	Names     []string  `json:"names"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	// Fingerprint is the SHA-256 of the certificate, the value a browser
	// shows when you inspect an exception you added by hand.
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
	// Expired and ExpiresSoon save the UI from doing date arithmetic.
	Expired     bool `json:"expired"`
	ExpiresSoon bool `json:"expiresSoon"`
	// Chain counts the certificates in the file: more than one means an
	// intermediate was supplied, which most real certificates need.
	Chain int `json:"chain"`
}

// Manager owns the certificate files and the certificate the server is
// currently serving.
type Manager struct {
	CertPath string
	KeyPath  string
	// Store holds the certificates this router issued or was given.
	Store *Store
	// Selected names the one the web UI serves; empty, and the built-in
	// self-signed pair is served instead.
	Selected func() string

	mu      sync.RWMutex
	current *tls.Certificate
	serving string
}

// New returns a manager over the given paths.
func New(certPath, keyPath string) *Manager {
	return &Manager{CertPath: certPath, KeyPath: keyPath}
}

// Load reads the built-in pair into memory so GetCertificate can serve
// them.
func (m *Manager) Load() error {
	cert, err := tls.LoadX509KeyPair(m.CertPath, m.KeyPath)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.current, m.serving = &cert, ""
	m.mu.Unlock()
	return nil
}

// Serving is the id of the certificate on the listener, empty for the
// built-in self-signed pair.
func (m *Manager) Serving() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.serving
}

// Reload serves whatever the configuration selected. A selection whose
// files are not there yet leaves the built-in pair up rather than taking
// the UI down, and the store tells us when they arrive.
func (m *Manager) Reload() error {
	want := ""
	if m.Selected != nil {
		want = m.Selected()
	}
	if want != "" && m.Store != nil {
		f, err := m.Store.Read(want)
		switch {
		case err == nil:
			pair, err := tls.X509KeyPair(f.FullChain, f.Key)
			if err != nil {
				return fmt.Errorf("certificate %s: %w", want, err)
			}
			m.mu.Lock()
			m.current, m.serving = &pair, want
			m.mu.Unlock()
			return nil
		case !errors.Is(err, ErrNoCertificate):
			return err
		}
	}
	m.mu.RLock()
	builtIn := m.current != nil && m.serving == ""
	m.mu.RUnlock()
	if builtIn {
		return nil
	}
	return m.Load()
}

// GetCertificate feeds tls.Config, so a replacement takes effect on the
// next connection instead of the next restart.
func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return nil, ErrNoCertificate
	}
	return m.current, nil
}

// Info describes what is installed.
func (m *Manager) Info() (*Info, error) {
	raw, err := os.ReadFile(m.CertPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoCertificate
	}
	if err != nil {
		return nil, err
	}
	return Describe(raw)
}

// Describe parses a PEM chain and summarises the leaf.
func Describe(pemBytes []byte) (*Info, error) {
	chain, err := parseChain(pemBytes)
	if err != nil {
		return nil, err
	}
	leaf := chain[0]
	sum := sha256.Sum256(leaf.Raw)
	now := time.Now()
	info := &Info{
		Subject:     nameOf(leaf.Subject),
		Issuer:      nameOf(leaf.Issuer),
		SelfSign:    leaf.Subject.String() == leaf.Issuer.String(),
		Names:       namesOf(leaf),
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
		Fingerprint: colonHex(sum[:]),
		Algorithm:   leaf.PublicKeyAlgorithm.String() + " / " + leaf.SignatureAlgorithm.String(),
		Expired:     now.After(leaf.NotAfter) || now.Before(leaf.NotBefore),
		// Soon is the last third of the lifetime rather than a fixed
		// number of days: a short-lived certificate lives six days, so
		// thirty would be "soon" from the moment it was issued.
		ExpiresSoon: now.After(leaf.NotAfter.Add(-leaf.NotAfter.Sub(leaf.NotBefore) / 3)),
		Chain:       len(chain),
	}
	return info, nil
}

// Install validates a certificate and key and writes them, then makes the
// server serve them. The files are replaced together or not at all: a
// certificate without its key would take the UI down.
func (m *Manager) Install(certPEM, keyPEM []byte) (*Info, error) {
	info, err := Validate(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(m.CertPath), 0o700); err != nil {
		return nil, err
	}
	// Write both before either goes in, so a failed write cannot leave a
	// certificate that does not match its key.
	cert, err := atomicfile.Create(m.CertPath, 0o644)
	if err != nil {
		return nil, err
	}
	defer cert.Close()
	key, err := atomicfile.Create(m.KeyPath, 0o600)
	if err != nil {
		return nil, err
	}
	defer key.Close()
	if _, err := cert.Write(certPEM); err != nil {
		return nil, err
	}
	if _, err := key.Write(keyPEM); err != nil {
		return nil, err
	}
	if err := cert.Commit(); err != nil {
		return nil, err
	}
	if err := key.Commit(); err != nil {
		return nil, err
	}
	if err := m.Load(); err != nil {
		return nil, err
	}
	return info, nil
}

// Validate checks that a certificate and key belong together and can be
// served, and describes the result.
func Validate(certPEM, keyPEM []byte) (*Info, error) {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		// tls reports a mismatch and a malformed file with the same kind
		// of error, so say which it is.
		if _, perr := parseChain(certPEM); perr != nil {
			return nil, perr
		}
		if !hasPEMBlock(keyPEM) {
			return nil, fmt.Errorf("private key: %w", ErrNotPEM)
		}
		return nil, fmt.Errorf("%w: %w", ErrMismatch, err)
	}
	return Describe(certPEM)
}

// SelfSigned generates a certificate for the given names and installs it.
// It is what a router starts with, and what the UI offers when the names it
// is reached by have changed.
func (m *Manager) SelfSigned(hosts []string) (*Info, error) {
	certPEM, keyPEM, err := GenerateSelfSigned(hosts)
	if err != nil {
		return nil, err
	}
	info, err := m.Install(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	// A router that is serving an issued certificate keeps serving it:
	// regenerating the built-in pair is preparation, not a switch.
	return info, m.Reload()
}

// EnsureSelfSigned generates a certificate only when one is missing, and
// reports whether it made one.
func (m *Manager) EnsureSelfSigned(hosts []string) (bool, error) {
	_, certErr := os.Stat(m.CertPath)
	_, keyErr := os.Stat(m.KeyPath)
	if certErr == nil && keyErr == nil {
		return false, m.Load()
	}
	if certErr != nil && !errors.Is(certErr, os.ErrNotExist) {
		return false, certErr
	}
	if keyErr != nil && !errors.Is(keyErr, os.ErrNotExist) {
		return false, keyErr
	}
	if _, err := m.SelfSigned(hosts); err != nil {
		return false, err
	}
	return true, nil
}

// GenerateSelfSigned returns a PEM certificate and key covering hosts.
func GenerateSelfSigned(hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ostiole", Organization: []string{"Ostiole firewall"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(SelfSignedYears, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func parseChain(pemBytes []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("certificate: %w", err)
		}
		out = append(out, cert)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("certificate: %w", ErrNotPEM)
	}
	return out, nil
}

func hasPEMBlock(b []byte) bool {
	block, _ := pem.Decode(b)
	return block != nil
}

// namesOf lists everything the certificate is valid for, sorted so the UI
// shows a stable list.
func namesOf(c *x509.Certificate) []string {
	names := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	for _, u := range c.URIs {
		names = append(names, u.String())
	}
	sort.Strings(names)
	if len(names) == 0 && c.Subject.CommonName != "" {
		names = []string{c.Subject.CommonName}
	}
	return names
}

func nameOf(n pkix.Name) string {
	if n.CommonName != "" {
		return n.CommonName
	}
	if len(n.Organization) > 0 {
		return n.Organization[0]
	}
	return n.String()
}

func colonHex(b []byte) string {
	s := strings.ToUpper(hex.EncodeToString(b))
	var out strings.Builder
	for i := 0; i < len(s); i += 2 {
		if i > 0 {
			out.WriteByte(':')
		}
		out.WriteString(s[i : i+2])
	}
	return out.String()
}
