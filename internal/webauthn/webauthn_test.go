package webauthn_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ostiole/internal/webauthn"
	"ostiole/internal/webauthn/webauthntest"
)

var (
	rp     = webauthn.RelyingParty{ID: "router.example", Name: "Ostiole"}
	other  = webauthn.RelyingParty{ID: "gateway.example", Name: "Ostiole"}
	userID = []byte("0123456789abcdef")
)

const (
	origin  = "https://router.example:8443"
	flagsAt = 32
)

func challenge(t testing.TB) []byte {
	t.Helper()
	c, err := webauthn.NewChallenge()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// served passes options through JSON, wrapped as the server answers, which
// is the form a browser gets them in.
func served(t testing.TB, opts map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"publicKey": opts})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func creation(t testing.TB, c []byte) map[string]any {
	t.Helper()
	return served(t, webauthn.CreationOptions(rp, userID, "casey", nil, c))
}

func request(t testing.TB, cred webauthn.Credential, c []byte) map[string]any {
	t.Helper()
	return served(t, webauthn.RequestOptions(rp, []webauthn.Credential{cred}, c, false))
}

func register(t testing.TB, a *webauthntest.Authenticator) webauthn.Credential {
	t.Helper()
	c := challenge(t)
	resp := a.Register(creation(t, c), origin)
	cred, err := webauthn.VerifyRegistration(resp, c, rp, origin)
	if err != nil {
		t.Fatalf("registration: %v", err)
	}
	return cred
}

// edit changes the bytes behind a base64url field.
func edit(t testing.TB, s string, f func([]byte) []byte) string {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(f(b))
}

func clearPresence(d []byte) []byte {
	d[flagsAt] &^= 1
	return d
}

func withExtensions(d []byte) []byte {
	d[flagsAt] |= 0x80
	return append(d, 0xa1, 0x6b, 'h', 'm', 'a', 'c', '-', 's', 'e', 'c', 'r', 'e', 't', 0xf5)
}

func TestEveryKindOfKey(t *testing.T) {
	for _, kind := range []string{"es256", "rs256", "eddsa"} {
		t.Run(kind, func(t *testing.T) {
			a := webauthntest.New(kind)
			cred := register(t, a)
			if !bytes.Equal(cred.ID, a.CredentialID) {
				t.Fatalf("credential ID %x, want %x", cred.ID, a.CredentialID)
			}
			c := challenge(t)
			opts := request(t, cred, c)

			a.SignCount = 3
			resp := a.Assert(opts, origin)
			got, err := webauthn.VerifyAssertion(resp, cred, c, rp, origin)
			if err != nil || got.SignCount != 3 || !got.UserVerified {
				t.Fatalf("sign-in gave %+v and error %v", got, err)
			}

			tampered := func(name string, change func(*webauthn.AssertionResponse)) {
				t.Helper()
				bad := resp
				change(&bad)
				_, err := webauthn.VerifyAssertion(bad, cred, c, rp, origin)
				if !errors.Is(err, webauthn.ErrSignature) {
					t.Errorf("%s: got %v, want %v", name, err, webauthn.ErrSignature)
				}
			}
			tampered("last byte of the signature changed", func(r *webauthn.AssertionResponse) {
				r.Response.Signature = edit(t, r.Response.Signature, func(b []byte) []byte {
					b[len(b)-1] ^= 1
					return b
				})
			})
			tampered("signature cut short", func(r *webauthn.AssertionResponse) {
				r.Response.Signature = edit(t, r.Response.Signature, func(b []byte) []byte { return b[:len(b)-1] })
			})
			tampered("counter raised after signing", func(r *webauthn.AssertionResponse) {
				r.Response.AuthenticatorData = edit(t, r.Response.AuthenticatorData, func(b []byte) []byte {
					b[len(b)-1]++
					return b
				})
			})
			tampered("client data changed after signing", func(r *webauthn.AssertionResponse) {
				r.Response.ClientDataJSON = edit(t, r.Response.ClientDataJSON, func(b []byte) []byte { return append(b, ' ') })
			})
		})
	}
}

