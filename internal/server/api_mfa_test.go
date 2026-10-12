package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/engine"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
	"ostiole/internal/totp"
	"ostiole/internal/webauthn/webauthntest"
)

// A passkey needs a host name, and the test server answers on an address.
// Requests meant for a passkey arrive at this name instead, which the client
// sends as the Host header and the software authenticator signs for.
const (
	passkeyHost   = "router.test"
	passkeyOrigin = "http://router.test"
)

// factorSite is a server with an audit log and its administrator, "admin",
// signed in. Its client keeps the session cookie itself, because a cookie
// jar files cookies by the Host header and a request for passkeyHost would
// not find the one from the test server's address.
type factorSite struct {
	srv     *httptest.Server
	as      *auth.Service
	log     *audit.Log
	dir     string
	session string
	// agent is the User-Agent the client sends, when not empty.
	agent string
}

func newFactorSite(t *testing.T) *factorSite {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := audit.Open(dir)
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Audit: log}))
	t.Cleanup(srv.Close)
	s := &factorSite{srv: srv, as: as, log: log, dir: dir}
	s.expect(t, http.StatusCreated, "", http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	return s
}

// call sends a request as the site's client, arriving at host: the test
// server's own address when that is empty.
func (s *factorSite) call(t *testing.T, host, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, s.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(RequestHeader, RequestHeaderValue)
	if s.agent != "" {
		req.Header.Set("User-Agent", s.agent)
	}
	if host != "" {
		req.Host = host
	}
	if s.session != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: s.session})
	}
	resp, err := s.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			s.session = c.Value
		}
	}
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// expect is call that fails the test unless the status is the wanted one.
func (s *factorSite) expect(t *testing.T, status int, host, method, path string, body any) []byte {
	t.Helper()
	resp, raw := s.call(t, host, method, path, body)
	if resp.StatusCode != status {
		t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, raw, status)
	}
	return raw
}

// refused checks that a request is turned down with the status and the words.
func (s *factorSite) refused(t *testing.T, status int, message, host, method, path string, body any) {
	t.Helper()
	resp, raw := s.call(t, host, method, path, body)
	if resp.StatusCode != status || errorOf(raw) != message {
		t.Errorf("%s %s: %d %s, want %d %q", method, path, resp.StatusCode, raw, status, message)
	}
}

func errorOf(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.Error
}

func jsonOf[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return v
}

func sessionCookieOf(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func (s *factorSite) signOut(t *testing.T) {
	t.Helper()
	s.expect(t, http.StatusNoContent, "", http.MethodPost, "/api/v1/auth/logout", nil)
}

func (s *factorSite) signIn(t *testing.T, host, user string) (*http.Response, []byte) {
	t.Helper()
	return s.call(t, host, http.MethodPost, "/api/v1/auth/login", credentials{Username: user, Password: testPassword})
}

// stepTwo is what a sign-in that passed the password answers with.
type stepTwo struct {
	Token    string `json:"token"`
	TOTP     bool   `json:"totp"`
	Passkeys bool   `json:"passkeys"`
	RPID     string `json:"rpId"`
}

// asked signs in as admin and gives the second step that is asked for,
// failing if a session came instead.
func (s *factorSite) asked(t *testing.T, host string) stepTwo {
	t.Helper()
	resp, raw := s.signIn(t, host, "admin")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in as admin: %d %s", resp.StatusCode, raw)
	}
	if c := sessionCookieOf(resp); c != nil {
		t.Fatalf("the password alone made a session: %+v", c)
	}
	step := jsonOf[struct {
		Pending *stepTwo `json:"pending"`
	}](t, raw)
	if step.Pending == nil || step.Pending.Token == "" {
		t.Fatalf("no second step in %s", raw)
	}
	return *step.Pending
}

func (s *factorSite) codeSignIn(t *testing.T, pending, code string) (*http.Response, []byte) {
	t.Helper()
	return s.call(t, "", http.MethodPost, "/api/v1/auth/login/totp", map[string]string{"pending": pending, "code": code})
}

// passkeySignIn asks for the challenge of a pending sign-in and answers it as
// a browser at passkeyHost would, claiming origin.
func (s *factorSite) passkeySignIn(t *testing.T, pending, origin string, key *webauthntest.Authenticator) (*http.Response, []byte) {
	t.Helper()
	raw := s.expect(t, http.StatusOK, passkeyHost, http.MethodPost, "/api/v1/auth/login/passkey/options", map[string]string{"pending": pending})
	return s.call(t, passkeyHost, http.MethodPost, "/api/v1/auth/login/passkey", map[string]any{
		"pending": pending, "credential": key.Assert(jsonOf[map[string]any](t, raw), origin),
	})
}

// addPasskey makes a passkey for the signed-in account on passkeyHost.
func (s *factorSite) addPasskey(t *testing.T, name string, key *webauthntest.Authenticator) passkeyView {
	t.Helper()
	raw := s.expect(t, http.StatusOK, passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys/options", withPassword)
	raw = s.expect(t, http.StatusCreated, passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys", map[string]any{
		"name": name, "credential": key.Register(jsonOf[map[string]any](t, raw), passkeyOrigin),
	})
	return jsonOf[passkeyView](t, raw)
}

// withPassword is the body of a request that asks for the account's password.
var withPassword = map[string]string{"password": testPassword}

// enableTOTP gives an account an authenticator app, and returns its secret.
func (s *factorSite) enableTOTP(t *testing.T, user string) string {
	t.Helper()
	secret, err := totp.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.as.EnableTOTP(user, secret, time.Now()); err != nil {
		t.Fatal(err)
	}
	return secret
}

