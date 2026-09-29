package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
)

// signer signs requests with an account key (RFC 8555 §6.2): ES256 or
// ES384 for an EC key, RS256 for an RSA one.
type signer struct {
	key  crypto.PrivateKey
	alg  string
	hash crypto.Hash
	// size is an EC signature's r and s, each this many bytes (RFC 7518
	// §3.4): a signature is the two side by side, not ASN.1.
	size int
	// jwk is the public key as RFC 7638 hashes it: the required members
	// only, in lexicographic order, no whitespace.
	jwk []byte
}

func newSigner(key crypto.PrivateKey) (*signer, error) {
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		s := &signer{key: k}
		var crv string
		switch k.Curve {
		case elliptic.P256():
			s.alg, s.hash, s.size, crv = "ES256", crypto.SHA256, 32, "P-256"
		case elliptic.P384():
			s.alg, s.hash, s.size, crv = "ES384", crypto.SHA384, 48, "P-384"
		default:
			return nil, fmt.Errorf("an EC account key is P-256 or P-384, not %s", k.Curve.Params().Name)
		}
		point, err := k.PublicKey.Bytes()
		if err != nil {
			return nil, err
		}
		// An uncompressed point: 0x04, then x and y at the curve's size.
		x, y := point[1:1+s.size], point[1+s.size:]
		s.jwk = fmt.Appendf(nil, `{"crv":%q,"kty":"EC","x":%q,"y":%q}`, crv, b64(x), b64(y))
		return s, nil
	case *rsa.PrivateKey:
		e := big.NewInt(int64(k.E)).Bytes()
		return &signer{
			key: k, alg: "RS256", hash: crypto.SHA256,
			jwk: fmt.Appendf(nil, `{"e":%q,"kty":"RSA","n":%q}`, b64(e), b64(k.N.Bytes())),
		}, nil
	}
	return nil, fmt.Errorf("a CA account key is EC or RSA, not %T", key)
}

// thumbprint is the key's RFC 7638 thumbprint, which a key authorization
// ends in.
func (s *signer) thumbprint() string {
	sum := sha256.Sum256(s.jwk)
	return b64(sum[:])
}

// keyAuthorization is what proves this account answered a challenge (RFC
// 8555 §8.1).
func (s *signer) keyAuthorization(token string) string {
	return token + "." + s.thumbprint()
}

// jws is a request body in the flattened JSON serialization (RFC 7515
// §7.2.2).
type jws struct {
	Protected string `json:"protected"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// sign makes the body of a POST to url. Until the account's URL is known
// the key itself goes in the header; after, the URL as kid. A nil payload
// is a POST-as-GET, whose payload is empty.
func (s *signer) sign(url, nonce, kid string, payload []byte) ([]byte, error) {
	header := map[string]any{"alg": s.alg, "nonce": nonce, "url": url}
	if kid != "" {
		header["kid"] = kid
	} else {
		header["jwk"] = json.RawMessage(s.jwk)
	}
	protected, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	out := jws{Protected: b64(protected), Payload: b64(payload)}
	h := s.hash.New()
	h.Write([]byte(out.Protected + "." + out.Payload))
	digest := h.Sum(nil)
	var sig []byte
	switch k := s.key.(type) {
	case *ecdsa.PrivateKey:
		r, ss, err := ecdsa.Sign(rand.Reader, k, digest)
		if err != nil {
			return nil, err
		}
		sig = make([]byte, 2*s.size)
		r.FillBytes(sig[:s.size])
		ss.FillBytes(sig[s.size:])
	case *rsa.PrivateKey:
		if sig, err = rsa.SignPKCS1v15(rand.Reader, k, s.hash, digest); err != nil {
			return nil, err
		}
	}
	out.Signature = b64(sig)
	return json.Marshal(out)
}

// bind is an external account binding (RFC 8555 §7.3.4): the account's
// public key, signed with the MAC key the CA handed out as kid.
func (s *signer) bind(url, kid string, macKey []byte) (json.RawMessage, error) {
	protected, err := json.Marshal(map[string]string{"alg": "HS256", "kid": kid, "url": url})
	if err != nil {
		return nil, err
	}
	out := jws{Protected: b64(protected), Payload: b64(s.jwk)}
	mac := hmac.New(sha256.New, macKey)
	mac.Write([]byte(out.Protected + "." + out.Payload))
	out.Signature = b64(mac.Sum(nil))
	return json.Marshal(out)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