func TestRegistrationReturnsWhatTheDeviceMade(t *testing.T) {
	a := webauthntest.New("es256")
	a.SignCount = 9
	c := challenge(t)
	resp := a.Register(creation(t, c), origin)
	resp.Response.Transports = []string{"internal", "carrier-pigeon", "internal", "usb"}
	cred, err := webauthn.VerifyRegistration(resp, c, rp, origin)
	if err != nil {
		t.Fatal(err)
	}
	if cred.SignCount != 9 {
		t.Errorf("sign count %d, want 9", cred.SignCount)
	}
	if got := strings.Join(cred.Transports, ","); got != "internal,usb" {
		t.Errorf("transports %q, want internal,usb", got)
	}
	if len(cred.PublicKey) == 0 {
		t.Error("no public key")
	}
}

func TestRegistrationRefusals(t *testing.T) {
	a := webauthntest.New("es256")
	c := challenge(t)
	opts := creation(t, c)
	resp := a.Register(opts, origin)
	if _, err := webauthn.VerifyRegistration(resp, c, rp, origin); err != nil {
		t.Fatalf("a good registration was refused: %v", err)
	}

	check := func(name string, resp webauthn.AttestationResponse, c []byte, rp webauthn.RelyingParty, want error) {
		t.Helper()
		if _, err := webauthn.VerifyRegistration(resp, c, rp, origin); !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", name, err, want)
		}
	}
	registering := func(f func([]byte) []byte) webauthn.AttestationResponse {
		b := *a
		b.EditAuthData = f
		return b.Register(opts, origin)
	}

	check("another origin", a.Register(opts, "https://evil.example"), c, rp, webauthn.ErrOrigin)
	check("another challenge", resp, challenge(t), rp, webauthn.ErrChallenge)
	check("no challenge expected", a.Register(creation(t, nil), origin), nil, rp, webauthn.ErrChallenge)
	check("another relying party", resp, c, other, webauthn.ErrRPID)
	check("user not present", registering(clearPresence), c, rp, webauthn.ErrUserPresence)
	framed := resp
	framed.Response.ClientDataJSON = edit(t, resp.Response.ClientDataJSON, func(b []byte) []byte {
		return bytes.Replace(b, []byte(`"crossOrigin":false`), []byte(`"crossOrigin":true`), 1)
	})
	check("made in a frame of another site", framed, c, rp, webauthn.ErrOrigin)
	check("bytes after the key and no extension flag", registering(func(d []byte) []byte { return append(d, 0) }), c, rp, webauthn.ErrMalformed)
	check("P-256 key that claims EdDSA", registering(func(d []byte) []byte {
		i := bytes.Index(d, []byte{0xa5, 0x01, 0x02, 0x03, 0x26})
		d[i+4] = 0x27
		return d
	}), c, rp, webauthn.ErrUnsupportedKey)

	t.Run("indefinite length", func(t *testing.T) {
		bad := resp
		bad.Response.AttestationObject = edit(t, resp.Response.AttestationObject, func(b []byte) []byte {
			b[0] = 0xbf
			return append(b, 0xff)
		})
		_, err := webauthn.VerifyRegistration(bad, c, rp, origin)
		if !errors.Is(err, webauthn.ErrMalformed) || !strings.Contains(err.Error(), "indefinite") {
			t.Errorf("got %v, want a malformed response that names the indefinite length", err)
		}
	})

	var full int
	probe := *a
	probe.EditAuthData = func(d []byte) []byte {
		full = len(d)
		return d
	}
	probe.Register(opts, origin)
	for n := range full {
		check(fmt.Sprintf("authenticator data cut to %d of %d bytes", n, full), registering(func(d []byte) []byte { return d[:n] }), c, rp, webauthn.ErrMalformed)
	}

	if _, err := webauthn.VerifyRegistration(registering(withExtensions), c, rp, origin); err != nil {
		t.Errorf("extensions after the key: %v", err)
	}
}

