package backup

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/model"
)

func TestEncryptedBackupOpensWithItsPassphrase(t *testing.T) {
	t.Parallel()
	a, err := Create(cfg(), Options{Ostiole: "v0.3.0", Passphrase: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(raw) {
		t.Fatalf("a passphrase wrote plain bytes: %q", raw[:min(len(raw), 60)])
	}
	// The file names itself so age(1) is willing to open it.
	if !strings.HasSuffix(a.Filename(), ".json"+Suffix) {
		t.Errorf("filename = %q", a.Filename())
	}
	// And the configuration is not in the bytes anywhere.
	if bytes.Contains(raw, []byte("gateway")) {
		t.Error("the hostname survived encryption in the clear")
	}

	plain, err := Decrypt(raw, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(plain)
	if err != nil {
		t.Fatal(err)
	}
	if back.Config.System.Hostname != "gateway" {
		t.Errorf("hostname = %q", back.Config.System.Hostname)
	}
}

func TestDecryptRefusesWithoutTheRightPassphrase(t *testing.T) {
	t.Parallel()
	a, _ := Create(cfg(), Options{Passphrase: "one"})
	raw, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(raw, ""); !errors.Is(err, ErrPassphraseNeeded) {
		t.Errorf("no passphrase = %v, want ErrPassphraseNeeded", err)
	}
	if _, err := Decrypt(raw, "two"); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("wrong passphrase = %v, want ErrBadPassphrase", err)
	}
}

// A plain file goes through Decrypt untouched, so one restore path
// handles both shapes.
func TestDecryptPassesPlainJSONThrough(t *testing.T) {
	t.Parallel()
	a, _ := Create(cfg(), Options{})
	raw, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if IsEncrypted(raw) {
		t.Fatal("a backup with no passphrase came out encrypted")
	}
	out, err := Decrypt(raw, "ignored")
	if err != nil || !bytes.Equal(out, raw) {
		t.Errorf("Decrypt of plain JSON = %v, %q", err, out)
	}
}

func TestRedactedBackupCarriesNoSecrets(t *testing.T) {
	t.Parallel()
	c := cfg()
	c.Interfaces = append(c.Interfaces, model.Interface{
		Name:      "wg0",
		Enabled:   true,
		IPv4:      model.IPv4{Mode: model.AddrStatic, Address: "10.66.0.1/24"},
		IPv6:      model.IPv6{Mode: model.AddrNone},
		WireGuard: &model.WireGuard{PrivateKey: "aGVsbG8="},
	})
	a, err := Create(c, Options{Redact: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("aGVsbG8=")) {
		t.Error("the tunnel key is in a redacted backup")
	}
	if !a.Summary().Redacted {
		t.Error("the summary does not say the secrets are missing")
	}
	// The original is untouched, so taking a shareable backup does not
	// blank the running configuration.
	if c.Interfaces[len(c.Interfaces)-1].WireGuard.PrivateKey == "" {
		t.Error("Create redacted the configuration it was handed")
	}
}

// Accounts and redaction do not go together: a password hash is a secret
// however the rest of the file was written.
func TestRedactedBackupRefusesAccounts(t *testing.T) {
	t.Parallel()
	_, err := Create(cfg(), Options{
		Redact: true,
		Users:  []auth.User{{Username: "admin", Hash: "$argon2id$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"}},
	})
	if !errors.Is(err, ErrRedactedUsers) {
		t.Errorf("err = %v, want ErrRedactedUsers", err)
	}
}
