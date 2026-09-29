// Package tsig signs DNS messages with a shared key and checks the
// signatures on answers (RFC 8945). RFC 2136 updates carry one.
package tsig

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // hmac-md5, for keys old BIND servers still hold
	"crypto/sha1" //nolint:gosec // hmac-sha1 is still a TSIG algorithm
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
)

// Fudge is how far apart two clocks may be, in seconds, for a signature to
// count. 300 is what RFC 8945 recommends and what BIND sends.
const Fudge = 300

// typeTSIG and classANY are the record type and class a TSIG record has.
const (
	typeTSIG = 250
	classANY = 255
)

// The errors a TSIG record carries back (RFC 8945 §5.3).
const (
	BadSig  = 16
	BadKey  = 17
	BadTime = 18
)

// algorithm is one HMAC and the name it goes by in a TSIG record.
type algorithm struct {
	name string
	hash func() hash.Hash
}

// algorithms are what a key may use, by the names an operator writes;
// hmac-sha256 when none is given.
var algorithms = map[string]algorithm{
	"hmac-md5":                 {"hmac-md5.sig-alg.reg.int.", md5.New},
	"hmac-md5.sig-alg.reg.int": {"hmac-md5.sig-alg.reg.int.", md5.New},
	"hmac-sha1":                {"hmac-sha1.", sha1.New},
	"hmac-sha224":              {"hmac-sha224.", sha256.New224},
	"hmac-sha256":              {"hmac-sha256.", sha256.New},
	"hmac-sha384":              {"hmac-sha384.", sha512.New384},
	"hmac-sha512":              {"hmac-sha512.", sha512.New},
}

// Key is a shared secret and what it is known by.
type Key struct {
	name   string
	alg    algorithm
	secret []byte
}

// NewKey reads a key as BIND writes one: its name, its algorithm, and the
// secret in base64.
func NewKey(name, alg, secret string) (Key, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ".")) + "."
	if name == "." {
		return Key{}, errors.New("the TSIG key has no name")
	}
	for label := range strings.SplitSeq(strings.TrimSuffix(name, "."), ".") {
		if label == "" || len(label) > 63 || len(name) > 254 {
			return Key{}, fmt.Errorf("%q is not a DNS name", strings.TrimSuffix(name, "."))
		}
	}
	alg = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(alg), "."))
	if alg == "" {
		alg = "hmac-sha256"
	}
	a, ok := algorithms[alg]
	if !ok {
		return Key{}, fmt.Errorf("%q is not a TSIG algorithm: use hmac-sha256, hmac-sha224, hmac-sha384, hmac-sha512, hmac-sha1 or hmac-md5", alg)
	}
	secret = strings.TrimSpace(secret)
	raw, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(secret, "="))
	}
	if err != nil || len(raw) == 0 {
		return Key{}, errors.New("the TSIG secret is not base64")
	}
	return Key{name: name, alg: a, secret: raw}, nil
}

// Name is the key's name, fully qualified.
func (k Key) Name() string { return k.name }

// Sign appends a TSIG record to msg, a packed message that has none, and
// returns the signed message and its MAC, which the answer's signature
// covers.
func (k Key) Sign(msg []byte, now time.Time) (signed, mac []byte, err error) {
	if len(msg) < 12 {
		return nil, nil, errors.New("the message has no header")
	}
	at := unix(now)
	mac = k.mac(nil, msg, at, Fudge, 0, nil)
	return k.append(msg, k.record(k.name, k.alg.name, at, mac, msg, 0, nil)), mac, nil
}

// SignAnswer signs an answer to req, as a server does, with the server's
// clock at now. A BADTIME answer is signed at the request's time and
// carries now.
func (k Key) SignAnswer(msg []byte, req Signed, now time.Time, tsigErr uint16) ([]byte, error) {
	if len(msg) < 12 {
		return nil, errors.New("the message has no header")
	}
	at, other := unix(now), []byte(nil)
	if tsigErr == BadTime {
		at, other = req.SignedAt, appendTime(nil, unix(now))
	}
	mac := k.mac(req.MAC, msg, at, Fudge, tsigErr, other)
	return k.append(msg, k.record(k.name, k.alg.name, at, mac, msg, tsigErr, other)), nil
}

// Refuse appends to msg the unsigned TSIG record that answers request with
// BADKEY or BADSIG: the key and algorithm the request named, and no MAC.
func Refuse(msg, request []byte, tsigErr uint16) ([]byte, error) {
	if len(msg) < 12 {
		return nil, errors.New("the message has no header")
	}
	off, err := lastRecord(request)
	if err != nil {
		return nil, err
	}
	rr, err := parseRecord(request, off)
	if err != nil {
		return nil, err
	}
	if rr.typ != typeTSIG {
		return nil, ErrUnsigned
	}
	return Key{}.append(msg, Key{}.record(rr.name, rr.alg, 0, nil, msg, tsigErr, nil)), nil
}

// append adds a record to the message's additional section.
func (Key) append(msg, rr []byte) []byte {
	out := append(bytes.Clone(msg), rr...)
	binary.BigEndian.PutUint16(out[10:], binary.BigEndian.Uint16(out[10:])+1)
	return out
}