func TestAssertionRefusals(t *testing.T) {
	a := webauthntest.New("es256")
	cred := register(t, a)
	c := challenge(t)
	opts := request(t, cred, c)
	resp := a.Assert(opts, origin)
	if _, err := webauthn.VerifyAssertion(resp, cred, c, rp, origin); err != nil {
		t.Fatalf("a good sign-in was refused: %v", err)
	}

	check := func(name string, resp webauthn.AssertionResponse, c []byte, rp webauthn.RelyingParty, want error) {
		t.Helper()
		if _, err := webauthn.VerifyAssertion(resp, cred, c, rp, origin); !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", name, err, want)
		}
	}
	signing := func(f func([]byte) []byte) webauthn.AssertionResponse {
		b := *a
		b.EditAuthData = f
		return b.Assert(opts, origin)
	}

	check("another origin", a.Assert(opts, "https://evil.example"), c, rp, webauthn.ErrOrigin)
	check("another challenge", resp, challenge(t), rp, webauthn.ErrChallenge)
	check("no challenge expected", a.Assert(request(t, cred, nil), origin), nil, rp, webauthn.ErrChallenge)
	check("another relying party", resp, c, other, webauthn.ErrRPID)
	check("user not present", signing(clearPresence), c, rp, webauthn.ErrUserPresence)
	for n := range 37 {
		check(fmt.Sprintf("authenticator data cut to %d bytes", n), signing(func(d []byte) []byte { return d[:n] }), c, rp, webauthn.ErrMalformed)
	}

	if _, err := webauthn.VerifyAssertion(signing(withExtensions), cred, c, rp, origin); err != nil {
		t.Errorf("extensions after the counter: %v", err)
	}
	if _, err := webauthn.VerifyAssertion(resp, webauthn.Credential{PublicKey: []byte{0xa0}}, c, rp, origin); !errors.Is(err, webauthn.ErrUnsupportedKey) {
		t.Errorf("a stored key that is no key: got %v", err)
	}
}

func TestSignCount(t *testing.T) {
	a := webauthntest.New("eddsa")
	cred := register(t, a)
	c := challenge(t)
	opts := request(t, cred, c)
	cases := []struct {
		name        string
		stored, got uint32
		counter     bool
	}{
		{"an authenticator that does not count", 0, 0, true},
		{"the first count", 0, 7, true},
		{"a higher count", 5, 6, true},
		{"the same count again", 5, 5, false},
		{"a lower count", 5, 3, false},
		{"a count that falls to zero", 5, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cred.SignCount, a.SignCount = tc.stored, tc.got
			got, err := webauthn.VerifyAssertion(a.Assert(opts, origin), cred, c, rp, origin)
			switch {
			case tc.counter && (err != nil || got.SignCount != tc.got):
				t.Errorf("got count %d and error %v, want %d", got.SignCount, err, tc.got)
			case !tc.counter && !errors.Is(err, webauthn.ErrCounter):
				t.Errorf("got %v, want %v", err, webauthn.ErrCounter)
			}
		})
	}
}

func TestPaddedBase64url(t *testing.T) {
	repad := func(s string) string {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return base64.URLEncoding.EncodeToString(b)
	}

	a := webauthntest.New("es256")
	c := challenge(t)
	made := webauthn.CreationOptions(rp, userID, "casey", nil, c)
	made["challenge"] = base64.URLEncoding.EncodeToString(c)
	reg := a.Register(served(t, made), origin)
	reg.Response.ClientDataJSON = repad(reg.Response.ClientDataJSON)
	reg.Response.AttestationObject = repad(reg.Response.AttestationObject)
	cred, err := webauthn.VerifyRegistration(reg, c, rp, origin)
	if err != nil {
		t.Fatalf("registration: %v", err)
	}

	as := a.Assert(request(t, cred, c), origin)
	as.Response.ClientDataJSON = repad(as.Response.ClientDataJSON)
	as.Response.AuthenticatorData = repad(as.Response.AuthenticatorData)
	as.Response.Signature = repad(as.Response.Signature)
	if _, err := webauthn.VerifyAssertion(as, cred, c, rp, origin); err != nil {
		t.Fatalf("sign-in: %v", err)
	}
}