// events lists what was recorded under an action, oldest first.
func (s *factorSite) events(t *testing.T, action string) []audit.Event {
	t.Helper()
	all, err := s.log.Events()
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Event
	for _, e := range all {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// sessionBody is the account as sign-in and /auth/me describe it.
type sessionBody struct {
	Username    string    `json:"username"`
	MFA         bool      `json:"mfa"`
	BypassUntil time.Time `json:"bypassUntil"`
	BypassBy    string    `json:"bypassBy"`
}

// factorState is what /auth/mfa says.
type factorState struct {
	TOTP *struct {
		EnabledAt time.Time `json:"enabledAt"`
		LastUsed  time.Time `json:"lastUsed"`
	} `json:"totp"`
	PasskeyLogin bool `json:"passkeyLogin"`
	Passkeys     []struct {
		ID       string    `json:"id"`
		Name     string    `json:"name"`
		RPID     string    `json:"rpId"`
		LastUsed time.Time `json:"lastUsed"`
	} `json:"passkeys"`
	RPID        string    `json:"rpId"`
	BypassUntil time.Time `json:"bypassUntil"`
	BypassBy    string    `json:"bypassBy"`
}

func (s *factorSite) state(t *testing.T, host string) (factorState, []byte) {
	t.Helper()
	raw := s.expect(t, http.StatusOK, host, http.MethodGet, "/api/v1/auth/mfa", nil)
	return jsonOf[factorState](t, raw), raw
}

// codeAt is the code an authenticator app shows steps 30 second steps from now.
// A test sending a code twice keeps the first: a later call can fall in the next step.
func codeAt(t *testing.T, secret string, steps int64) string {
	t.Helper()
	code, err := totp.Code(secret, totp.Step(time.Now())+steps)
	if err != nil {
		t.Fatal(err)
	}
	return code
}

// wrongCode is a six digit code that is right at none of the steps near now.
func wrongCode(t *testing.T, secret string) string {
	t.Helper()
	right := map[string]bool{}
	for steps := int64(-3); steps <= 3; steps++ {
		right[codeAt(t, secret, steps)] = true
	}
	for _, code := range []string{"000000", "111111"} {
		if !right[code] {
			return code
		}
	}
	t.Fatal("every wrong code was right")
	return ""
}

func TestSignInWithAnAuthenticatorAppTakesTheCode(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	secret := s.enableTOTP(t, "admin")
	s.signOut(t)

	step := s.asked(t, "")
	if !step.TOTP || step.Passkeys || step.RPID != "" {
		t.Errorf("second step = %+v, want the app alone, reached by address", step)
	}
	s.refused(t, http.StatusUnauthorized, "authentication required", "", http.MethodGet, "/api/v1/auth/me", nil)
	if n := len(s.events(t, audit.SignIn)); n != 0 {
		t.Errorf("%d sign-ins recorded before the code", n)
	}
	// By address there is no passkey to ask for.
	s.refused(t, http.StatusBadRequest, errNeedsAName.Error(), "", http.MethodPost, "/api/v1/auth/login/passkey/options", map[string]string{"pending": step.Token})

	resp, raw := s.codeSignIn(t, step.Token, wrongCode(t, secret))
	if resp.StatusCode != http.StatusUnauthorized || errorOf(raw) != "invalid code" || sessionCookieOf(resp) != nil {
		t.Fatalf("wrong code: %d %s", resp.StatusCode, raw)
	}
	code := codeAt(t, secret, 0)
	resp, raw = s.codeSignIn(t, step.Token, code)
	if resp.StatusCode != http.StatusOK || sessionCookieOf(resp) == nil {
		t.Fatalf("right code: %d %s", resp.StatusCode, raw)
	}
	if me := jsonOf[sessionBody](t, raw); me.Username != "admin" || !me.MFA {
		t.Errorf("session = %+v", me)
	}
	s.expect(t, http.StatusOK, "", http.MethodGet, "/api/v1/auth/me", nil)
	if got := s.events(t, audit.SignIn); len(got) != 1 || got[0].By.Name != "admin" || got[0].By.Kind != audit.Account {
		t.Errorf("sign-ins recorded = %+v, want admin's, once", got)
	}
	s.refused(t, http.StatusUnauthorized, "authentication required", "", http.MethodPost, "/api/v1/auth/login/totp",
		map[string]string{"pending": step.Token, "code": code})

	// The code that signed in is spent for the rest of its half minute.
	s.signOut(t)
	again := s.asked(t, "")
	resp, raw = s.codeSignIn(t, again.Token, code)
	if resp.StatusCode != http.StatusUnauthorized || errorOf(raw) != auth.ErrCodeUsed.Error() || sessionCookieOf(resp) != nil {
		t.Errorf("a code used twice: %d %s", resp.StatusCode, raw)
	}
}

func TestFiveWrongCodesEndTheSignIn(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	secret := s.enableTOTP(t, "admin")
	s.signOut(t)
	step := s.asked(t, "")

	for i := range auth.MaxFailures {
		resp, raw := s.codeSignIn(t, step.Token, wrongCode(t, secret))
		if resp.StatusCode != http.StatusUnauthorized || errorOf(raw) != "invalid code" {
			t.Fatalf("wrong code %d: %d %s", i+1, resp.StatusCode, raw)
		}
	}
	// The right code is too late, and the password is turned away from the
	// address that guessed.
	s.refused(t, http.StatusUnauthorized, "authentication required", "", http.MethodPost, "/api/v1/auth/login/totp",
		map[string]string{"pending": step.Token, "code": codeAt(t, secret, 0)})
	if resp, raw := s.signIn(t, "", "admin"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("password after five wrong codes: %d %s, want 429", resp.StatusCode, raw)
	}
	if n := len(s.events(t, audit.SignIn)); n != 0 {
		t.Errorf("%d sign-ins recorded", n)
	}
}

func TestABypassSkipsTheSecondStep(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	s.signOut(t)

	until := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	if err := auth.WriteBypass(s.dir, auth.Bypass{Until: until, By: "root"}); err != nil {
		t.Fatal(err)
	}
	resp, raw := s.signIn(t, "", "admin")
	if resp.StatusCode != http.StatusOK || sessionCookieOf(resp) == nil {
		t.Fatalf("sign-in during a bypass: %d %s", resp.StatusCode, raw)
	}
	if me := jsonOf[sessionBody](t, raw); !me.MFA || !me.BypassUntil.Equal(until) || me.BypassBy != "root" {
		t.Errorf("session during a bypass = %+v", me)
	}
	if state, _ := s.state(t, ""); state.TOTP == nil || !state.BypassUntil.Equal(until) || state.BypassBy != "root" {
		t.Errorf("MFA during a bypass = %+v", state)
	}
	s.signOut(t)

	if err := auth.ClearBypass(s.dir); err != nil {
		t.Fatal(err)
	}
	s.asked(t, "")
	if err := auth.WriteBypass(s.dir, auth.Bypass{Until: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	s.asked(t, "")
}

func TestSettingUpAnAuthenticatorApp(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)

	made := jsonOf[struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}](t, s.expect(t, http.StatusOK, "", http.MethodPost, "/api/v1/auth/mfa/totp/start", nil))
	uri, err := url.Parse(made.URI)
	if err != nil {
		t.Fatal(err)
	}
	q := uri.Query()
	if uri.Scheme != "otpauth" || uri.Host != "totp" || uri.Path != "/Ostiole:admin" ||
		q.Get("secret") != made.Secret || q.Get("issuer") != "Ostiole" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Errorf("uri = %s", made.URI)
	}
	if state, _ := s.state(t, ""); state.TOTP != nil || s.as.HasMFA("admin") {
		t.Fatal("the app was on before a code was confirmed")
	}

	// A typo costs nothing: the secret waits for the right code.
	s.refused(t, http.StatusBadRequest, "invalid code", "", http.MethodPost, "/api/v1/auth/mfa/totp/confirm", map[string]string{"code": wrongCode(t, made.Secret)})
	if s.as.HasMFA("admin") {
		t.Fatal("a wrong code turned the app on")
	}
	setup := codeAt(t, made.Secret, 0)
	s.expect(t, http.StatusNoContent, "", http.MethodPost, "/api/v1/auth/mfa/totp/confirm", map[string]string{"code": setup})
	state, raw := s.state(t, "")
	if state.TOTP == nil || len(state.Passkeys) != 0 {
		t.Errorf("MFA = %+v", state)
	}
	if strings.Contains(string(raw), made.Secret) {
		t.Errorf("the secret is in the account's page: %s", raw)
	}
	if got := s.events(t, audit.MFATOTP); len(got) != 1 || got[0].By.Name != "admin" {
		t.Errorf("recorded %+v, want the app set up once by admin", got)
	}
	s.refused(t, http.StatusBadRequest, "the setup timed out; start it again", "", http.MethodPost, "/api/v1/auth/mfa/totp/confirm",
		map[string]string{"code": setup})

	// The code that proved the setup is spent, the next one is not.
	s.signOut(t)
	step := s.asked(t, "")
	if resp, raw := s.codeSignIn(t, step.Token, setup); resp.StatusCode != http.StatusUnauthorized || errorOf(raw) != auth.ErrCodeUsed.Error() {
		t.Errorf("the setup's code signed in: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := s.codeSignIn(t, step.Token, codeAt(t, made.Secret, 1)); resp.StatusCode != http.StatusOK {
		t.Errorf("the next code: %d %s", resp.StatusCode, raw)
	}
}

func TestAPasskeyNeedsTheAppAndAName(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.refused(t, http.StatusBadRequest, auth.ErrTOTPFirst.Error(), passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys/options", withPassword)

	s.enableTOTP(t, "admin")
	s.refused(t, http.StatusBadRequest, errNeedsAName.Error(), "", http.MethodPost, "/api/v1/auth/mfa/passkeys/options", withPassword)
	options := jsonOf[struct {
		PublicKey struct {
			RP struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"rp"`
			User struct {
				Name string `json:"name"`
			} `json:"user"`
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}](t, s.expect(t, http.StatusOK, passkeyHost+":8443", http.MethodPost, "/api/v1/auth/mfa/passkeys/options", withPassword)).PublicKey
	if options.RP.ID != passkeyHost || options.RP.Name != "Ostiole" || options.User.Name != "admin" || options.Challenge == "" {
		t.Errorf("creation options = %+v", options)
	}

	// The name is looked at before the browser's answer is.
	for name, message := range map[string]string{
		"":                      "the name must be 1 to 40 characters",
		"   ":                   "the name must be 1 to 40 characters",
		strings.Repeat("a", 41): "the name must be 1 to 40 characters",
		"laptop\x1b[2J":         "the name may not contain control characters",
		"tab\there":             "the name may not contain control characters",
		strings.Repeat("é", 41): "the name must be 1 to 40 characters",
	} {
		s.refused(t, http.StatusBadRequest, message, passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys",
			map[string]any{"name": name, "credential": map[string]any{}})
	}
}

func TestAPasskeyCannotBeAddedOnceTheAppIsGone(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	options := s.expect(t, http.StatusOK, passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys/options", withPassword)
	if err := s.as.ClearMFA("admin"); err != nil {
		t.Fatal(err)
	}
	s.refused(t, http.StatusBadRequest, auth.ErrTOTPFirst.Error(), passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys",
		map[string]any{"name": "Work laptop", "credential": webauthntest.New("es256").Register(jsonOf[map[string]any](t, options), passkeyOrigin)})
	if s.as.HasMFA("admin") {
		t.Error("a passkey was kept for an account with no authenticator app")
	}
}

func TestAddingAPasskeyAndSigningInWithIt(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	key := webauthntest.New("es256")

	added := s.addPasskey(t, "Work laptop", key)
	if added.Name != "Work laptop" || added.RPID != passkeyHost || added.ID != b64url(key.CredentialID) {
		t.Errorf("added passkey = %+v", added)
	}
	state, raw := s.state(t, passkeyHost)
	if state.TOTP == nil || len(state.Passkeys) != 1 || state.Passkeys[0].ID != added.ID || state.RPID != passkeyHost {
		t.Errorf("MFA = %+v", state)
	}
	if strings.Contains(string(raw), "publicKey") || strings.Contains(string(raw), "signCount") {
		t.Errorf("the account's page carries the passkey's key: %s", raw)
	}
	if got := s.events(t, audit.PasskeyAdd); len(got) != 1 || got[0].Target != "Work laptop" || got[0].By.Name != "admin" {
		t.Errorf("recorded %+v, want the passkey added once by admin", got)
	}
	// The same device cannot be added twice.
	options := s.expect(t, http.StatusOK, passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys/options", withPassword)
	s.refused(t, http.StatusBadRequest, "that passkey is already registered", passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys",
		map[string]any{"name": "Again", "credential": key.Register(jsonOf[map[string]any](t, options), passkeyOrigin)})

	s.signOut(t)
	step := s.asked(t, passkeyHost)
	if !step.TOTP || !step.Passkeys || step.RPID != passkeyHost {
		t.Errorf("second step by name = %+v", step)
	}
	byAddress := s.asked(t, "")
	if !byAddress.TOTP || byAddress.Passkeys || byAddress.RPID != "" {
		t.Errorf("second step by address = %+v", byAddress)
	}

	key.SignCount = 3
	resp, raw := s.passkeySignIn(t, step.Token, passkeyOrigin, key)
	if resp.StatusCode != http.StatusOK || sessionCookieOf(resp) == nil {
		t.Fatalf("sign-in with the passkey: %d %s", resp.StatusCode, raw)
	}
	if me := jsonOf[sessionBody](t, raw); me.Username != "admin" || !me.MFA {
		t.Errorf("session = %+v", me)
	}
	if got := s.events(t, audit.SignIn); len(got) != 1 {
		t.Errorf("sign-ins recorded = %+v, want one", got)
	}
	if state, _ := s.state(t, passkeyHost); state.Passkeys[0].LastUsed.IsZero() {
		t.Error("the passkey does not say it was used")
	}
	s.refused(t, http.StatusUnauthorized, "authentication required", passkeyHost, http.MethodPost, "/api/v1/auth/login/passkey/options",
		map[string]string{"pending": step.Token})
}

func TestAPasskeySignInThatDoesNotCheckOut(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	key := webauthntest.New("es256")
	s.addPasskey(t, "Phone", key)
	s.signOut(t)

	first := s.asked(t, passkeyHost)
	// Signed for another site, or made by a device the account never added.
	resp, raw := s.passkeySignIn(t, first.Token, "http://elsewhere.test", key)
	if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(errorOf(raw), "passkey refused") || sessionCookieOf(resp) != nil {
		t.Errorf("a passkey signed for another origin: %d %s", resp.StatusCode, raw)
	}
	resp, raw = s.passkeySignIn(t, first.Token, passkeyOrigin, webauthntest.New("es256"))
	if resp.StatusCode != http.StatusUnauthorized || errorOf(raw) != auth.ErrNoSuchPasskey.Error() {
		t.Errorf("a passkey the account does not have: %d %s", resp.StatusCode, raw)
	}
	key.SignCount = 4
	if resp, raw = s.passkeySignIn(t, first.Token, passkeyOrigin, key); resp.StatusCode != http.StatusOK {
		t.Fatalf("the real passkey, after those: %d %s", resp.StatusCode, raw)
	}

	// A device whose counter has not moved is a copy of one that has.
	s.signOut(t)
	second := s.asked(t, passkeyHost)
	resp, raw = s.passkeySignIn(t, second.Token, passkeyOrigin, key)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(errorOf(raw), "counter") {
		t.Errorf("a passkey whose counter stood still: %d %s", resp.StatusCode, raw)
	}
	key.SignCount = 5
	if resp, raw = s.passkeySignIn(t, second.Token, passkeyOrigin, key); resp.StatusCode != http.StatusOK {
		t.Errorf("the counter moving on: %d %s", resp.StatusCode, raw)
	}
}

func TestFiveBadPasskeysEndTheSignIn(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	key := webauthntest.New("es256")
	s.addPasskey(t, "Work laptop", key)
	s.signOut(t)
	step := s.asked(t, passkeyHost)

	for i := range auth.MaxFailures {
		resp, raw := s.passkeySignIn(t, step.Token, "http://elsewhere.test", key)
		if resp.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(errorOf(raw), "passkey refused") {
			t.Fatalf("bad passkey %d: %d %s", i+1, resp.StatusCode, raw)
		}
	}
	s.refused(t, http.StatusUnauthorized, "authentication required", passkeyHost, http.MethodPost, "/api/v1/auth/login/passkey/options",
		map[string]string{"pending": step.Token})
}

func TestRemovingAPasskey(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	added := s.addPasskey(t, "Work laptop", webauthntest.New("es256"))

	s.expect(t, http.StatusNoContent, "", http.MethodDelete, "/api/v1/auth/mfa/passkeys/"+added.ID, nil)
	if state, _ := s.state(t, passkeyHost); state.TOTP == nil || len(state.Passkeys) != 0 {
		t.Errorf("MFA after the removal = %+v, want the app alone", state)
	}
	if got := s.events(t, audit.PasskeyRemove); len(got) != 1 || got[0].Target != "Work laptop" {
		t.Errorf("recorded %+v, want the removal of Work laptop", got)
	}
	s.refused(t, http.StatusNotFound, auth.ErrNoSuchPasskey.Error(), "", http.MethodDelete, "/api/v1/auth/mfa/passkeys/"+added.ID, nil)

	s.signOut(t)
	if step := s.asked(t, passkeyHost); step.Passkeys {
		t.Errorf("a removed passkey is still offered: %+v", step)
	}
}

func TestAnAdminClearsAnotherAccountsMFA(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	if err := s.as.CreateUser("watcher", testPassword, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	secret := s.enableTOTP(t, "watcher")
	s.enableTOTP(t, "admin")
	key := auth.Passkey{ID: "d2F0Y2hlcg", Name: "Pocket key", PublicKey: "cHVibGlj", RPID: passkeyHost}
	if err := s.as.AddPasskey("watcher", key); err != nil {
		t.Fatal(err)
	}
	if err := s.as.SetPasskeyLogin("watcher", true); err != nil {
		t.Fatal(err)
	}

	type mfaRow struct {
		TOTP         bool `json:"totp"`
		Passkeys     int  `json:"passkeys"`
		PasskeyLogin bool `json:"passkeyLogin"`
	}
	rows := func(raw []byte) map[string]mfaRow {
		out := map[string]mfaRow{}
		for _, r := range jsonOf[[]struct {
			Username string `json:"username"`
			MFA      mfaRow `json:"mfa"`
		}](t, raw) {
			out[r.Username] = r.MFA
		}
		return out
	}
	raw := s.expect(t, http.StatusOK, "", http.MethodGet, "/api/v1/users", nil)
	if got, want := rows(raw), (map[string]mfaRow{"admin": {TOTP: true}, "watcher": {TOTP: true, Passkeys: 1, PasskeyLogin: true}}); !maps.Equal(got, want) {
		t.Errorf("MFA in the account list = %+v, want %+v", got, want)
	}
	for _, leak := range []string{secret, key.ID, key.PublicKey, key.Name, "hash"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the account list carries %q: %s", leak, raw)
		}
	}

	// Your own goes through the password.
	resp, body := s.call(t, "", http.MethodDelete, "/api/v1/users/admin/mfa", nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(errorOf(body), "password") || !s.as.HasMFA("admin") {
		t.Errorf("clearing your own: %d %s", resp.StatusCode, body)
	}

	raw = s.expect(t, http.StatusOK, "", http.MethodDelete, "/api/v1/users/watcher/mfa", nil)
	if got, want := rows(raw), (map[string]mfaRow{"admin": {TOTP: true}, "watcher": {}}); !maps.Equal(got, want) {
		t.Errorf("MFA in the account list after the clear = %+v, want %+v", got, want)
	}
	if s.as.HasMFA("watcher") {
		t.Error("the account kept its MFA")
	}
	cleared := s.events(t, audit.AccountMFAClear)
	if len(cleared) != 1 || cleared[0].Target != "watcher" || cleared[0].By.Name != "admin" {
		t.Errorf("recorded %+v, want watcher's cleared by admin", cleared)
	}
	page := jsonOf[struct {
		Entries []struct {
			Action string `json:"action"`
			Text   string `json:"text"`
		} `json:"entries"`
	}](t, s.expect(t, http.StatusOK, "", http.MethodGet, "/api/v1/audit", nil))
	var text string
	for _, e := range page.Entries {
		if e.Action == audit.AccountMFAClear {
			text = e.Text
		}
	}
	if text != "Turned off multi-factor authentication for watcher" {
		t.Errorf("the audit page says %q", text)
	}

	// Nothing left to clear is not worth a line in the log.
	s.expect(t, http.StatusOK, "", http.MethodDelete, "/api/v1/users/watcher/mfa", nil)
	if n := len(s.events(t, audit.AccountMFAClear)); n != 1 {
		t.Errorf("%d clears recorded, want one", n)
	}
	if resp, body := s.call(t, "", http.MethodDelete, "/api/v1/users/ghost/mfa", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("clearing an account that is not there: %d %s", resp.StatusCode, body)
	}

	s.signOut(t)
	if resp, body := s.signIn(t, "", "watcher"); resp.StatusCode != http.StatusOK || sessionCookieOf(resp) == nil {
		t.Errorf("sign-in after the clear: %d %s", resp.StatusCode, body)
	}
}

func TestTurningMFAOffAsksForThePassword(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")

	s.refused(t, http.StatusUnauthorized, auth.ErrInvalidCredentials.Error(), "", http.MethodPost, "/api/v1/auth/mfa/clear",
		map[string]string{"password": "not the password"})
	if !s.as.HasMFA("admin") {
		t.Fatal("a wrong password turned MFA off")
	}
	s.expect(t, http.StatusNoContent, "", http.MethodPost, "/api/v1/auth/mfa/clear", map[string]string{"password": testPassword})
	if s.as.HasMFA("admin") {
		t.Error("MFA is still on")
	}
	if got := s.events(t, audit.MFAClear); len(got) != 1 || got[0].By.Name != "admin" {
		t.Errorf("recorded %+v, want admin's turned off once", got)
	}

	s.signOut(t)
	resp, raw := s.signIn(t, "", "admin")
	if resp.StatusCode != http.StatusOK || sessionCookieOf(resp) == nil || jsonOf[sessionBody](t, raw).MFA {
		t.Errorf("sign-in after turning it off: %d %s", resp.StatusCode, raw)
	}
}

func TestADeadSignInIsRefusedAtEveryStep(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	for path, body := range map[string]any{
		"/api/v1/auth/login/totp":            map[string]string{"pending": "gone", "code": "123456"},
		"/api/v1/auth/login/passkey/options": map[string]string{"pending": "gone"},
		"/api/v1/auth/login/passkey":         map[string]any{"pending": "gone", "credential": map[string]any{}},
	} {
		s.refused(t, http.StatusUnauthorized, "authentication required", passkeyHost, http.MethodPost, path, body)
	}
}

func TestPasskeysAreBoundToTheHostName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		host, rpID, origin string
		tls                bool
	}{
		{"router.test", "router.test", "http://router.test", false},
		{"router.test:8443", "router.test", "http://router.test:8443", false},
		{"Router.Test", "router.test", "http://router.test", false},
		{"router.test", "router.test", "https://router.test", true},
		{"192.0.2.1", "", "http://192.0.2.1", false},
		{"192.0.2.1:8443", "", "http://192.0.2.1:8443", false},
		{"[2001:db8::1]:8443", "", "http://[2001:db8::1]:8443", false},
		{"[2001:db8::1]", "", "http://[2001:db8::1]", false},
	} {
		r := &http.Request{Host: tc.host}
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := relyingParty(r).ID; got != tc.rpID {
			t.Errorf("host %q: relying party %q, want %q", tc.host, got, tc.rpID)
		}
		if got := originOf(r); got != tc.origin {
			t.Errorf("host %q: origin %q, want %q", tc.host, got, tc.origin)
		}
	}
}

func TestTheMFAEntriesReadAsSentences(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		event audit.Event
		want  string
	}{
		{audit.Event{Action: audit.MFATOTP}, "Set up an authenticator app"},
		{audit.Event{Action: audit.MFAClear}, "Turned off multi-factor authentication"},
		{audit.Event{Action: audit.PasskeyAdd, Target: "Work laptop"}, "Added the passkey Work laptop"},
		{audit.Event{Action: audit.PasskeyRemove, Target: "Work laptop"}, "Removed the passkey Work laptop"},
		{audit.Event{Action: audit.PasskeyRename, Target: "Work laptop", Detail: "Desk key"}, "Renamed the passkey Work laptop to Desk key"},
		{audit.Event{Action: audit.MFAPasskeyLogin, Detail: "on"}, "Turned on passkey sign-in"},
		{audit.Event{Action: audit.MFAPasskeyLogin, Detail: "off"}, "Turned off passkey sign-in"},
		{audit.Event{Action: audit.SignIn, Detail: "passkey"}, "Signed in with a passkey"},
		{audit.Event{Action: audit.SignIn}, "Signed in"},
		{audit.Event{Action: audit.AccountMFAClear, Target: "watcher"}, "Turned off multi-factor authentication for watcher"},
		{audit.Event{Action: audit.MFABypass, Detail: "15m0s"}, "Bypassed MFA for 15 minutes"},
		{audit.Event{Action: audit.MFABypass, Detail: "3600"}, "Bypassed MFA for 1 hour"},
		{audit.Event{Action: audit.MFABypass, Detail: "45s"}, "Bypassed MFA for 45 seconds"},
		{audit.Event{Action: audit.MFABypass}, "Bypassed MFA"},
		{audit.Event{Action: audit.MFABypassEnd}, "Ended the MFA bypass"},
	} {
		if got := tc.event.Sentence(); got != tc.want {
			t.Errorf("%s reads %q, want %q", tc.event.Action, got, tc.want)
		}
	}
}

// passkeyAlone is what login/start offers: a token and the request options.
type passkeyAlone struct {
	Token     string         `json:"token"`
	PublicKey map[string]any `json:"publicKey"`
}

// start asks login/start about admin at passkeyHost and gives what it
// offers, nil for none.
func (s *factorSite) start(t *testing.T) *passkeyAlone {
	t.Helper()
	raw := s.expect(t, http.StatusOK, passkeyHost, http.MethodPost, "/api/v1/auth/login/start", map[string]string{"username": "admin"})
	return jsonOf[struct {
		Passkey *passkeyAlone `json:"passkey"`
	}](t, raw).Passkey
}

// passkeyAloneSignIn answers what login/start offered as a browser at
// passkeyHost would.
func (s *factorSite) passkeyAloneSignIn(t *testing.T, offer *passkeyAlone, key *webauthntest.Authenticator) (*http.Response, []byte) {
	t.Helper()
	return s.call(t, passkeyHost, http.MethodPost, "/api/v1/auth/login/passkey", map[string]any{
		"pending": offer.Token, "credential": key.Assert(map[string]any{"publicKey": offer.PublicKey}, passkeyOrigin),
	})
}

// passkeyLogin turns admin's passkey sign-in on, on the password.
func (s *factorSite) passkeyLogin(t *testing.T) factorState {
	t.Helper()
	raw := s.expect(t, http.StatusOK, passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkey-login", map[string]any{"on": true, "password": testPassword})
	return jsonOf[factorState](t, raw)
}

func TestSigningInWithAPasskeyAlone(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	secret := s.enableTOTP(t, "admin")
	key := webauthntest.New("es256")
	added := s.addPasskey(t, "Work laptop", key)
	if state := s.passkeyLogin(t); !state.PasskeyLogin || state.TOTP == nil || len(state.Passkeys) != 1 {
		t.Errorf("MFA after turning passkey sign-in on = %+v", state)
	}
	if got := s.events(t, audit.MFAPasskeyLogin); len(got) != 1 || got[0].Detail != "on" || got[0].By.Name != "admin" {
		t.Errorf("recorded %+v, want passkey sign-in turned on by admin", got)
	}
	s.signOut(t)

	offer := s.start(t)
	if offer == nil || offer.Token == "" {
		t.Fatal("no passkey offered")
	}
	if uv := offer.PublicKey["userVerification"]; uv != "required" {
		t.Errorf("userVerification = %v, want required", uv)
	}
	if allow, _ := offer.PublicKey["allowCredentials"].([]any); len(allow) != 1 {
		t.Errorf("allowCredentials = %v, want the one passkey", offer.PublicKey["allowCredentials"])
	}
	if n := len(s.events(t, audit.SignIn)); n != 0 {
		t.Errorf("%d sign-ins recorded before the passkey answered", n)
	}

	key.SignCount = 1
	resp, raw := s.passkeyAloneSignIn(t, offer, key)
	if resp.StatusCode != http.StatusOK || sessionCookieOf(resp) == nil {
		t.Fatalf("sign-in with the passkey alone: %d %s", resp.StatusCode, raw)
	}
	if me := jsonOf[sessionBody](t, raw); me.Username != "admin" {
		t.Errorf("session = %+v", me)
	}
	s.expect(t, http.StatusOK, "", http.MethodGet, "/api/v1/auth/me", nil)
	if got := s.events(t, audit.SignIn); len(got) != 1 || got[0].Detail != "passkey" || got[0].By.Name != "admin" {
		t.Errorf("sign-ins recorded = %+v, want admin's with a passkey", got)
	}
	if state, _ := s.state(t, passkeyHost); state.Passkeys[0].ID != added.ID || state.Passkeys[0].LastUsed.IsZero() {
		t.Errorf("the passkey does not say it was used: %+v", state.Passkeys)
	}

	// A token that has not passed the password finishes with a passkey or not at all.
	s.signOut(t)
	other := s.start(t)
	s.refused(t, http.StatusUnauthorized, "authentication required", "", http.MethodPost, "/api/v1/auth/login/totp",
		map[string]string{"pending": other.Token, "code": codeAt(t, secret, 0)})
	s.refused(t, http.StatusUnauthorized, "authentication required", passkeyHost, http.MethodPost, "/api/v1/auth/login/passkey/options",
		map[string]string{"pending": other.Token})
	if n := len(s.events(t, audit.SignIn)); n != 1 {
		t.Errorf("%d sign-ins recorded, want the one", n)
	}
}

func TestLoginStartOffersNothingOtherwise(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	s.addPasskey(t, "Work laptop", webauthntest.New("es256"))
	if err := s.as.CreateUser("watcher", testPassword, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}

	// With the switch off, and for every other reason, the answer reads alike.
	nothing := func(why, host, user string) {
		t.Helper()
		raw := s.expect(t, http.StatusOK, host, http.MethodPost, "/api/v1/auth/login/start", map[string]string{"username": user})
		if got := strings.TrimSpace(string(raw)); got != `{"passkey":null}` {
			t.Errorf("%s: %s", why, got)
		}
	}
	nothing("switch off", passkeyHost, "admin")
	s.passkeyLogin(t)
	if s.start(t) == nil {
		t.Fatal("no passkey offered with the switch on")
	}
	nothing("unknown name", passkeyHost, "ghost")
	nothing("no passkey", passkeyHost, "watcher")
	nothing("by address", "", "admin")
	nothing("by another address", "192.0.2.1", "admin")
	nothing("on another host name", "elsewhere.test", "admin")

	// Asking is not counted against the address.
	for range 2 * auth.MaxFailures {
		nothing("unknown name, again", passkeyHost, "ghost")
	}
	if resp, raw := s.signIn(t, "", "watcher"); resp.StatusCode != http.StatusOK {
		t.Errorf("sign-in after many starts: %d %s", resp.StatusCode, raw)
	}
	if got := s.events(t, audit.SignIn); len(got) != 1 {
		t.Errorf("sign-ins recorded = %+v, want watcher's alone", got)
	}

	// An address shut out by the limiter is told so.
	for range auth.MaxFailures {
		s.call(t, "", http.MethodPost, "/api/v1/auth/login", credentials{Username: "watcher", Password: "not the password"})
	}
	s.refused(t, http.StatusTooManyRequests, auth.ErrRateLimited.Error(), passkeyHost, http.MethodPost, "/api/v1/auth/login/start",
		map[string]string{"username": "admin"})
}

func TestAPasskeyAloneNeedsTheDeviceToCheckTheUser(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	key := webauthntest.New("es256")
	s.addPasskey(t, "Work laptop", key)
	s.passkeyLogin(t)
	s.signOut(t)

	// After the password a touch is enough.
	key.UserVerified = false
	key.SignCount = 1
	if resp, raw := s.passkeySignIn(t, s.asked(t, passkeyHost).Token, passkeyOrigin, key); resp.StatusCode != http.StatusOK {
		t.Fatalf("second step without verification: %d %s", resp.StatusCode, raw)
	}
	s.signOut(t)

	for i := range auth.MaxFailures {
		key.SignCount++
		resp, raw := s.passkeyAloneSignIn(t, s.start(t), key)
		if resp.StatusCode != http.StatusUnauthorized || errorOf(raw) != "passkey refused: "+errNotVerified.Error() || sessionCookieOf(resp) != nil {
			t.Fatalf("passkey alone without verification %d: %d %s", i+1, resp.StatusCode, raw)
		}
	}
	if n := len(s.events(t, audit.SignIn)); n != 1 {
		t.Errorf("%d sign-ins recorded, want the one after the password", n)
	}
	s.refused(t, http.StatusTooManyRequests, auth.ErrRateLimited.Error(), passkeyHost, http.MethodPost, "/api/v1/auth/login/start",
		map[string]string{"username": "admin"})
}

func TestAPasskeyAloneStopsWhenTheSwitchGoesOff(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	key := webauthntest.New("es256")
	s.addPasskey(t, "Work laptop", key)
	s.passkeyLogin(t)
	offer := s.start(t)
	if err := s.as.SetPasskeyLogin("admin", false); err != nil {
		t.Fatal(err)
	}
	s.session = ""
	key.SignCount = 1
	resp, raw := s.passkeyAloneSignIn(t, offer, key)
	if resp.StatusCode != http.StatusUnauthorized || errorOf(raw) != errPasskeyOff.Error() || sessionCookieOf(resp) != nil {
		t.Errorf("passkey alone after the switch went off: %d %s", resp.StatusCode, raw)
	}
}

func TestAddingAPasskeyAsksForThePassword(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	for _, body := range []any{map[string]string{"password": "not the password"}, map[string]string{}} {
		s.refused(t, http.StatusUnauthorized, errWrongPassword.Error(), passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys/options", body)
	}
	// No challenge was handed out, so nothing can be added.
	s.refused(t, http.StatusBadRequest, "that passkey request timed out; try again", passkeyHost, http.MethodPost, "/api/v1/auth/mfa/passkeys",
		map[string]any{"name": "Work laptop", "credential": map[string]any{}})
	if _, keys, _ := s.as.MFAOf("admin"); len(keys) != 0 {
		t.Errorf("passkeys = %+v, want none", keys)
	}
	s.addPasskey(t, "Work laptop", webauthntest.New("es256"))
}

func TestRenamingAPasskey(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	added := s.addPasskey(t, "Work laptop", webauthntest.New("es256"))
	path := "/api/v1/auth/mfa/passkeys/" + added.ID

	renamed := jsonOf[passkeyView](t, s.expect(t, http.StatusOK, "", http.MethodPatch, path, map[string]string{"name": "  Desk key "}))
	if renamed.ID != added.ID || renamed.Name != "Desk key" || renamed.RPID != passkeyHost {
		t.Errorf("renamed passkey = %+v", renamed)
	}
	if state, _ := s.state(t, passkeyHost); len(state.Passkeys) != 1 || state.Passkeys[0].Name != "Desk key" {
		t.Errorf("passkeys after the rename = %+v", state.Passkeys)
	}
	got := s.events(t, audit.PasskeyRename)
	if len(got) != 1 || got[0].Target != "Work laptop" || got[0].Detail != "Desk key" || got[0].By.Name != "admin" {
		t.Errorf("recorded %+v, want Work laptop renamed Desk key by admin", got)
	}

	// The same name again changes nothing and is not recorded.
	s.expect(t, http.StatusOK, "", http.MethodPatch, path, map[string]string{"name": "Desk key"})
	if n := len(s.events(t, audit.PasskeyRename)); n != 1 {
		t.Errorf("%d renames recorded, want one", n)
	}
	s.refused(t, http.StatusBadRequest, "the name must be 1 to 40 characters", "", http.MethodPatch, path, map[string]string{"name": " "})
	s.refused(t, http.StatusBadRequest, "the name may not contain control characters", "", http.MethodPatch, path, map[string]string{"name": "desk\x07"})
	s.refused(t, http.StatusNotFound, auth.ErrNoSuchPasskey.Error(), "", http.MethodPatch, "/api/v1/auth/mfa/passkeys/bm9uZQ", map[string]string{"name": "Spare"})
}

func TestThePasskeySignInSwitch(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.enableTOTP(t, "admin")
	const path = "/api/v1/auth/mfa/passkey-login"

	// Turning it on needs a passkey, after the password.
	s.refused(t, http.StatusBadRequest, auth.ErrNoPasskeys.Error(), passkeyHost, http.MethodPost, path, map[string]any{"on": true, "password": testPassword})
	s.addPasskey(t, "Work laptop", webauthntest.New("es256"))
	s.refused(t, http.StatusUnauthorized, errWrongPassword.Error(), passkeyHost, http.MethodPost, path, map[string]any{"on": true})
	s.refused(t, http.StatusUnauthorized, errWrongPassword.Error(), passkeyHost, http.MethodPost, path, map[string]any{"on": true, "password": "not the password"})
	if _, _, on := s.as.MFAOf("admin"); on {
		t.Fatal("turned on without the password")
	}
	s.passkeyLogin(t)

	// Off needs nothing.
	state := jsonOf[factorState](t, s.expect(t, http.StatusOK, passkeyHost, http.MethodPost, path, map[string]any{"on": false}))
	if state.PasskeyLogin || state.TOTP == nil || len(state.Passkeys) != 1 || state.RPID != passkeyHost {
		t.Errorf("MFA after turning it off = %+v", state)
	}
	s.expect(t, http.StatusOK, passkeyHost, http.MethodPost, path, map[string]any{"on": false})
	var details []string
	for _, e := range s.events(t, audit.MFAPasskeyLogin) {
		details = append(details, e.Detail)
	}
	if strings.Join(details, " ") != "on off" {
		t.Errorf("recorded %q, want on then off", details)
	}

	// Removing the authenticator app turns it off with the rest.
	s.passkeyLogin(t)
	s.expect(t, http.StatusNoContent, "", http.MethodPost, "/api/v1/auth/mfa/clear", withPassword)
	if state, _ := s.state(t, passkeyHost); state.PasskeyLogin || state.TOTP != nil || len(state.Passkeys) != 0 {
		t.Errorf("MFA after the clear = %+v", state)
	}
	s.signOut(t)
	if offer := s.start(t); offer != nil {
		t.Errorf("a passkey is offered after the clear: %+v", offer)
	}
}

func TestTheAppSaysWhenItWasSetUpAndLastUsed(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	if _, raw := s.state(t, ""); !strings.Contains(string(raw), `"totp":null`) {
		t.Errorf("no app reads %s", raw)
	}
	s.enableTOTP(t, "admin")
	state, raw := s.state(t, "")
	if state.TOTP == nil || state.TOTP.EnabledAt.IsZero() || strings.Contains(string(raw), "lastUsed") {
		t.Errorf("an app never used reads %s", raw)
	}
	step := totp.Step(time.Now())
	if err := s.as.UseTOTPStep("admin", step); err != nil {
		t.Fatal(err)
	}
	if state, _ := s.state(t, ""); state.TOTP == nil || !state.TOTP.LastUsed.Equal(time.Unix(step*30, 0)) {
		t.Errorf("an app used at step %d reads %+v", step, state.TOTP)
	}
}
