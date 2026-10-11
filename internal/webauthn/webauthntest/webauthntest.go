// Package webauthntest is a software authenticator. It makes what a
// browser hands back from navigator.credentials.create and get, so the
// verifier and the server can be tried without a browser or a key.
package webauthntest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"

	"ostiole/internal/webauthn"
)

const (
	flagUP = 1 << 0
	flagUV = 1 << 2
	flagAT = 1 << 6
)

// Authenticator holds one credential: a key of the kind asked for and the
// ID it goes by.
type Authenticator struct {
	// CredentialID is the ID of the credential, fixed when it is made.
	CredentialID []byte
	// SignCount is the counter Register and Assert report. They leave it as
	// it is, so set it between calls to make it climb, stand still or fall.
	SignCount uint32
	// EditAuthData, when set, gets the authenticator data before it is
	// signed and may change it or cut it short, to make what no good device
	// would.
	EditAuthData func([]byte) []byte

	alg    int
	key    crypto.Signer
	handle string
}

// New makes an authenticator with a new key of a kind: "es256", "rs256" or
// "eddsa". It panics on any other kind.
func New(kind string) *Authenticator {
	a := &Authenticator{CredentialID: make([]byte, 16)}
	_, err := rand.Read(a.CredentialID)
	must(err)
	switch kind {
	case "es256":
		a.alg = -7
		a.key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "rs256":
		a.alg = -257
		a.key, err = rsa.GenerateKey(rand.Reader, 2048)
	case "eddsa":
		a.alg = -8
		_, a.key, err = ed25519.GenerateKey(rand.Reader)
	default:
		panic("webauthntest: unknown key kind " + kind)
	}
	must(err)
	return a
}