func unix(t time.Time) uint64 {
	return uint64(t.Unix()) //nolint:gosec // a clock past 1970
}

// Signed is what a TSIG record said.
type Signed struct {
	// Error is the TSIG record's own error: BadSig, BadKey or BadTime
	// when the other side refused the signature.
	Error uint16
	// MAC is the record's MAC, which an answer to it is signed over.
	MAC []byte
	// SignedAt is the record's time, in seconds since 1970.
	SignedAt uint64
}

// Why a signature does not check out.
var (
	ErrUnsigned = errors.New("the message is not signed")
	ErrOtherKey = errors.New("the message is signed with another key")
	ErrMAC      = errors.New("the signature does not match")
	ErrTime     = errors.New("the signature is too old or too new")
)

// Verify checks the TSIG record at the end of msg: the key, the MAC over
// the message, requestMAC for an answer, and the time. A record carrying
// an error comes back with it; the caller says what the error meant. One
// signed at the wrong time comes back with its MAC and ErrTime, so that a
// server can answer it with BADTIME.
func (k Key) Verify(msg, requestMAC []byte, now time.Time) (Signed, error) {
	off, err := lastRecord(msg)
	if err != nil {
		return Signed{}, err
	}
	rr, err := parseRecord(msg, off)
	if err != nil {
		return Signed{}, err
	}
	if rr.typ != typeTSIG {
		return Signed{}, ErrUnsigned
	}
	// A refusal of the key or the signature is unsigned by design.
	if rr.err == BadSig || rr.err == BadKey {
		return Signed{Error: rr.err}, nil
	}
	if rr.name != k.name {
		return Signed{}, fmt.Errorf("%w: %s, not %s", ErrOtherKey, strings.TrimSuffix(rr.name, "."), strings.TrimSuffix(k.name, "."))
	}
	if rr.alg != k.alg.name {
		return Signed{}, fmt.Errorf("%w: %s, not %s", ErrOtherKey, strings.TrimSuffix(rr.alg, "."), strings.TrimSuffix(k.alg.name, "."))
	}
	stripped := bytes.Clone(msg[:off])
	binary.BigEndian.PutUint16(stripped, rr.origID)
	binary.BigEndian.PutUint16(stripped[10:], binary.BigEndian.Uint16(stripped[10:])-1)
	want := k.mac(requestMAC, stripped, rr.signedAt, rr.fudge, rr.err, rr.other)
	if !hmac.Equal(want, rr.mac) {
		return Signed{}, ErrMAC
	}
	signed := Signed{Error: rr.err, MAC: rr.mac, SignedAt: rr.signedAt}
	if rr.err == BadTime {
		return signed, nil
	}
	skew := now.Unix() - int64(rr.signedAt) //nolint:gosec // a clock past 1970
	if skew < -int64(rr.fudge) || skew > int64(rr.fudge) {
		return signed, fmt.Errorf("%w: signed %d seconds away from this clock, more than the %d allowed", ErrTime, skew, rr.fudge)
	}
	return signed, nil
}

// mac is the HMAC over the prior MAC, the message and the TSIG variables
// (RFC 8945 §4.3). other is a BADTIME answer's clock.
func (k Key) mac(requestMAC, msg []byte, signedAt uint64, fudge, tsigErr uint16, other []byte) []byte {
	h := hmac.New(k.alg.hash, k.secret)
	if requestMAC != nil {
		h.Write(binary.BigEndian.AppendUint16(nil, uint16(len(requestMAC)))) //nolint:gosec // a MAC is a hash long
		h.Write(requestMAC)
	}
	h.Write(msg)
	v := appendName(nil, k.name)
	v = binary.BigEndian.AppendUint16(v, classANY)
	v = binary.BigEndian.AppendUint32(v, 0)
	v = appendName(v, k.alg.name)
	v = appendTime(v, signedAt)
	v = binary.BigEndian.AppendUint16(v, fudge)
	v = binary.BigEndian.AppendUint16(v, tsigErr)
	v = binary.BigEndian.AppendUint16(v, uint16(len(other))) //nolint:gosec // six bytes at most
	v = append(v, other...)
	h.Write(v)
	return h.Sum(nil)
}

// record is a TSIG record for msg, names uncompressed.
func (Key) record(name, alg string, signedAt uint64, mac, msg []byte, tsigErr uint16, other []byte) []byte {
	rdata := appendName(nil, alg)
	rdata = appendTime(rdata, signedAt)
	rdata = binary.BigEndian.AppendUint16(rdata, Fudge)
	rdata = binary.BigEndian.AppendUint16(rdata, uint16(len(mac))) //nolint:gosec // a MAC is a hash long
	rdata = append(rdata, mac...)
	rdata = append(rdata, msg[:2]...)
	rdata = binary.BigEndian.AppendUint16(rdata, tsigErr)
	rdata = binary.BigEndian.AppendUint16(rdata, uint16(len(other))) //nolint:gosec // six bytes at most
	rdata = append(rdata, other...)

	out := appendName(nil, name)
	out = binary.BigEndian.AppendUint16(out, typeTSIG)
	out = binary.BigEndian.AppendUint16(out, classANY)
	out = binary.BigEndian.AppendUint32(out, 0)
	out = binary.BigEndian.AppendUint16(out, uint16(len(rdata))) //nolint:gosec // a few hundred bytes
	return append(out, rdata...)
}

