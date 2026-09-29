package tsig

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

// An UPDATE that nsupdate 9.18 signed with hmac-sha256, key test-key and
// the secret below, captured off the wire:
//
//	printf 'server 127.0.0.1 18999\nzone example.test.\nupdate delete
//	_acme-challenge.example.test. 0 TXT "gone"\nupdate add
//	_acme-challenge.example.test. 120 TXT "LoqX…"\nsend\n' |
//	nsupdate -y "hmac-sha256:test-key.:$secret"
const (
	nsupdateSecret = "b3N0aW9sZS10c2lnLXRlc3Qtc2VjcmV0LTMyYnl0ZXM="
	nsupdateSigned = "e8cd28000001000000020001076578616d706c65047465737400000600010f5f61636d652d6368616c6c656e6765c00c001000fe00000000000504676f6e65c01e0010000100000078002c2b4c6f71586359563871354f4e624a5178626d52375343544e6f337469415844666f77796a78416a4575583008746573742d6b65790000fa00ff00000000003d0b686d61632d7368613235360000006ab86fdd012c00200a4d2f47d137371baa6538876f2e90cb08f5b1f6a8441f90a8fd15fb447b4714e8cd00000000"
	// nsupdateTSIG is where the TSIG record starts.
	nsupdateTSIG = 119
)

var nsupdateTime = time.Unix(0x6ab86fdd, 0)

func nsupdateKey(t *testing.T) Key {
	t.Helper()
	k, err := NewKey("Test-Key", "HMAC-SHA256.", nsupdateSecret)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The same message, key and time sign to nsupdate's bytes exactly.
func TestSignsAsNsupdateDoes(t *testing.T) {
	t.Parallel()
	want, _ := hex.DecodeString(nsupdateSigned)
	unsigned := bytes.Clone(want[:nsupdateTSIG])
	binary.BigEndian.PutUint16(unsigned[10:], 0)
	got, mac, err := nsupdateKey(t).Sign(unsigned, nsupdateTime)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("signed:\n%x\nwant:\n%x", got, want)
	}
	if hex.EncodeToString(mac) != "0a4d2f47d137371baa6538876f2e90cb08f5b1f6a8441f90a8fd15fb447b4714" {
		t.Errorf("mac = %x", mac)
	}
}

// nsupdate's request checks out, and one changed byte or the wrong key
// does not.
func TestVerifiesNsupdatesSignature(t *testing.T) {
	t.Parallel()
	msg, _ := hex.DecodeString(nsupdateSigned)
	k := nsupdateKey(t)
	got, err := k.Verify(msg, nil, nsupdateTime.Add(time.Minute))
	if err != nil || got.Error != 0 || len(got.MAC) != 32 {
		t.Fatalf("Verify = %+v, %v", got, err)
	}
	bad := bytes.Clone(msg)
	bad[40] ^= 1
	if _, err := k.Verify(bad, nil, nsupdateTime); !errors.Is(err, ErrMAC) {
		t.Errorf("a changed message: %v", err)
	}
	other, _ := NewKey("test-key", "hmac-sha256", "b3RoZXI=")
	if _, err := other.Verify(msg, nil, nsupdateTime); !errors.Is(err, ErrMAC) {
		t.Errorf("another secret: %v", err)
	}
	sha512Key, _ := NewKey("test-key", "hmac-sha512", nsupdateSecret)
	if _, err := sha512Key.Verify(msg, nil, nsupdateTime); !errors.Is(err, ErrOtherKey) || !strings.HasSuffix(err.Error(), "hmac-sha256, not hmac-sha512") {
		t.Errorf("another algorithm: %v", err)
	}
	if _, err := k.Verify(msg, nil, nsupdateTime.Add(6*time.Minute)); !errors.Is(err, ErrTime) || !strings.Contains(err.Error(), "360 seconds away") {
		t.Errorf("an old signature: %v", err)
	}
	unsigned := bytes.Clone(msg[:nsupdateTSIG])
	binary.BigEndian.PutUint16(unsigned[10:], 0)
	if _, err := k.Verify(unsigned, nil, nsupdateTime); !errors.Is(err, ErrUnsigned) {
		t.Errorf("no TSIG record: %v", err)
	}
	if _, err := k.Verify(msg[:nsupdateTSIG+20], nil, nsupdateTime); err == nil {
		t.Error("a message cut short verified")
	}
}

