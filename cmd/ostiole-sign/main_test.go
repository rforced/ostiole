package main

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"ostiole/internal/update"
)

func TestVerify(t *testing.T) {
	signer, signerPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "checksums.txt")
	sig := filepath.Join(dir, "checksums.txt.sig")
	data := []byte("0123abcd  ostiole_linux_amd64.tar.gz\n")
	if err := os.WriteFile(in, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sig, []byte(update.Sign(signerPriv, data)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := verify([]ed25519.PublicKey{other, signer}, in, sig); err != nil {
		t.Errorf("signed by a trusted key: %v", err)
	}
	if err := verify([]ed25519.PublicKey{other}, in, sig); err == nil {
		t.Error("signed by an untrusted key: verified")
	}
	if err := verify(nil, in, sig); err == nil {
		t.Error("no trusted keys: verified")
	}
	if err := os.WriteFile(in, append(data, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verify([]ed25519.PublicKey{signer}, in, sig); err == nil {
		t.Error("changed checksums: verified")
	}
	if err := verify([]ed25519.PublicKey{signer}, in, filepath.Join(dir, "missing.sig")); err == nil {
		t.Error("missing signature file: verified")
	}
}