// appendTime writes a time as TSIG does, in 48 bits.
func appendTime(b []byte, t uint64) []byte {
	return append(b, binary.BigEndian.AppendUint64(nil, t)[2:]...)
}

// appendName writes a lowercase, fully qualified name without compression,
// which is how TSIG takes names into the MAC.
func appendName(b []byte, name string) []byte {
	for label := range strings.SplitSeq(strings.TrimSuffix(name, "."), ".") {
		if label == "" {
			continue
		}
		b = append(b, byte(len(label))) //nolint:gosec // NewKey keeps labels to 63 bytes
		b = append(b, strings.ToLower(label)...)
	}
	return append(b, 0)
}

// tsigRecord is a TSIG record read off the wire.
type tsigRecord struct {
	name, alg   string
	typ         uint16
	signedAt    uint64
	fudge       uint16
	mac, other  []byte
	origID, err uint16
}

var errShort = errors.New("the message ends early")

// lastRecord is where the message's last additional record starts.
func lastRecord(msg []byte) (int, error) {
	if len(msg) < 12 {
		return 0, errShort
	}
	qd := int(binary.BigEndian.Uint16(msg[4:]))
	rest := int(binary.BigEndian.Uint16(msg[6:])) + int(binary.BigEndian.Uint16(msg[8:]))
	ar := int(binary.BigEndian.Uint16(msg[10:]))
	if ar == 0 {
		return 0, ErrUnsigned
	}
	off := 12
	var err error
	for range qd {
		if off, err = skipName(msg, off); err != nil {
			return 0, err
		}
		off += 4
	}
	for range rest + ar - 1 {
		if off, err = skipName(msg, off); err != nil {
			return 0, err
		}
		if off+10 > len(msg) {
			return 0, errShort
		}
		off += 10 + int(binary.BigEndian.Uint16(msg[off+8:]))
	}
	if off >= len(msg) {
		return 0, errShort
	}
	return off, nil
}

func parseRecord(msg []byte, off int) (tsigRecord, error) {
	var rr tsigRecord
	var err error
	if rr.name, off, err = readName(msg, off); err != nil {
		return rr, err
	}
	if off+10 > len(msg) {
		return rr, errShort
	}
	rr.typ = binary.BigEndian.Uint16(msg[off:])
	end := off + 10 + int(binary.BigEndian.Uint16(msg[off+8:]))
	if end > len(msg) {
		return rr, errShort
	}
	if rr.typ != typeTSIG {
		return rr, nil
	}
	off += 10
	if rr.alg, off, err = readName(msg, off); err != nil {
		return rr, err
	}
	if off+10 > end {
		return rr, errShort
	}
	t := msg[off:]
	rr.signedAt = uint64(t[0])<<40 | uint64(t[1])<<32 | uint64(t[2])<<24 | uint64(t[3])<<16 | uint64(t[4])<<8 | uint64(t[5])
	rr.fudge = binary.BigEndian.Uint16(msg[off+6:])
	size := int(binary.BigEndian.Uint16(msg[off+8:]))
	off += 10
	if off+size+6 > end {
		return rr, errShort
	}
	rr.mac = msg[off : off+size]
	off += size
	rr.origID = binary.BigEndian.Uint16(msg[off:])
	rr.err = binary.BigEndian.Uint16(msg[off+2:])
	otherLen := int(binary.BigEndian.Uint16(msg[off+4:]))
	if off+6+otherLen > end {
		return rr, errShort
	}
	rr.other = msg[off+6 : off+6+otherLen]
	return rr, nil
}

// skipName steps over a name, compressed or not.
func skipName(msg []byte, off int) (int, error) {
	for {
		if off >= len(msg) {
			return 0, errShort
		}
		switch l := int(msg[off]); {
		case l == 0:
			return off + 1, nil
		case l&0xc0 == 0xc0:
			return off + 2, nil
		default:
			off += 1 + l
		}
	}
}

// readName reads a name, following compression pointers, lowercase and
// fully qualified.
func readName(msg []byte, off int) (string, int, error) {
	var b strings.Builder
	next := -1
	for jumps := 0; ; {
		if off >= len(msg) {
			return "", 0, errShort
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if next < 0 {
				next = off + 1
			}
			if b.Len() == 0 {
				return ".", next, nil
			}
			return strings.ToLower(b.String()), next, nil
		case l&0xc0 == 0xc0:
			if off+1 >= len(msg) || jumps > 10 {
				return "", 0, errors.New("a name's compression goes round in a loop")
			}
			if next < 0 {
				next = off + 2
			}
			jumps++
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3fff)
		default:
			if off+1+l > len(msg) {
				return "", 0, errShort
			}
			b.Write(msg[off+1 : off+1+l])
			b.WriteByte('.')
			off += 1 + l
		}
	}
}