// An answer is signed over the request's MAC, so it cannot be moved to
// another request.
func TestAnswersAreBoundToTheirRequest(t *testing.T) {
	t.Parallel()
	k := nsupdateKey(t)
	request, _ := hex.DecodeString(nsupdateSigned)
	requestMAC := request[nsupdateTSIG+14+2+6+2+2 : nsupdateTSIG+14+2+6+2+2+32]
	answer := bytes.Clone(request[:nsupdateTSIG])
	binary.BigEndian.PutUint16(answer[10:], 0)
	answer[2] |= 0x80
	req := Signed{MAC: requestMAC, SignedAt: 0x6ab86fdd}
	signed, err := k.SignAnswer(answer, req, nsupdateTime, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Verify(signed, requestMAC, nsupdateTime); err != nil {
		t.Errorf("Verify = %v", err)
	}
	if _, err := k.Verify(signed, make([]byte, 32), nsupdateTime); !errors.Is(err, ErrMAC) {
		t.Errorf("an answer to another request: %v", err)
	}
	// BADTIME is signed at the request's time and carries the server's.
	late, _ := k.SignAnswer(answer, req, nsupdateTime.Add(time.Hour), BadTime)
	if got, err := k.Verify(late, requestMAC, nsupdateTime.Add(time.Hour)); err != nil || got.Error != BadTime {
		t.Errorf("BADTIME answer: %+v, %v", got, err)
	}
	// A refusal of the key comes back unsigned, naming the key asked for.
	refused, err := Refuse(answer, request, BadKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(refused, []byte("\x08test-key\x00")) {
		t.Errorf("refusal %x does not name the key", refused)
	}
	other, _ := NewKey("other-key", "hmac-sha512", nsupdateSecret)
	if got, err := other.Verify(refused, requestMAC, nsupdateTime); err != nil || got.Error != BadKey {
		t.Errorf("BADKEY answer: %+v, %v", got, err)
	}
}

func TestNewKeyRefusesWhatItCannotUse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, alg, secret, want string }{
		{"k", "hmac-sha3", "c2VjcmV0", `"hmac-sha3" is not a TSIG algorithm`},
		{"k", "", "not base64!", "the TSIG secret is not base64"},
		{"", "", "c2VjcmV0", "the TSIG key has no name"},
	} {
		if _, err := NewKey(tc.name, tc.alg, tc.secret); err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Errorf("NewKey(%q, %q, %q) = %v, want %q", tc.name, tc.alg, tc.secret, err, tc.want)
		}
	}
	for _, alg := range []string{"", "hmac-md5", "HMAC-MD5.SIG-ALG.REG.INT", "hmac-sha1.", "hmac-sha224", "hmac-sha384", "hmac-sha512"} {
		if _, err := NewKey("k", alg, "c2VjcmV0"); err != nil {
			t.Errorf("%q: %v", alg, err)
		}
	}
}

// BIND 9.20's answer to an update signed with the nsupdate key, captured
// off the wire: signed over the request's MAC.
const (
	bindRequestMAC = "d1db6942a42cd61b657bfa1790826f84e7fa6f74efc5ffbdc709dd570e5e5343"
	bindAnswer     = "1234a8000001000000000001076578616d706c650474657374000006000108746573742d6b65790000fa00ff00000000003d0b686d61632d7368613235360000006ab871e6012c00202f16f643e1f424c7b5ab22a100909e29d1168a4af466f14a1f86cd53a4541aff123400000000"
)

func TestVerifiesBINDsAnswer(t *testing.T) {
	t.Parallel()
	answer, _ := hex.DecodeString(bindAnswer)
	requestMAC, _ := hex.DecodeString(bindRequestMAC)
	k := nsupdateKey(t)
	at := time.Unix(0x6ab871e6, 0)
	if _, err := k.Verify(answer, requestMAC, at); err != nil {
		t.Errorf("Verify = %v", err)
	}
	other := bytes.Clone(requestMAC)
	other[0] ^= 1
	if _, err := k.Verify(answer, other, at); !errors.Is(err, ErrMAC) {
		t.Errorf("against another request: %v", err)
	}
}
