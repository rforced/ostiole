package wg

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateAndDerive(t *testing.T) {
	t.Parallel()
	priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidKey(priv) {
		t.Fatalf("generated key is invalid: %q", priv)
	}
	raw, _ := base64.StdEncoding.DecodeString(priv)
	if raw[0]&7 != 0 || raw[31]&128 != 0 || raw[31]&64 == 0 {
		t.Errorf("key is not clamped: %x", raw)
	}
	pub, err := PublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidKey(pub) || pub == priv {
		t.Errorf("public key = %q", pub)
	}
	again, _ := PublicKey(priv)
	if again != pub {
		t.Error("derivation is not deterministic")
	}
	other, _ := GenerateKey()
	if other == priv {
		t.Error("two generated keys are the same")
	}
}

func TestPSKAndValidation(t *testing.T) {
	t.Parallel()
	psk, err := GeneratePSK()
	if err != nil || !ValidKey(psk) {
		t.Fatalf("psk = %q, %v", psk, err)
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if ValidKey(bad) {
			t.Errorf("ValidKey(%q) = true", bad)
		}
	}
	if _, err := PublicKey("not base64!"); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Errorf("err = %v", err)
	}
}

// The key pair from the WireGuard documentation; the derivation was
// cross-checked against an X25519 implementation outside Go.
func TestMatchesReferenceVector(t *testing.T) {
	t.Parallel()
	const priv = "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	const want = "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw="
	got, err := PublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("PublicKey = %q, want %q", got, want)
	}
}
