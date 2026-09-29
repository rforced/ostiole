package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCA is an ACME server in memory, over HTTPS, for what pebble will not
// do on demand: a run of refused nonces, a rate limit, a challenge that
// fails, a finalize still processing. It checks every request's JWS with
// the standard library alone, so a signature it takes is one any CA
// would.
type fakeCA struct {
	t   *testing.T
	srv *httptest.Server

	caKey  *ecdsa.PrivateKey
	caCert *x509.Certificate

	mu       sync.Mutex
	nonces   map[string]bool
	next     int
	accounts map[string]crypto.PublicKey // account URL → key
	byKey    map[string]string           // thumbprint → account URL
	orders   map[string]*fakeOrder
	authzs   map[string]*fakeAuthz
	certs    map[string][]byte
	// issuedTo is the account each certificate went to, by its ARI id.
	issuedTo map[string]string

	// What it is asked to get wrong.
	badNonces       int
	rateLimited     bool
	failing         map[string]bool
	processing      int
	alreadyReplaced bool
	forgetAccounts  bool
	eabKID          string
	eabKey          []byte

	// What it saw.
	calls       []string
	deactivated []string
	replaces    []string
	created     int
}

type fakeOrder struct {
	id          string
	account     string
	status      string
	identifiers []identifier
	authzs      []string
	cert        string
	err         *Problem
}

type fakeAuthz struct {
	id         string
	identifier identifier
	status     string
	chalStatus string
	token      string
	err        *Problem
}

func newFakeCA(t *testing.T) *fakeCA {
	t.Helper()
	f := &fakeCA{
		t: t, nonces: map[string]bool{}, accounts: map[string]crypto.PublicKey{}, byKey: map[string]string{},
		orders: map[string]*fakeOrder{}, authzs: map[string]*fakeAuthz{}, certs: map[string][]byte{}, issuedTo: map[string]string{},
		failing: map[string]bool{},
	}
	var err error
	if f.caKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		SubjectKeyId: []byte{1, 2, 3, 4},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &f.caKey.PublicKey, f.caKey)
	if err != nil {
		t.Fatal(err)
	}
	f.caCert, _ = x509.ParseCertificate(der)
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// trust is the PEM an account names so the client trusts the fake.
func (f *fakeCA) trust() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw}))
}

func (f *fakeCA) url(path string) string { return f.srv.URL + path }

func (f *fakeCA) id() string {
	f.next++
	return fmt.Sprint(f.next)
}

func (f *fakeCA) nonce(w http.ResponseWriter) {
	n := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("nonce-%d-%d", f.next, len(f.nonces))))
	f.nonces[n] = true
	f.next++
	w.Header().Set("Replay-Nonce", n)
}

func (f *fakeCA) problem(w http.ResponseWriter, status int, kind, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: problemPrefix + kind, Detail: detail, Status: status})
}

func (f *fakeCA) reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeCA) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/dir":
		f.reply(w, http.StatusOK, map[string]any{
			"newNonce": f.url("/nonce"), "newAccount": f.url("/account"), "newOrder": f.url("/order"),
			"renewalInfo": f.url("/renewal"), "meta": map[string]any{"externalAccountRequired": f.eabKID != ""},
		})
		return
	case r.Method == http.MethodHead && r.URL.Path == "/nonce":
		f.nonce(w)
		w.WriteHeader(http.StatusOK)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/renewal/"):
		w.Header().Set("Retry-After", "21600")
		f.reply(w, http.StatusOK, map[string]any{"suggestedWindow": map[string]any{
			"start": "2026-10-01T00:00:00Z", "end": "2026-10-02T00:00:00Z",
		}})
		return
	case r.Method != http.MethodPost:
		f.problem(w, http.StatusMethodNotAllowed, "malformed", "POST only")
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.nonce(w)
	hdr, payload, err := f.verify(r, body)
	if err != nil {
		f.problem(w, http.StatusBadRequest, err.kind, err.detail)
		return
	}
	if f.badNonces > 0 {
		f.badNonces--
		f.problem(w, http.StatusBadRequest, "badNonce", "JWS has an invalid anti-replay nonce")
		return
	}
	path := r.URL.Path
	switch {
	case path == "/account":
		f.account(w, hdr, payload)
	case path == "/order":
		f.newOrder(w, hdr, payload)
	case strings.HasPrefix(path, "/authz/"):
		f.authz(w, strings.TrimPrefix(path, "/authz/"), payload)
	case strings.HasPrefix(path, "/chall/"):
		f.challenge(w, strings.TrimPrefix(path, "/chall/"))
	case strings.HasPrefix(path, "/order/"):
		o := f.orders[strings.TrimPrefix(path, "/order/")]
		f.reply(w, http.StatusOK, f.orderJSON(o))
		// Each read brings a processing order closer to valid.
		if o.status == "processing" {
			if f.processing--; f.processing <= 0 {
				o.status = "valid"
			}
		}
	case strings.HasPrefix(path, "/finalize/"):
		f.finalize(w, strings.TrimPrefix(path, "/finalize/"), payload)
	case strings.HasPrefix(path, "/cert/"):
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(f.certs[strings.TrimPrefix(path, "/cert/")])
	default:
		f.problem(w, http.StatusNotFound, "malformed", "no such resource")
	}
}