func FuzzVerifyRegistration(f *testing.F) {
	a := webauthntest.New("es256")
	c := bytes.Repeat([]byte{7}, 32)
	opts := creation(f, c)
	var genuine []byte
	probe := *a
	probe.EditAuthData = func(d []byte) []byte {
		genuine = bytes.Clone(d)
		return d
	}
	probe.Register(opts, origin)
	f.Add(genuine)
	f.Add(genuine[:37])
	f.Add(genuine[:80])
	f.Fuzz(func(_ *testing.T, data []byte) {
		b := *a
		b.EditAuthData = func([]byte) []byte { return data }
		_, _ = webauthn.VerifyRegistration(b.Register(opts, origin), c, rp, origin)
	})
}

func TestOptionsAreReadyForABrowser(t *testing.T) {
	c := []byte{1, 2, 3}
	asJSON := func(opts map[string]any) string {
		raw, err := json.Marshal(opts)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	cred := webauthn.Credential{ID: []byte{9, 9}, Transports: []string{"usb"}}

	create := asJSON(webauthn.CreationOptions(rp, userID, "casey", nil, c))
	if !strings.Contains(create, `"excludeCredentials":[]`) {
		t.Errorf("no passkeys to exclude must be an empty list, not null: %s", create)
	}
	for _, want := range []string{
		`"challenge":"AQID"`, `"alg":-7`, `"alg":-8`, `"alg":-257`, `"attestation":"none"`, `"id":"router.example"`,
		// A passkey that signs in on its own has to be one the device can find.
		`"residentKey":"required"`, `"requireResidentKey":true`,
	} {
		if !strings.Contains(create, want) {
			t.Errorf("creation options lack %s: %s", want, create)
		}
	}
	if with := asJSON(webauthn.CreationOptions(rp, userID, "casey", []webauthn.Credential{cred}, c)); !strings.Contains(with, `{"id":"CQk","transports":["usb"],"type":"public-key"}`) {
		t.Errorf("a passkey to exclude is not listed as a descriptor: %s", with)
	}

	get := asJSON(webauthn.RequestOptions(rp, nil, c, false))
	if !strings.Contains(get, `"allowCredentials":[]`) || !strings.Contains(get, `"rpId":"router.example"`) ||
		!strings.Contains(get, `"userVerification":"preferred"`) {
		t.Errorf("request options: %s", get)
	}
	if required := asJSON(webauthn.RequestOptions(rp, nil, c, true)); !strings.Contains(required, `"userVerification":"required"`) {
		t.Errorf("request options asking for verification: %s", required)
	}
}

func TestUserVerified(t *testing.T) {
	a := webauthntest.New("es256")
	cred := register(t, a)
	c := challenge(t)
	opts := request(t, cred, c)
	a.UserVerified = false
	got, err := webauthn.VerifyAssertion(a.Assert(opts, origin), cred, c, rp, origin)
	if err != nil || got.UserVerified {
		t.Fatalf("a device that skipped the PIN gave %+v and error %v", got, err)
	}
	a.UserVerified, a.SignCount = true, 1
	if got, err = webauthn.VerifyAssertion(a.Assert(opts, origin), cred, c, rp, origin); err != nil || !got.UserVerified {
		t.Fatalf("a device that asked for the PIN gave %+v and error %v", got, err)
	}
}
