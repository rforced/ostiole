// Package wg generates and checks the keys WireGuard uses. Keys are the
// base64 encoding of 32 raw bytes, the same format wg(8) prints.
package wg

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// KeyLen is the length of every WireGuard key in bytes.
const KeyLen = 32

// GenerateKey returns a new X25519 private key.
func GenerateKey() (string, error) {
	var key [KeyLen]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	clamp(&key)
	return base64.StdEncoding.EncodeToString(key[:]), nil
}

// GeneratePSK returns a preshared key: 32 random bytes with no structure.
func GeneratePSK() (string, error) {
	var key [KeyLen]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key[:]), nil
}

// PublicKey derives the public key belonging to a private key.
func PublicKey(private string) (string, error) {
	raw, err := DecodeKey(private)
	if err != nil {
		return "", err
	}
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("derive public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// DecodeKey checks the encoding and length of a key.
func DecodeKey(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid key: not base64")
	}
	if len(raw) != KeyLen {
		return nil, fmt.Errorf("invalid key: %d bytes, want %d", len(raw), KeyLen)
	}
	return raw, nil
}

// ValidKey reports whether s is a well-formed key.
func ValidKey(s string) bool {
	_, err := DecodeKey(s)
	return err == nil
}

// clamp applies the X25519 private key conditioning from RFC 7748, which
// is what wg(8) does when it generates a key.
func clamp(key *[KeyLen]byte) {
	key[0] &= 248
	key[31] &= 127
	key[31] |= 64
}