type jwsHeader struct {
	Alg   string          `json:"alg"`
	Nonce string          `json:"nonce"`
	URL   string          `json:"url"`
	Kid   string          `json:"kid"`
	JWK   json.RawMessage `json:"jwk"`
}

type fakeError struct{ kind, detail string }

// verify checks a request's JWS: a nonce this server gave and nobody
// used, the URL it was sent to, and the signature, by the key in the
// header or the account's.
func (f *fakeCA) verify(r *http.Request, body []byte) (jwsHeader, []byte, *fakeError) {
	var j jws
	var hdr jwsHeader
	if err := json.Unmarshal(body, &j); err != nil {
		return hdr, nil, &fakeError{"malformed", "not a JWS"}
	}
	raw, _ := base64.RawURLEncoding.DecodeString(j.Protected)
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return hdr, nil, &fakeError{"malformed", "bad protected header"}
	}
	if !f.nonces[hdr.Nonce] {
		return hdr, nil, &fakeError{"badNonce", "unknown nonce"}
	}
	delete(f.nonces, hdr.Nonce)
	if hdr.URL != f.url(r.URL.Path) {
		return hdr, nil, &fakeError{"malformed", "url " + hdr.URL + " is not " + f.url(r.URL.Path)}
	}
	var key crypto.PublicKey
	switch {
	case hdr.Kid != "" && hdr.JWK != nil:
		return hdr, nil, &fakeError{"malformed", "both kid and jwk"}
	case hdr.Kid != "":
		if key = f.accounts[hdr.Kid]; key == nil {
			return hdr, nil, &fakeError{"accountDoesNotExist", "no such account"}
		}
	default:
		k, err := parseJWK(hdr.JWK)
		if err != nil {
			return hdr, nil, &fakeError{"badPublicKey", err.Error()}
		}
		key = k
	}
	sig, _ := base64.RawURLEncoding.DecodeString(j.Signature)
	input := []byte(j.Protected + "." + j.Payload)
	ok := false
	switch k := key.(type) {
	case *ecdsa.PublicKey:
		size := (k.Curve.Params().BitSize + 7) / 8
		h := sha256.New()
		if hdr.Alg == "ES384" {
			h = crypto.SHA384.New()
		}
		h.Write(input)
		ok = len(sig) == 2*size && ecdsa.Verify(k, h.Sum(nil), new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:]))
	case *rsa.PublicKey:
		sum := sha256.Sum256(input)
		ok = hdr.Alg == "RS256" && rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig) == nil
	}
	if !ok {
		return hdr, nil, &fakeError{"unauthorized", "the signature does not verify"}
	}
	payload, _ := base64.RawURLEncoding.DecodeString(j.Payload)
	return hdr, payload, nil
}

func parseJWK(raw json.RawMessage) (crypto.PublicKey, error) {
	var k struct{ Kty, Crv, X, Y, N, E string }
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, err
	}
	d := func(s string) []byte { b, _ := base64.RawURLEncoding.DecodeString(s); return b }
	switch k.Kty {
	case "EC":
		curve := elliptic.P256()
		if k.Crv == "P-384" {
			curve = elliptic.P384()
		}
		return ecdsa.ParseUncompressedPublicKey(curve, append(append([]byte{4}, d(k.X)...), d(k.Y)...))
	case "RSA":
		return &rsa.PublicKey{N: new(big.Int).SetBytes(d(k.N)), E: int(new(big.Int).SetBytes(d(k.E)).Int64())}, nil
	}
	return nil, fmt.Errorf("kty %q", k.Kty)
}

