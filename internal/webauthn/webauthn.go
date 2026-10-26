// Package webauthn registers passkeys and checks their sign-ins: the
// server half of Web Authentication. Attestation is read and ignored, so
// a credential is trusted the first time it is seen, as an SSH key is.
package webauthn

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Errors returned by VerifyRegistration and VerifyAssertion.
var (
	ErrMalformed      = errors.New("the response is malformed")
	ErrChallenge      = errors.New("the challenge does not match")
	ErrOrigin         = errors.New("the origin is not this site's")
	ErrRPID           = errors.New("the credential belongs to another relying party")
	ErrUserPresence   = errors.New("the user was not present")
	ErrUnsupportedKey = errors.New("the public key is not supported")
	ErrSignature      = errors.New("the signature does not verify")
	ErrCounter        = errors.New("the signature counter did not increase")
)

const (
	flagUP = 1 << 0
	flagUV = 1 << 2
	flagAT = 1 << 6
	flagED = 1 << 7

	authDataMin = sha256.Size + 1 + 4

	aaguidLen       = 16
	maxCredentialID = 1023
)

const timeoutMillis = 60000

// transports are the values of AuthenticatorTransport. A credential keeps
// only these, since they go back to the browser as hints.
var transports = []string{"usb", "nfc", "ble", "smart-card", "hybrid", "internal"}

// RelyingParty is the site a passkey belongs to. ID is a host name, never
// an IP address.
type RelyingParty struct{ ID, Name string }

// Credential is a passkey as the server keeps it.
type Credential struct {
	ID         []byte
	PublicKey  []byte // COSE_Key, CBOR
	SignCount  uint32
	Transports []string
}

// NewChallenge returns 32 random bytes for one ceremony.
func NewChallenge() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// b64Decode reads base64url with or without padding.
func b64Decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// descriptors lists credentials as PublicKeyCredentialDescriptor objects.
// An empty list stays a list: a browser rejects null.
func descriptors(creds []Credential) []map[string]any {
	out := make([]map[string]any, 0, len(creds))
	for _, c := range creds {
		d := map[string]any{"type": "public-key", "id": b64(c.ID)}
		if len(c.Transports) > 0 {
			d["transports"] = c.Transports
		}
		out = append(out, d)
	}
	return out
}

// CreationOptions is the PublicKeyCredentialCreationOptions object, ready
// for JSON, with bytes as base64url without padding.
func CreationOptions(rp RelyingParty, userID []byte, username string, exclude []Credential, challenge []byte) map[string]any {
	return map[string]any{
		"rp":        map[string]any{"id": rp.ID, "name": rp.Name},
		"user":      map[string]any{"id": b64(userID), "name": username, "displayName": username},
		"challenge": b64(challenge),
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": algES256},
			{"type": "public-key", "alg": algEdDSA},
			{"type": "public-key", "alg": algRS256},
		},
		"timeout":                timeoutMillis,
		"attestation":            "none",
		"authenticatorSelection": map[string]any{"residentKey": "required", "requireResidentKey": true, "userVerification": "preferred"},
		"excludeCredentials":     descriptors(exclude),
	}
}

// RequestOptions is the PublicKeyCredentialRequestOptions object, ready for
// JSON, with bytes as base64url without padding. requireUV asks for the
// device PIN or biometric rather than preferring it.
func RequestOptions(rp RelyingParty, allow []Credential, challenge []byte, requireUV bool) map[string]any {
	uv := "preferred"
	if requireUV {
		uv = "required"
	}
	return map[string]any{
		"challenge":        b64(challenge),
		"rpId":             rp.ID,
		"allowCredentials": descriptors(allow),
		"userVerification": uv,
		"timeout":          timeoutMillis,
	}
}

// AttestationResponse is what PublicKeyCredential.toJSON() gives for a
// creation. The browser sends more than this, which is ignored.
type AttestationResponse struct {
	ID       string          `json:"id"`
	RawID    string          `json:"rawId"`
	Type     string          `json:"type"`
	Response AttestationBody `json:"response"`
}

// AttestationBody is the response member of an AttestationResponse.
type AttestationBody struct {
	ClientDataJSON    string   `json:"clientDataJSON"`
	AttestationObject string   `json:"attestationObject"`
	Transports        []string `json:"transports,omitempty"`
}

// AssertionResponse is what PublicKeyCredential.toJSON() gives for a
// sign-in.
type AssertionResponse struct {
	ID       string        `json:"id"`
	RawID    string        `json:"rawId"`
	Type     string        `json:"type"`
	Response AssertionBody `json:"response"`
}

// AssertionBody is the response member of an AssertionResponse.
type AssertionBody struct {
	ClientDataJSON    string `json:"clientDataJSON"`
	AuthenticatorData string `json:"authenticatorData"`
	Signature         string `json:"signature"`
	UserHandle        string `json:"userHandle,omitempty"`
}

// VerifyRegistration checks type "webauthn.create", the challenge, the
// origin, the relying party hash, the user present flag and the attested
// credential, and returns the credential.
func VerifyRegistration(resp AttestationResponse, challenge []byte, rp RelyingParty, origin string) (Credential, error) {
	if _, err := checkClientData(resp.Response.ClientDataJSON, "webauthn.create", challenge, origin); err != nil {
		return Credential{}, err
	}
	obj, err := b64Decode(resp.Response.AttestationObject)
	if err != nil {
		return Credential{}, fmt.Errorf("%w: attestation object: %w", ErrMalformed, err)
	}
	v, err := decodeCBOR(obj)
	if err != nil {
		return Credential{}, fmt.Errorf("%w: attestation object: %w", ErrMalformed, err)
	}
	m, _ := v.(cborMap)
	raw, ok := m["authData"].([]byte)
	if !ok {
		return Credential{}, fmt.Errorf("%w: the attestation object has no authenticator data", ErrMalformed)
	}
	data, err := readAuthData(raw, rp)
	if err != nil {
		return Credential{}, err
	}
	id, key, err := readAttested(data)
	if err != nil {
		return Credential{}, err
	}
	if _, err := parseCOSEKey(key); err != nil {
		return Credential{}, err
	}
	return Credential{
		ID:         bytes.Clone(id),
		PublicKey:  bytes.Clone(key),
		SignCount:  data.count,
		Transports: knownTransports(resp.Response.Transports),
	}, nil
}