// options is the part of the options objects the authenticator reads. The
// server's answer wraps them in "publicKey", which is looked through.
type options struct {
	PublicKey *options `json:"publicKey"`
	Challenge string   `json:"challenge"`
	RPID      string   `json:"rpId"`
	RP        struct {
		ID string `json:"id"`
	} `json:"rp"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Params []struct {
		Alg int `json:"alg"`
	} `json:"pubKeyCredParams"`
}

func readOptions(m map[string]any) options {
	raw, err := json.Marshal(m)
	must(err)
	var o options
	must(json.Unmarshal(raw, &o))
	if o.PublicKey != nil {
		o = *o.PublicKey
	}
	return o
}

// Register answers a creation request as a browser would on origin.
func (a *Authenticator) Register(opts map[string]any, origin string) webauthn.AttestationResponse {
	o := readOptions(opts)
	if o.RP.ID == "" {
		panic("webauthntest: the options name no relying party")
	}
	if len(o.Params) > 0 && !a.allowed(o) {
		panic(fmt.Sprintf("webauthntest: the options do not allow algorithm %d", a.alg))
	}
	a.handle = o.User.ID

	data := rpIDHash(o.RP.ID)
	data = append(data, flagUP|flagUV|flagAT)
	data = binary.BigEndian.AppendUint32(data, a.SignCount)
	data = append(data, make([]byte, 16)...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(a.CredentialID)))
	data = append(data, a.CredentialID...)
	data = append(data, a.coseKey()...)
	if a.EditAuthData != nil {
		data = a.EditAuthData(data)
	}

	obj := head(nil, 5, 3)
	obj = text(obj, "fmt")
	obj = text(obj, "none")
	obj = text(obj, "attStmt")
	obj = head(obj, 5, 0)
	obj = text(obj, "authData")
	obj = bytestring(obj, data)

	id := b64(a.CredentialID)
	var resp webauthn.AttestationResponse
	resp.ID, resp.RawID, resp.Type = id, id, "public-key"
	resp.Response.ClientDataJSON = b64(clientData("webauthn.create", o.Challenge, origin))
	resp.Response.AttestationObject = b64(obj)
	resp.Response.Transports = []string{"internal"}
	return resp
}

// Assert answers a request to sign in as a browser would on origin.
func (a *Authenticator) Assert(opts map[string]any, origin string) webauthn.AssertionResponse {
	o := readOptions(opts)
	if o.RPID == "" {
		panic("webauthntest: the options name no relying party")
	}
	data := rpIDHash(o.RPID)
	data = append(data, flagUP|flagUV)
	data = binary.BigEndian.AppendUint32(data, a.SignCount)
	if a.EditAuthData != nil {
		data = a.EditAuthData(data)
	}
	cd := clientData("webauthn.get", o.Challenge, origin)
	hash := sha256.Sum256(cd)

	id := b64(a.CredentialID)
	var resp webauthn.AssertionResponse
	resp.ID, resp.RawID, resp.Type = id, id, "public-key"
	resp.Response.ClientDataJSON = b64(cd)
	resp.Response.AuthenticatorData = b64(data)
	resp.Response.Signature = b64(a.sign(slices.Concat(data, hash[:])))
	resp.Response.UserHandle = a.handle
	return resp
}

func (a *Authenticator) allowed(o options) bool {
	for _, p := range o.Params {
		if p.Alg == a.alg {
			return true
		}
	}
	return false
}

func (a *Authenticator) sign(msg []byte) []byte {
	sum := sha256.Sum256(msg)
	var sig []byte
	var err error
	switch k := a.key.(type) {
	case *ecdsa.PrivateKey:
		sig, err = ecdsa.SignASN1(rand.Reader, k, sum[:])
	case *rsa.PrivateKey:
		sig, err = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	case ed25519.PrivateKey:
		sig = ed25519.Sign(k, msg)
	}
	must(err)
	return sig
}

// coseKey is the public key as a COSE_Key in CTAP2 canonical form.
func (a *Authenticator) coseKey() []byte {
	var m []byte
	switch k := a.key.(type) {
	case *ecdsa.PrivateKey:
		pt, err := k.PublicKey.Bytes()
		must(err)
		m = head(m, 5, 5)
		m = pair(m, 1, 2)
		m = pair(m, 3, -7)
		m = pair(m, -1, 1)
		m = bytestring(integer(m, -2), pt[1:33])
		m = bytestring(integer(m, -3), pt[33:])
	case *rsa.PrivateKey:
		m = head(m, 5, 4)
		m = pair(m, 1, 3)
		m = pair(m, 3, -257)
		m = bytestring(integer(m, -1), k.N.Bytes())
		m = bytestring(integer(m, -2), big.NewInt(int64(k.E)).Bytes())
	case ed25519.PrivateKey:
		m = head(m, 5, 4)
		m = pair(m, 1, 1)
		m = pair(m, 3, -8)
		m = pair(m, -1, 6)
		m = bytestring(integer(m, -2), k.Public().(ed25519.PublicKey))
	}
	return m
}

func clientData(typ, challenge, origin string) []byte {
	b, err := json.Marshal(struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}{typ, challenge, origin, false})
	must(err)
	return b
}

func rpIDHash(id string) []byte {
	sum := sha256.Sum256([]byte(id))
	return sum[:]
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func must(err error) {
	if err != nil {
		panic("webauthntest: " + err.Error())
	}
}

// The CBOR written here is only what authenticator data and an attestation
// object hold: integers, byte and text strings, and map headers.

func head(b []byte, major byte, n uint64) []byte {
	m := major << 5
	switch {
	case n < 24:
		return append(b, m|byte(n))
	case n <= 0xff:
		return append(b, m|24, byte(n))
	case n <= 0xffff:
		return binary.BigEndian.AppendUint16(append(b, m|25), uint16(n))
	default:
		return binary.BigEndian.AppendUint32(append(b, m|26), uint32(n))
	}
}

func integer(b []byte, n int64) []byte {
	if n >= 0 {
		return head(b, 0, uint64(n))
	}
	return head(b, 1, uint64(-1-n))
}

func pair(b []byte, k, v int64) []byte { return integer(integer(b, k), v) }

func bytestring(b, v []byte) []byte { return append(head(b, 2, uint64(len(v))), v...) }

func text(b []byte, s string) []byte { return append(head(b, 3, uint64(len(s))), s...) }
