package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// RFC 7638 §3.1's example key and the thumbprint it gives.
func TestThumbprintMatchesRFC7638(t *testing.T) {
	t.Parallel()
	n, err := base64.RawURLEncoding.DecodeString("0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw")
	if err != nil {
		t.Fatal(err)
	}
	key := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: new(big.Int).SetBytes(n), E: 65537}}
	s, err := newSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.thumbprint(), "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"; got != want {
		t.Errorf("thumbprint = %q, want %q", got, want)
	}
	if got := s.keyAuthorization("token"); got != "token.NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs" {
		t.Errorf("key authorization = %q", got)
	}
}

type decodedJWS struct {
	header    map[string]any
	payload   []byte
	signature []byte
	input     string
}

func decodeJWS(t *testing.T, raw []byte) decodedJWS {
	t.Helper()
	var j jws
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	var d decodedJWS
	protected, err := base64.RawURLEncoding.DecodeString(j.Protected)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(protected, &d.header); err != nil {
		t.Fatal(err)
	}
	if d.payload, err = base64.RawURLEncoding.DecodeString(j.Payload); err != nil {
		t.Fatal(err)
	}
	if d.signature, err = base64.RawURLEncoding.DecodeString(j.Signature); err != nil {
		t.Fatal(err)
	}
	d.input = j.Protected + "." + j.Payload
	return d
}

// An EC signature is r and s side by side at the curve's size (RFC 7518
// §3.4), padded when either is short, which one signature in a hundred
// or so needs. ASN.1 would not verify this way.
func TestECSignaturesAreFixedWidth(t *testing.T) {
	t.Parallel()
	for _, curve := range []elliptic.Curve{elliptic.P256(), elliptic.P384()} {
		key, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		s, err := newSigner(key)
		if err != nil {
			t.Fatal(err)
		}
		size := (curve.Params().BitSize + 7) / 8
		padded := false
		for i := 0; i < 2000 && !padded; i++ {
			raw, err := s.sign("https://ca.example/order", "nonce", "https://ca.example/acct/1", []byte(`{"a":1}`))
			if err != nil {
				t.Fatal(err)
			}
			d := decodeJWS(t, raw)
			if len(d.signature) != 2*size {
				t.Fatalf("%s: signature is %d bytes, want %d", s.alg, len(d.signature), 2*size)
			}
			h := s.hash.New()
			h.Write([]byte(d.input))
			r, ss := new(big.Int).SetBytes(d.signature[:size]), new(big.Int).SetBytes(d.signature[size:])
			if !ecdsa.Verify(&key.PublicKey, h.Sum(nil), r, ss) {
				t.Fatalf("%s: the signature does not verify", s.alg)
			}
			padded = d.signature[0] == 0 || d.signature[size] == 0
		}
		if !padded {
			t.Errorf("%s: no signature needed padding in 2000", s.alg)
		}
	}
}

// The header carries the key until the account's URL is known, then the
// URL; a POST-as-GET's payload is empty.
func TestHeadersCarryTheKeyThenTheAccount(t *testing.T) {
	t.Parallel()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, err := newSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := s.sign("https://ca.example/new-acct", "n1", "", []byte(`{}`))
	d := decodeJWS(t, raw)
	jwk, ok := d.header["jwk"].(map[string]any)
	if !ok || d.header["kid"] != nil || jwk["crv"] != "P-256" || d.header["alg"] != "ES256" ||
		d.header["nonce"] != "n1" || d.header["url"] != "https://ca.example/new-acct" {
		t.Errorf("header = %v", d.header)
	}
	raw, _ = s.sign("https://ca.example/authz/1", "n2", "https://ca.example/acct/1", nil)
	d = decodeJWS(t, raw)
	if d.header["jwk"] != nil || d.header["kid"] != "https://ca.example/acct/1" || len(d.payload) != 0 {
		t.Errorf("POST-as-GET: header %v, payload %q", d.header, d.payload)
	}
	var j jws
	_ = json.Unmarshal(raw, &j)
	if j.Payload != "" {
		t.Errorf("POST-as-GET payload = %q, want empty", j.Payload)
	}

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rs, err := newSigner(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = rs.sign("https://ca.example/order", "n3", "https://ca.example/acct/2", []byte(`{}`))
	d = decodeJWS(t, raw)
	sum := sha256.Sum256([]byte(d.input))
	if d.header["alg"] != "RS256" || rsa.VerifyPKCS1v15(&rsaKey.PublicKey, crypto.SHA256, sum[:], d.signature) != nil {
		t.Errorf("RS256: header %v", d.header)
	}
}

// RFC 8555 §7.3.4: the binding is a JWS over the account's key, signed
// with HS256 and the CA's MAC key, naming the CA's key id and the
// newAccount URL.
func TestExternalAccountBindingShape(t *testing.T) {
	t.Parallel()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, _ := newSigner(key)
	mac, _ := decodeMAC("zWNDZM6eQGHWpSRTPal5eIUYFTu7EajVIoguysqZ9wG44nMEtx3MUAsUDkMTQ12W")
	raw, err := s.bind("https://ca.example/new-acct", "kid-1", mac)
	if err != nil {
		t.Fatal(err)
	}
	d := decodeJWS(t, raw)
	if len(d.header) != 3 || d.header["alg"] != "HS256" || d.header["kid"] != "kid-1" || d.header["url"] != "https://ca.example/new-acct" {
		t.Errorf("header = %v, want alg, kid and url alone", d.header)
	}
	if string(d.payload) != string(s.jwk) {
		t.Errorf("payload = %s, want the account key %s", d.payload, s.jwk)
	}
	h := hmac.New(sha256.New, mac)
	h.Write([]byte(d.input))
	if !hmac.Equal(h.Sum(nil), d.signature) {
		t.Error("the MAC does not verify")
	}
	if _, err := decodeMAC("not base64url!"); err == nil {
		t.Error("a MAC key that is not base64url was taken")
	}
}

// RFC 9773 §4.1's example: the key identifier and serial from its sample
// certificate.
func TestCertIDMatchesRFC9773(t *testing.T) {
	t.Parallel()
	aki, _ := hex.DecodeString("69885B6B87464041E1B37B847BA0AE2CDE01C8D4")
	serial, _ := new(big.Int).SetString("87654321", 16)
	got, err := certID(&x509.Certificate{AuthorityKeyId: aki, SerialNumber: serial})
	if err != nil {
		t.Fatal(err)
	}
	if want := "aYhba4dGQEHhs3uEe6CuLN4ByNQ.AIdlQyE"; got != want {
		t.Errorf("certID = %q, want %q", got, want)
	}
	if _, err := certID(&x509.Certificate{SerialNumber: serial}); err == nil || !strings.Contains(err.Error(), "authority key identifier") {
		t.Errorf("no AKI: %v", err)
	}
}
