package webauthn

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"fmt"
	"math"
	"math/big"
	"slices"
)

// COSE algorithm identifiers, key types and curves (RFC 9053).
const (
	algES256 = -7
	algEdDSA = -8
	algRS256 = -257

	ktyOKP = 1
	ktyEC2 = 2
	ktyRSA = 3

	crvP256    = 1
	crvEd25519 = 6
)

// RSA moduli outside these sizes are refused: smaller are too weak, larger
// make every sign-in expensive.
const (
	minRSABits = 2048
	maxRSABits = 8192
)

type publicKey interface {
	verify(signed, sig []byte) error
}

type es256Key struct{ key *ecdsa.PublicKey }

func (k es256Key) verify(signed, sig []byte) error {
	sum := sha256.Sum256(signed)
	if !ecdsa.VerifyASN1(k.key, sum[:], sig) {
		return ErrSignature
	}
	return nil
}

type rs256Key struct{ key *rsa.PublicKey }

func (k rs256Key) verify(signed, sig []byte) error {
	sum := sha256.Sum256(signed)
	if rsa.VerifyPKCS1v15(k.key, crypto.SHA256, sum[:], sig) != nil {
		return ErrSignature
	}
	return nil
}

type eddsaKey struct{ key ed25519.PublicKey }

func (k eddsaKey) verify(signed, sig []byte) error {
	if !ed25519.Verify(k.key, signed, sig) {
		return ErrSignature
	}
	return nil
}

// parseCOSEKey reads a COSE_Key. The key type and the algorithm must go
// together, so that a signature is only ever checked the way the key was
// made for.
func parseCOSEKey(raw []byte) (publicKey, error) {
	v, err := decodeCBOR(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: public key: %w", ErrMalformed, err)
	}
	m, ok := v.(cborMap)
	if !ok {
		return nil, fmt.Errorf("%w: public key is not a map", ErrMalformed)
	}
	kty, _ := m[int64(1)].(int64)
	alg, _ := m[int64(3)].(int64)
	switch {
	case kty == ktyEC2 && alg == algES256:
		return ec2Key(m)
	case kty == ktyRSA && alg == algRS256:
		return rsaKey(m)
	case kty == ktyOKP && alg == algEdDSA:
		return okpKey(m)
	}
	return nil, fmt.Errorf("%w: key type %d with algorithm %d", ErrUnsupportedKey, kty, alg)
}

func coseBytes(m cborMap, label int64) []byte {
	b, _ := m[label].([]byte)
	return b
}

func ec2Key(m cborMap) (publicKey, error) {
	crv, _ := m[int64(-1)].(int64)
	x, y := coseBytes(m, -2), coseBytes(m, -3)
	if crv != crvP256 || len(x) != 32 || len(y) != 32 {
		return nil, fmt.Errorf("%w: not a P-256 point", ErrUnsupportedKey)
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), slices.Concat([]byte{4}, x, y))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupportedKey, err)
	}
	return es256Key{key}, nil
}

func rsaKey(m cborMap) (publicKey, error) {
	n := new(big.Int).SetBytes(coseBytes(m, -1))
	e := new(big.Int).SetBytes(coseBytes(m, -2))
	if bits := n.BitLen(); bits < minRSABits || bits > maxRSABits || n.Bit(0) == 0 {
		return nil, fmt.Errorf("%w: RSA modulus of %d bits", ErrUnsupportedKey, n.BitLen())
	}
	if !e.IsInt64() || e.Int64() < 3 || e.Int64() > math.MaxInt32 || e.Bit(0) == 0 {
		return nil, fmt.Errorf("%w: RSA exponent", ErrUnsupportedKey)
	}
	return rs256Key{&rsa.PublicKey{N: n, E: int(e.Int64())}}, nil
}

func okpKey(m cborMap) (publicKey, error) {
	crv, _ := m[int64(-1)].(int64)
	x := coseBytes(m, -2)
	if crv != crvEd25519 || len(x) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: not an Ed25519 key", ErrUnsupportedKey)
	}
	return eddsaKey{ed25519.PublicKey(bytes.Clone(x))}, nil
}