// Assertion is what a verified sign-in tells the server: the count to keep
// and whether the device checked who was holding it.
type Assertion struct {
	SignCount    uint32
	UserVerified bool
}

// VerifyAssertion checks type "webauthn.get", the challenge, the origin, the
// relying party hash, the user present flag, the signature over the
// authenticator data and the hash of the client data with the credential's
// key, and the sign count.
func VerifyAssertion(resp AssertionResponse, cred Credential, challenge []byte, rp RelyingParty, origin string) (Assertion, error) {
	hash, err := checkClientData(resp.Response.ClientDataJSON, "webauthn.get", challenge, origin)
	if err != nil {
		return Assertion{}, err
	}
	raw, err := b64Decode(resp.Response.AuthenticatorData)
	if err != nil {
		return Assertion{}, fmt.Errorf("%w: authenticator data: %w", ErrMalformed, err)
	}
	data, err := readAuthData(raw, rp)
	if err != nil {
		return Assertion{}, err
	}
	sig, err := b64Decode(resp.Response.Signature)
	if err != nil {
		return Assertion{}, fmt.Errorf("%w: signature: %w", ErrMalformed, err)
	}
	key, err := parseCOSEKey(cred.PublicKey)
	if err != nil {
		return Assertion{}, err
	}
	if err := key.verify(slices.Concat(raw, hash[:]), sig); err != nil {
		return Assertion{}, err
	}
	if (cred.SignCount != 0 || data.count != 0) && data.count <= cred.SignCount {
		return Assertion{}, ErrCounter
	}
	return Assertion{SignCount: data.count, UserVerified: data.flags&flagUV != 0}, nil
}

// checkClientData checks what the browser says about the ceremony and
// returns the hash of the client data, which the signature covers.
func checkClientData(encoded, wantType string, challenge []byte, origin string) ([sha256.Size]byte, error) {
	var none [sha256.Size]byte
	raw, err := b64Decode(encoded)
	if err != nil {
		return none, fmt.Errorf("%w: client data: %w", ErrMalformed, err)
	}
	var cd struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}
	if err := json.Unmarshal(raw, &cd); err != nil {
		return none, fmt.Errorf("%w: client data: %w", ErrMalformed, err)
	}
	if cd.Type != wantType {
		return none, fmt.Errorf("%w: client data is of type %q, not %q", ErrMalformed, cd.Type, wantType)
	}
	got, err := b64Decode(cd.Challenge)
	if err != nil || len(challenge) == 0 || subtle.ConstantTimeCompare(got, challenge) != 1 {
		return none, ErrChallenge
	}
	if origin == "" || cd.Origin != origin || cd.CrossOrigin {
		return none, ErrOrigin
	}
	return sha256.Sum256(raw), nil
}

type authData struct {
	flags byte
	count uint32
	rest  []byte
}

// readAuthData checks the length, the relying party hash and the user
// present flag, and leaves what follows the counter in rest.
func readAuthData(b []byte, rp RelyingParty) (authData, error) {
	if len(b) < authDataMin {
		return authData{}, fmt.Errorf("%w: authenticator data is %d bytes, want at least %d", ErrMalformed, len(b), authDataMin)
	}
	want := sha256.Sum256([]byte(rp.ID))
	if rp.ID == "" || subtle.ConstantTimeCompare(b[:sha256.Size], want[:]) != 1 {
		return authData{}, ErrRPID
	}
	d := authData{flags: b[sha256.Size], count: binary.BigEndian.Uint32(b[sha256.Size+1:]), rest: b[authDataMin:]}
	if d.flags&flagUP == 0 {
		return authData{}, ErrUserPresence
	}
	return d, nil
}

// readAttested picks the credential ID and the COSE key out of the attested
// credential data. Extensions may follow the key, when the flag says so.
func readAttested(d authData) (id, key []byte, err error) {
	if d.flags&flagAT == 0 {
		return nil, nil, fmt.Errorf("%w: no attested credential data", ErrMalformed)
	}
	const head = aaguidLen + 2
	if len(d.rest) < head {
		return nil, nil, fmt.Errorf("%w: attested credential data is cut short", ErrMalformed)
	}
	n := int(binary.BigEndian.Uint16(d.rest[aaguidLen:head]))
	if n == 0 || n > maxCredentialID || len(d.rest) < head+n {
		return nil, nil, fmt.Errorf("%w: credential ID of %d bytes does not fit", ErrMalformed, n)
	}
	id, rest := d.rest[head:head+n], d.rest[head+n:]
	_, used, err := decodeCBORPrefix(rest)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: public key: %w", ErrMalformed, err)
	}
	if used < len(rest) && d.flags&flagED == 0 {
		return nil, nil, fmt.Errorf("%w: %d bytes after the public key", ErrMalformed, len(rest)-used)
	}
	return id, rest[:used], nil
}

func knownTransports(in []string) []string {
	var out []string
	for _, t := range in {
		if slices.Contains(transports, t) && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}
