package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCert(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cert := filepath.Join(dir, "tls", "cert.pem")
	key := filepath.Join(dir, "tls", "key.pem")

	created, err := EnsureCert(cert, key, []string{"fw.lan", "192.168.1.1", "", "2001:db8::1"})
	if err != nil || !created {
		t.Fatalf("EnsureCert = %v, %v", created, err)
	}
	if info, _ := os.Stat(key); info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %o", info.Mode().Perm())
	}
	if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
		t.Fatalf("pair does not load: %v", err)
	}
	raw, _ := os.ReadFile(cert)
	block, _ := pem.Decode(raw)
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.DNSNames) != 1 || parsed.DNSNames[0] != "fw.lan" || len(parsed.IPAddresses) != 2 {
		t.Errorf("SANs = %v %v", parsed.DNSNames, parsed.IPAddresses)
	}

	created, err = EnsureCert(cert, key, nil)
	if err != nil || created {
		t.Fatalf("second EnsureCert = %v, %v; want no-op", created, err)
	}
}
