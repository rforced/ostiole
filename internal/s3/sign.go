package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

// algorithm names the signing scheme, in the Authorization header and in
// the string that is signed.
const algorithm = "AWS4-HMAC-SHA256"

const (
	stampFormat = "20060102T150405Z"
	dateFormat  = "20060102"
)

// emptyPayload is the hash of no body at all, which every GET and DELETE
// carries.
const emptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// signable are the headers, other than host and the x-amz- family, that
// go into the signature when a request carries them.
var signable = map[string]bool{
	"content-md5":  true,
	"content-type": true,
	"date":         true,
	"range":        true,
}

// sign adds the Authorization header to a request the caller has already
// built, so that a test can feed it a published example exactly as that
// example is written.
func sign(req *http.Request, keyID, secret, region, payloadHash string, now time.Time) {
	now = now.UTC()
	req.Header.Set("X-Amz-Date", now.Format(stampFormat))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	names := signedNames(req.Header)
	scope := now.Format(dateFormat) + "/" + region + "/s3/aws4_request"
	sts := stringToSign(now, scope, canonicalRequest(req, names, payloadHash))
	sig := hex.EncodeToString(hmacSHA256(signingKey(secret, now, region), sts))
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, keyID, scope, strings.Join(names, ";"), sig))
}

// Verify recomputes a received request's signature over what actually
// arrived. The in-memory bucket in s3test checks its callers with it,
// which is how a mistake in the header set or the encoding is caught
// here rather than by a real service.
func Verify(req *http.Request, keyID, secret string, body []byte) error {
	rest, ok := strings.CutPrefix(req.Header.Get("Authorization"), algorithm+" ")
	if !ok {
		return errors.New("the request is not signed")
	}
	fields := map[string]string{}
	for part := range strings.SplitSeq(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		fields[k] = v
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 {
		return fmt.Errorf("credential %q is not key/date/region/service/aws4_request", fields["Credential"])
	}
	if cred[0] != keyID {
		return fmt.Errorf("unknown key %q", cred[0])
	}
	stamp := req.Header.Get("X-Amz-Date")
	now, err := time.Parse(stampFormat, stamp)
	if err != nil {
		return fmt.Errorf("x-amz-date %q: %w", stamp, err)
	}
	if cred[1] != now.Format(dateFormat) {
		return fmt.Errorf("credential scope date %q is not x-amz-date %q", cred[1], stamp)
	}
	hash := req.Header.Get("X-Amz-Content-Sha256")
	if want := hashOf(body); hash != want {
		return fmt.Errorf("x-amz-content-sha256 is %q, the body hashes to %q", hash, want)
	}
	names := strings.Split(fields["SignedHeaders"], ";")
	if !slices.Contains(names, "host") {
		return errors.New("the host header was not signed")
	}
	for k := range req.Header {
		l := strings.ToLower(k)
		if (signable[l] || strings.HasPrefix(l, "x-amz-")) && !slices.Contains(names, l) {
			return fmt.Errorf("%s was sent but not signed", l)
		}
	}
	canonical := canonicalRequest(req, names, hash)
	scope := strings.Join(cred[1:], "/")
	sts := stringToSign(now, scope, canonical)
	sig := hex.EncodeToString(hmacSHA256(signingKey(secret, now, cred[2]), sts))
	if !hmac.Equal([]byte(sig), []byte(fields["Signature"])) {
		return fmt.Errorf("the signature does not match; over:\n%s", canonical)
	}
	return nil
}

// signedNames is the set of headers that go into the signature, sorted.
// Host is always one of them and is never in the header map.
func signedNames(h http.Header) []string {
	names := []string{"host"}
	for k := range h {
		l := strings.ToLower(k)
		if signable[l] || strings.HasPrefix(l, "x-amz-") {
			names = append(names, l)
		}
	}
	sort.Strings(names)
	return names
}

func canonicalRequest(req *http.Request, names []string, payloadHash string) string {
	var b strings.Builder
	b.WriteString(req.Method)
	b.WriteByte('\n')
	b.WriteString(encodePath(req.URL.Path))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(req.URL.RawQuery))
	b.WriteByte('\n')
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(':')
		b.WriteString(headerValue(req, n))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(names, ";"))
	b.WriteByte('\n')
	b.WriteString(payloadHash)
	return b.String()
}

func stringToSign(now time.Time, scope, canonical string) string {
	return strings.Join([]string{algorithm, now.Format(stampFormat), scope, hashOf([]byte(canonical))}, "\n")
}

func headerValue(req *http.Request, name string) string {
	if name == "host" {
		host := req.Host
		if host == "" {
			host = req.URL.Host
		}
		return squash(host)
	}
	return squash(strings.Join(req.Header.Values(name), ","))
}

// squash trims a header value and collapses runs of spaces, which is what
// the signature is taken over.
func squash(v string) string {
	fields := strings.Fields(strings.TrimSpace(v))
	return strings.Join(fields, " ")
}

// encodePath encodes a path the way S3 canonicalises one: every segment
// escaped, the separators left alone, and no second pass.
func encodePath(p string) string {
	if p == "" {
		return "/"
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			b.WriteByte('/')
			continue
		}
		writeEscaped(&b, p[i])
	}
	return b.String()
}

// canonicalQuery sorts the parameters and re-encodes them, so that a
// value-less parameter such as lifecycle signs as "lifecycle=".
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type param struct{ key, value string }
	var params []param
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		params = append(params, param{escape(unescape(k)), escape(unescape(v))})
	}
	sort.Slice(params, func(i, j int) bool {
		if params[i].key != params[j].key {
			return params[i].key < params[j].key
		}
		return params[i].value < params[j].value
	})
	out := make([]string, 0, len(params))
	for _, p := range params {
		out = append(out, p.key+"="+p.value)
	}
	return strings.Join(out, "&")
}

// unescape reads a query component without treating a plus as a space:
// the query is built here and a plus in a key name is a plus.
func unescape(s string) string {
	out, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return out
}

// escape percent-encodes everything that is not unreserved, which is what
// both the canonical query and the request line use.
func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		writeEscaped(&b, s[i])
	}
	return b.String()
}

const hexDigits = "0123456789ABCDEF"

func writeEscaped(b *strings.Builder, c byte) {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
		c == '-', c == '_', c == '.', c == '~':
		b.WriteByte(c)
	default:
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
}

func signingKey(secret string, t time.Time, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), t.Format(dateFormat))
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	return hmacSHA256(k, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
