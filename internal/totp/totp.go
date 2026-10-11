// Package totp makes and checks the codes an authenticator app shows: RFC
// 6238 one-time passwords with HMAC-SHA1, six digits and a 30 second step,
// the only settings every app supports.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // authenticator apps compute HMAC-SHA1, which SHA-1 collisions do not break
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Digits is the length of a code and Period how long each one lasts.
const (
	Digits = 6
	Period = 30 * time.Second
)

const (
	secretBytes   = 20
	periodSeconds = int64(Period / time.Second)
)

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random secret: 20 bytes as upper case base32 without padding.
func NewSecret() (string, error) {
	key := make([]byte, secretBytes)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return encoding.EncodeToString(key), nil
}

// Step is the number of the 30 second step that holds the time.
func Step(at time.Time) int64 {
	return at.Unix() / periodSeconds
}

// Code is the code an app shows for the secret during the step.
func Code(secret string, step int64) (string, error) {
	key, err := decode(secret)
	if err != nil {
		return "", err
	}
	return code(key, step, Digits), nil
}

// Verify reports whether the code is right at the step for at or within skew
// steps either side, and returns the step that matched, which a caller records
// to refuse a replay. Only a bad secret is an error.
func Verify(secret, given string, at time.Time, skew int) (step int64, ok bool, err error) {
	key, err := decode(secret)
	if err != nil {
		return 0, false, err
	}
	given = strings.Join(strings.Fields(given), "")
	if len(given) != Digits {
		return 0, false, nil
	}
	now, reach := Step(at), int64(skew)
	// Every step is tried and the latest match kept, so the time taken does
	// not say which step it was and a replay check on the result holds.
	for s := now - reach; s <= now+reach; s++ {
		if subtle.ConstantTimeCompare([]byte(code(key, s, Digits)), []byte(given)) == 1 {
			step, ok = s, true
		}
	}
	return step, ok, nil
}

// URI is the otpauth address an authenticator app reads from a QR code. The
// issuer and the account must not contain a colon.
func URI(secret, issuer, account string) string {
	label := url.PathEscape(account)
	query := []string{"secret=" + escape(normalise(secret))}
	if issuer != "" {
		label = url.PathEscape(issuer) + ":" + label
		query = append(query, "issuer="+escape(issuer))
	}
	query = append(query,
		"algorithm=SHA1",
		"digits="+strconv.Itoa(Digits),
		"period="+strconv.FormatInt(periodSeconds, 10),
	)
	return "otpauth://totp/" + label + "?" + strings.Join(query, "&")
}

// code is RFC 4226's HOTP with the step as the counter.
func code(key []byte, step int64, digits int) string {
	mac := hmac.New(sha1.New, key)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step)) //nolint:gosec // the counter is the step's own eight bytes
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := uint64(binary.BigEndian.Uint32(sum[offset:]) & 0x7fffffff)
	modulus := uint64(1)
	for range digits {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%modulus)
}

func normalise(secret string) string {
	return strings.ToUpper(strings.TrimRight(secret, "="))
}

func decode(secret string) ([]byte, error) {
	key, err := encoding.DecodeString(normalise(secret))
	if err != nil {
		return nil, errors.New("the secret is not base32")
	}
	if len(key) == 0 {
		return nil, errors.New("the secret is empty")
	}
	return key, nil
}

// escape spells a space %20, because some apps read the plus QueryEscape uses
// as a plus.
func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
