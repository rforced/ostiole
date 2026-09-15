// Package auth manages local admin accounts, password hashing, sessions,
// and login rate limiting.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. Memory is in KiB. These follow the OWASP baseline
// with a little extra memory; verification takes tens of milliseconds on
// modest appliance hardware.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Password policy.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 512
)

// ErrWeakPassword is returned for passwords outside the policy.
var ErrWeakPassword = fmt.Errorf("password must be %d to %d characters", MinPasswordLength, MaxPasswordLength)

// HashPassword returns a PHC-formatted argon2id hash.
func HashPassword(password string) (string, error) {
	if err := checkPassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func checkPassword(password string) error {
	if len(password) < MinPasswordLength || len(password) > MaxPasswordLength {
		return ErrWeakPassword
	}
	return nil
}

// VerifyPassword reports whether password matches the PHC hash. The
// parameters are taken from the hash so they can evolve over time.
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unsupported password hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, errors.New("malformed argon2 parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	if len(want) < 16 || len(want) > 128 {
		return false, errors.New("malformed argon2 key length")
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want))) //nolint:gosec // bounded above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
