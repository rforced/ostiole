package totp

import (
	"encoding/base32"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The shared secret of the RFC 4226 and RFC 6238 test vectors, raw and as
// an app is given it.
const (
	rfcKey    = "12345678901234567890"
	rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
)

// RFC 6238 appendix B, the SHA-1 rows, which use eight digits.
func TestRFC6238Vectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, c := range cases {
		if got := code([]byte(rfcKey), Step(time.Unix(c.unix, 0)), 8); got != c.want {
			t.Errorf("T=%d: code = %s, want %s", c.unix, got, c.want)
		}
	}
}

// RFC 4226 appendix D lists the six digit codes for counters 0 to 9. The
// last row is the RFC 6238 vector at 1111111109 cut to six digits; its code
// starts with a zero, which an app shows and a server must keep.
func TestCodeIsSixDigits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		step int64
		want string
	}{
		{0, "755224"},
		{1, "287082"},
		{2, "359152"},
		{3, "969429"},
		{4, "338314"},
		{5, "254676"},
		{6, "287922"},
		{7, "162583"},
		{8, "399871"},
		{9, "520489"},
		{37037036, "081804"},
	}
	for _, c := range cases {
		got, err := Code(rfcSecret, c.step)
		if err != nil || got != c.want {
			t.Errorf("step %d: code = %q, %v, want %q", c.step, got, err, c.want)
		}
	}
}

func TestSecretsAreRandomAndWork(t *testing.T) {
	t.Parallel()
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 || secret != strings.ToUpper(secret) || strings.Contains(secret, "=") {
		t.Errorf("secret = %q, want 32 upper case characters without padding", secret)
	}
	if other, _ := NewSecret(); other == secret {
		t.Error("two secrets were the same")
	}

	now := time.Now()
	entered, err := Code(secret, Step(now))
	if err != nil {
		t.Fatal(err)
	}
	if step, ok, err := Verify(secret, entered, now, 0); err != nil || !ok || step != Step(now) {
		t.Errorf("Verify = %d, %v, %v, want %d, true, nil", step, ok, err, Step(now))
	}
}

func TestVerifySkew(t *testing.T) {
	t.Parallel()
	at := time.Unix(1111111111, 0)
	cases := []struct {
		name string
		off  int64
		skew int
		ok   bool
	}{
		{"on time", 0, 0, true},
		{"a step early, no skew", -1, 0, false},
		{"a step late, no skew", 1, 0, false},
		{"a step early, skew 1", -1, 1, true},
		{"a step late, skew 1", 1, 1, true},
		{"two steps early, skew 1", -2, 1, false},
		{"two steps late, skew 1", 2, 1, false},
		{"two steps early, skew 2", -2, 2, true},
		{"three steps late, skew 2", 3, 2, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			want := Step(at) + c.off
			entered, err := Code(rfcSecret, want)
			if err != nil {
				t.Fatal(err)
			}
			step, ok, err := Verify(rfcSecret, entered, at, c.skew)
			if err != nil || ok != c.ok {
				t.Fatalf("Verify = %d, %v, %v, want ok %v", step, ok, err, c.ok)
			}
			if ok && step != want {
				t.Errorf("step = %d, want %d", step, want)
			}
		})
	}
}

func TestVerifyTakesTheCodeAsTyped(t *testing.T) {
	t.Parallel()
	at := time.Unix(1111111111, 0)
	for _, entered := range []string{"050471", "050 471", " 050471\n"} {
		if _, ok, err := Verify(rfcSecret, entered, at, 0); err != nil || !ok {
			t.Errorf("Verify(%q) = %v, %v, want true", entered, ok, err)
		}
	}
}

func TestVerifyRefusesAWrongCode(t *testing.T) {
	t.Parallel()
	at := time.Unix(1111111111, 0)
	cases := map[string]string{
		"another code":     "050472",
		"zeros":            "000000",
		"too short":        "05047",
		"too long":         "0504711",
		"the 8 digit form": "14050471",
		"letters":          "05047a",
		"empty":            "",
		"blank":            "      ",
	}
	for name, entered := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if step, ok, err := Verify(rfcSecret, entered, at, 1); err != nil || ok {
				t.Errorf("Verify(%q) = %d, %v, %v, want a refusal without an error", entered, step, ok, err)
			}
		})
	}
}

func TestMalformedSecret(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"empty":        "",
		"padding only": "====",
		"not base32":   "not base32!",
		"digit one":    "GEZDGNB1",
	}
	for name, secret := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got, err := Code(secret, 1); err == nil {
				t.Errorf("Code = %q, want an error", got)
			}
			if _, ok, err := Verify(secret, "123456", time.Unix(59, 0), 1); err == nil || ok {
				t.Errorf("Verify = %v, %v, want an error", ok, err)
			}
		})
	}
}

// A 19 byte key needs padding, which some apps and tools write and others
// leave off, in either case.
func TestSecretSpellings(t *testing.T) {
	t.Parallel()
	key := []byte(rfcKey[:19])
	padded := base32.StdEncoding.EncodeToString(key)
	want := code(key, 1, Digits)
	spellings := map[string]string{
		"unpadded":     encoding.EncodeToString(key),
		"padded":       padded,
		"lower":        strings.ToLower(encoding.EncodeToString(key)),
		"padded lower": strings.ToLower(padded),
	}
	for name, secret := range spellings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got, err := Code(secret, 1); err != nil || got != want {
				t.Errorf("Code(%q) = %q, %v, want %q", secret, got, err, want)
			}
		})
	}
}

func TestURI(t *testing.T) {
	t.Parallel()
	key := []byte(rfcKey[:19])
	canonical := encoding.EncodeToString(key)
	cases := []struct {
		name, secret, issuer, account, label string
	}{
		{"plain", canonical, "Ostiole", "josh", "Ostiole:josh"},
		{"space and at sign", canonical, "Ostiole router", "josh@example.net", "Ostiole router:josh@example.net"},
		{"no issuer", canonical, "", "josh", "josh"},
		{"secret as spelled with padding", strings.ToLower(base32.StdEncoding.EncodeToString(key)), "Ostiole", "josh", "Ostiole:josh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw := URI(c.secret, c.issuer, c.account)
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("%q: %v", raw, err)
			}
			if u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/"+c.label {
				t.Errorf("%q: scheme %q, host %q, label %q, want otpauth, totp, %q", raw, u.Scheme, u.Host, u.Path, "/"+c.label)
			}
			want := url.Values{"secret": {canonical}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
			if c.issuer != "" {
				want.Set("issuer", c.issuer)
			}
			if got := u.Query(); !reflect.DeepEqual(got, want) {
				t.Errorf("%q: query = %v, want %v", raw, got, want)
			}
			if strings.ContainsAny(raw, " +") {
				t.Errorf("%q has a space or a plus, which apps do not all read the same", raw)
			}
		})
	}
}