func thumb(raw json.RawMessage) string {
	var m map[string]string
	_ = json.Unmarshal(raw, &m)
	var canon string
	if m["kty"] == "EC" {
		canon = fmt.Sprintf(`{"crv":%q,"kty":"EC","x":%q,"y":%q}`, m["crv"], m["x"], m["y"])
	} else {
		canon = fmt.Sprintf(`{"e":%q,"kty":"RSA","n":%q}`, m["e"], m["n"])
	}
	sum := sha256.Sum256([]byte(canon))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (f *fakeCA) account(w http.ResponseWriter, hdr jwsHeader, payload []byte) {
	if hdr.JWK == nil {
		f.problem(w, http.StatusBadRequest, "malformed", "newAccount wants a jwk")
		return
	}
	var req struct {
		OnlyReturnExisting bool            `json:"onlyReturnExisting"`
		Terms              bool            `json:"termsOfServiceAgreed"`
		Contact            []string        `json:"contact"`
		Binding            json.RawMessage `json:"externalAccountBinding"`
	}
	_ = json.Unmarshal(payload, &req)
	tp := thumb(hdr.JWK)
	if url, ok := f.byKey[tp]; ok {
		w.Header().Set("Location", url)
		f.reply(w, http.StatusOK, map[string]any{"status": "valid"})
		return
	}
	if req.OnlyReturnExisting {
		f.problem(w, http.StatusBadRequest, "accountDoesNotExist", "no account for this key")
		return
	}
	if !req.Terms {
		f.problem(w, http.StatusBadRequest, "malformed", "the terms were not agreed")
		return
	}
	if f.eabKID != "" {
		if err := f.checkBinding(req.Binding, hdr.JWK); err != "" {
			f.problem(w, http.StatusUnauthorized, "externalAccountRequired", err)
			return
		}
	}
	key, _ := parseJWK(hdr.JWK)
	url := f.url("/acct/" + f.id())
	f.accounts[url], f.byKey[tp] = key, url
	f.created++
	w.Header().Set("Location", url)
	f.reply(w, http.StatusCreated, map[string]any{"status": "valid", "contact": req.Contact})
}

func (f *fakeCA) checkBinding(raw, accountJWK json.RawMessage) string {
	if raw == nil {
		return "an external account binding is required"
	}
	var j jws
	if json.Unmarshal(raw, &j) != nil {
		return "the binding is not a JWS"
	}
	protected, _ := base64.RawURLEncoding.DecodeString(j.Protected)
	var hdr map[string]string
	_ = json.Unmarshal(protected, &hdr)
	if hdr["alg"] != "HS256" || hdr["kid"] != f.eabKID || hdr["url"] != f.url("/account") {
		return "the binding's header is wrong"
	}
	payload, _ := base64.RawURLEncoding.DecodeString(j.Payload)
	if thumb(payload) != thumb(accountJWK) {
		return "the binding is for another key"
	}
	mac := hmac.New(sha256.New, f.eabKey)
	mac.Write([]byte(j.Protected + "." + j.Payload))
	sig, _ := base64.RawURLEncoding.DecodeString(j.Signature)
	if !hmac.Equal(mac.Sum(nil), sig) {
		return "the binding's MAC does not verify"
	}
	return ""
}

func (f *fakeCA) newOrder(w http.ResponseWriter, hdr jwsHeader, payload []byte) {
	var req struct {
		Identifiers []identifier `json:"identifiers"`
		Replaces    string       `json:"replaces"`
	}
	_ = json.Unmarshal(payload, &req)
	if f.forgetAccounts {
		f.forgetAccounts = false
		f.accounts, f.byKey = map[string]crypto.PublicKey{}, map[string]string{}
		f.problem(w, http.StatusBadRequest, "accountDoesNotExist", "no such account")
		return
	}
	if f.rateLimited {
		w.Header().Set("Retry-After", "3600")
		f.problem(w, http.StatusTooManyRequests, "rateLimited", "too many certificates already issued for example.test")
		return
	}
	if req.Replaces != "" {
		f.replaces = append(f.replaces, req.Replaces)
		if f.alreadyReplaced {
			f.problem(w, http.StatusConflict, "alreadyReplaced", "that certificate is already being replaced")
			return
		}
		// Let's Encrypt's words for a certificate another account ordered.
		if owner, ok := f.issuedTo[req.Replaces]; ok && owner != hdr.Kid {
			f.problem(w, http.StatusForbidden, "unauthorized", "requester account did not request the certificate being replaced by this order")
			return
		}
	}
	o := &fakeOrder{id: f.id(), account: hdr.Kid, status: "pending", identifiers: req.Identifiers}
	for _, id := range req.Identifiers {
		az := &fakeAuthz{id: f.id(), identifier: id, status: "pending", chalStatus: "pending", token: "token-" + id.Value}
		f.authzs[az.id] = az
		o.authzs = append(o.authzs, az.id)
	}
	f.orders[o.id] = o
	w.Header().Set("Location", f.url("/order/"+o.id))
	f.reply(w, http.StatusCreated, f.orderJSON(o))
}

func (f *fakeCA) orderJSON(o *fakeOrder) map[string]any {
	var authzs []string
	for _, id := range o.authzs {
		authzs = append(authzs, f.url("/authz/"+id))
	}
	out := map[string]any{"status": o.status, "identifiers": o.identifiers, "authorizations": authzs, "finalize": f.url("/finalize/" + o.id)}
	if o.cert != "" {
		out["certificate"] = f.url("/cert/" + o.cert)
	}
	if o.err != nil {
		out["error"] = o.err
	}
	return out
}

func (f *fakeCA) authzJSON(az *fakeAuthz) map[string]any {
	chal := map[string]any{"type": "http-01", "url": f.url("/chall/" + az.id), "token": az.token, "status": az.chalStatus}
	if az.err != nil {
		chal["error"] = az.err
	}
	dns := map[string]any{"type": "dns-01", "url": f.url("/chall/" + az.id), "token": az.token, "status": az.chalStatus}
	return map[string]any{"status": az.status, "identifier": az.identifier, "challenges": []any{chal, dns}}
}

func (f *fakeCA) authz(w http.ResponseWriter, id string, payload []byte) {
	az := f.authzs[id]
	if az == nil {
		f.problem(w, http.StatusNotFound, "malformed", "no such authorization")
		return
	}
	if len(payload) > 0 {
		var req struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(payload, &req)
		if req.Status == "deactivated" {
			az.status = "deactivated"
			f.deactivated = append(f.deactivated, az.identifier.Value)
		}
	} else if az.status == "processing" {
		// The check finishes by the time anyone asks again.
		az.status, az.chalStatus = "valid", "valid"
		if f.failing[az.identifier.Value] {
			az.status, az.chalStatus = "invalid", "invalid"
			az.err = &Problem{Type: problemPrefix + "incorrectResponse", Detail: "The key authorization file from the server did not match this challenge"}
		}
	}
	f.reply(w, http.StatusOK, f.authzJSON(az))
}

func (f *fakeCA) challenge(w http.ResponseWriter, id string) {
	az := f.authzs[id]
	if az == nil {
		f.problem(w, http.StatusNotFound, "malformed", "no such challenge")
		return
	}
	if az.status == "pending" {
		az.status, az.chalStatus = "processing", "processing"
	}
	w.Header().Set("Link", `<`+f.url("/authz/"+id)+`>;rel="up"`)
	f.reply(w, http.StatusOK, map[string]any{"type": "http-01", "url": f.url("/chall/" + id), "token": az.token, "status": az.chalStatus})
}

func (f *fakeCA) finalize(w http.ResponseWriter, id string, payload []byte) {
	o := f.orders[id]
	for _, azID := range o.authzs {
		if f.authzs[azID].status != "valid" {
			f.problem(w, http.StatusForbidden, "orderNotReady", "the order is not ready")
			return
		}
	}
	var req struct {
		CSR string `json:"csr"`
	}
	_ = json.Unmarshal(payload, &req)
	der, _ := base64.RawURLEncoding.DecodeString(req.CSR)
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil || csr.CheckSignature() != nil {
		f.problem(w, http.StatusBadRequest, "badCSR", "the CSR does not parse")
		return
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(int64(1000 + f.next)), Subject: csr.Subject,
		DNSNames: csr.DNSNames, IPAddresses: csr.IPAddresses,
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour),
		AuthorityKeyId: f.caCert.SubjectKeyId,
	}
	leaf, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, csr.PublicKey, f.caKey)
	if err != nil {
		f.problem(w, http.StatusInternalServerError, "serverInternal", err.Error())
		return
	}
	if parsed, err := x509.ParseCertificate(leaf); err == nil {
		if ari, err := certID(parsed); err == nil {
			f.issuedTo[ari] = o.account
		}
	}
	cert := f.id()
	f.certs[cert] = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.caCert.Raw})...)
	o.cert = cert
	o.status = "valid"
	if f.processing > 0 {
		o.status = "processing"
		w.Header().Set("Retry-After", "1")
	}
	f.reply(w, http.StatusOK, f.orderJSON(o))
}

func (f *fakeCA) saw(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, call) {
			n++
		}
	}
	return n
}

// deactivatedNames is what the client gave up, sorted.
func (f *fakeCA) deactivatedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.deactivated)
	slices.Sort(out)
	return out
}
