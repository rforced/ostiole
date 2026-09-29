package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	return New(filepath.Join(dir, "tls", "cert.pem"), filepath.Join(dir, "tls", "key.pem"))
}

func TestEnsureSelfSignedIsOnceOnly(t *testing.T) {
	t.Parallel()
	m := manager(t)

	created, err := m.EnsureSelfSigned([]string{"fw.lan", "192.168.1.1", "", "2001:db8::1"})
	if err != nil || !created {
		t.Fatalf("EnsureSelfSigned = %v, %v", created, err)
	}
	if info, _ := os.Stat(m.KeyPath); info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %o, want the key to be readable by root alone", info.Mode().Perm())
	}
	if _, err := tls.LoadX509KeyPair(m.CertPath, m.KeyPath); err != nil {
		t.Fatalf("the pair does not load: %v", err)
	}
	raw, _ := os.ReadFile(m.CertPath)
	block, _ := pem.Decode(raw)
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.DNSNames) != 1 || parsed.DNSNames[0] != "fw.lan" || len(parsed.IPAddresses) != 2 {
		t.Errorf("SANs = %v %v", parsed.DNSNames, parsed.IPAddresses)
	}

	created, err = m.EnsureSelfSigned(nil)
	if err != nil || created {
		t.Fatalf("second EnsureSelfSigned = %v, %v; want it left alone", created, err)
	}
	// The server can serve it without reading the files again.
	if _, err := m.GetCertificate(nil); err != nil {
		t.Errorf("GetCertificate: %v", err)
	}
}

func TestInfoDescribesTheCertificate(t *testing.T) {
	t.Parallel()
	m := manager(t)
	if _, err := m.SelfSigned([]string{"fw.lan", "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	info, err := m.Info()
	if err != nil {
		t.Fatal(err)
	}
	if !info.SelfSign {
		t.Error("a self-signed certificate should say so")
	}
	if info.Expired || info.ExpiresSoon {
		t.Errorf("a fresh certificate looks expired: %+v", info)
	}
	if strings.Join(info.Names, ",") != "10.0.0.1,fw.lan" {
		t.Errorf("names = %v", info.Names)
	}
	if info.Chain != 1 {
		t.Errorf("chain = %d, want just the leaf", info.Chain)
	}
	// The fingerprint is what someone compares against their browser.
	if len(info.Fingerprint) != 95 || !strings.Contains(info.Fingerprint, ":") {
		t.Errorf("fingerprint = %q", info.Fingerprint)
	}
	if info.Subject != "ostiole" {
		t.Errorf("subject = %q", info.Subject)
	}
}

// Replacing the certificate has to take effect on the next connection, or
// the operator is told it worked while the browser keeps the old one.
func TestInstallSwapsWhatIsServed(t *testing.T) {
	t.Parallel()
	m := manager(t)
	if _, err := m.SelfSigned([]string{"old.lan"}); err != nil {
		t.Fatal(err)
	}
	before, err := m.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	certPEM, keyPEM, err := GenerateSelfSigned([]string{"new.lan"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.Install(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(info.Names, ",") != "new.lan" {
		t.Errorf("names = %v", info.Names)
	}
	after, err := m.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Certificate[0]) == string(before.Certificate[0]) {
		t.Error("the server is still serving the old certificate")
	}
	// And the files on disk agree, so a restart serves the same thing.
	reloaded := New(m.CertPath, m.KeyPath)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("the written pair does not load: %v", err)
	}
}

func TestValidateRejectsMismatchedAndMalformed(t *testing.T) {
	t.Parallel()
	certA, _, err := GenerateSelfSigned([]string{"a.lan"})
	if err != nil {
		t.Fatal(err)
	}
	_, keyB, err := GenerateSelfSigned([]string{"b.lan"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Validate(certA, keyB); !errors.Is(err, ErrMismatch) {
		t.Errorf("a key from another certificate = %v, want a mismatch", err)
	}
	if _, err := Validate([]byte("hello"), keyB); !errors.Is(err, ErrNotPEM) {
		t.Errorf("a certificate that is not PEM = %v", err)
	}
	if _, err := Validate(certA, []byte("hello")); !errors.Is(err, ErrNotPEM) {
		t.Errorf("a key that is not PEM = %v", err)
	}
}

// A rejected upload must leave the working certificate in place: this is
// the management interface, and there is no console on most of these
// routers.
func TestInstallLeavesAWorkingPairAlone(t *testing.T) {
	t.Parallel()
	m := manager(t)
	if _, err := m.SelfSigned([]string{"keep.lan"}); err != nil {
		t.Fatal(err)
	}
	certBefore, _ := os.ReadFile(m.CertPath)

	certA, _, _ := GenerateSelfSigned([]string{"a.lan"})
	_, keyB, _ := GenerateSelfSigned([]string{"b.lan"})
	if _, err := m.Install(certA, keyB); err == nil {
		t.Fatal("a mismatched pair was installed")
	}
	certAfter, _ := os.ReadFile(m.CertPath)
	if string(certBefore) != string(certAfter) {
		t.Error("the certificate was replaced by one that does not match its key")
	}
	if _, err := tls.LoadX509KeyPair(m.CertPath, m.KeyPath); err != nil {
		t.Errorf("the router is left unable to serve TLS: %v", err)
	}
}

func TestInfoWithoutACertificate(t *testing.T) {
	t.Parallel()
	if _, err := manager(t).Info(); !errors.Is(err, ErrNoCertificate) {
		t.Errorf("err = %v, want ErrNoCertificate", err)
	}
}
