package server

import (
	"cmp"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/logging"
	"ostiole/internal/totp"
	"ostiole/internal/webauthn"
)

// SessionCookie is the session cookie name.
const SessionCookie = "ostiole_session"

func (a *api) registerAuth(mux *router) {
	mux.HandleFunc("GET /api/v1/setup", a.public(a.setupStatus))
	mux.HandleFunc("POST /api/v1/setup", a.public(a.setup))
	mux.HandleFunc("GET /api/v1/auth/login", a.public(a.loginPage))
	mux.HandleFunc("POST /api/v1/auth/login", a.public(a.login))
	mux.HandleFunc("POST /api/v1/auth/login/totp", a.public(a.loginTOTP))
	mux.HandleFunc("POST /api/v1/auth/login/passkey/options", a.public(a.loginPasskeyOptions))
	mux.HandleFunc("POST /api/v1/auth/login/passkey", a.public(a.loginPasskey))
	mux.HandleFunc("POST /api/v1/auth/logout", a.protect(a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.protect(a.me))
	mux.HandleFunc("POST /api/v1/auth/password", a.protect(a.changePassword))
	mux.HandleFunc("GET /api/v1/auth/second-factor", a.protect(a.secondFactor))
	mux.HandleFunc("POST /api/v1/auth/second-factor/clear", a.protect(a.clearSecondFactor))
	mux.HandleFunc("POST /api/v1/auth/totp/start", a.protect(a.totpStart))
	mux.HandleFunc("POST /api/v1/auth/totp/confirm", a.protect(a.totpConfirm))
	mux.HandleFunc("POST /api/v1/auth/passkeys/options", a.protect(a.passkeyOptions))
	mux.HandleFunc("POST /api/v1/auth/passkeys", a.protect(a.addPasskey))
	mux.HandleFunc("DELETE /api/v1/auth/passkeys/{id}", a.protect(a.removePasskey))
}

// session resolves the request's session cookie.
func (a *api) session(r *http.Request) (*auth.Session, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, false
	}
	return a.auth.Session(c.Value)
}

// Secure follows the connection: the UI is served over TLS in production,
// while plain HTTP on localhost is allowed for development.
func setSessionCookie(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is set whenever the request arrived over TLS
		Name:     SessionCookie,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(time.Until(sess.Expires).Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // see setSessionCookie
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// sessionResponse describes the caller's session and the role it acts
// with, which the UI reads to grey out what the account may not change.
type sessionResponse struct {
	Username string    `json:"username"`
	Role     auth.Role `json:"role"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
	// SecondFactor says the account has an authenticator app or passkeys.
	SecondFactor bool `json:"secondFactor"`
	// BypassUntil and BypassBy tell of a bypass of the second factor,
	// started at the command line, for as long as it runs.
	BypassUntil time.Time `json:"bypassUntil,omitzero"`
	BypassBy    string    `json:"bypassBy,omitempty"`
}

func (a *api) describe(sess *auth.Session) sessionResponse {
	res := sessionResponse{
		Username: sess.Username, Role: a.auth.Role(sess.Username),
		Created: sess.Created, LastSeen: sess.LastSeen, Expires: sess.Expires,
		SecondFactor: a.auth.HasSecondFactor(sess.Username),
	}
	if b, ok := a.auth.Bypass(); ok {
		res.BypassUntil, res.BypassBy = b.Until, b.By
	}
	return res
}

func (a *api) setupStatus(w http.ResponseWriter, _ *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needed": a.auth.NeedsSetup()})
	return nil
}

// setup creates the first account and logs it in.
func (a *api) setup(w http.ResponseWriter, r *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		return err
	}
	if err := a.auth.Setup(c.Username, c.Password); err != nil {
		return err
	}
	sess, err := a.auth.SignIn(c.Username)
	if err != nil {
		return err
	}
	a.recordAs(r, sess, audit.Setup)
	setSessionCookie(w, r, sess)
	writeJSON(w, http.StatusCreated, a.describe(sess))
	return nil
}

// loginPageResponse is what the sign-in page shows before anyone signs in.
type loginPageResponse struct {
	Hostname string `json:"hostname,omitempty"`
}

// loginPage names the router on the sign-in page by the saved
// configuration's hostname, the one the sidebar shows.
func (a *api) loginPage(w http.ResponseWriter, _ *http.Request) error {
	var res loginPageResponse
	// A configuration that cannot be read is reported after sign-in, not
	// to whoever opens this page.
	if a.engine != nil {
		if cfg, err := a.engine.Store().Load(); err == nil {
			res.Hostname = cfg.System.Hostname
		}
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// login checks the password. An account with a second factor gets a token
// for the second step instead of a session, unless a bypass is running.
func (a *api) login(w http.ResponseWriter, r *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		return err
	}
	if err := a.auth.Authenticate(c.Username, c.Password, remoteIP(r)); err != nil {
		return a.refused(r, c.Username, err)
	}
	if a.secondFactorDue(c.Username) {
		return a.askForSecondFactor(w, r, c.Username)
	}
	sess, err := a.auth.SignIn(c.Username)
	if err != nil {
		return err
	}
	a.startSession(w, r, sess)
	return nil
}

// refused logs a sign-in that did not get through, and returns its error.
func (a *api) refused(r *http.Request, username string, err error) error {
	// A name that is no account is left out of the log: it is as often
	// a password typed into the wrong box.
	user := "(no such account)"
	if _, ok := a.auth.Account(username); ok {
		user = username
	}
	// Sign-ins are kept at every level, as sshd's are.
	slog.WarnContext(logging.Always(r.Context()), "sign-in refused", "user", user, "address", remoteIP(r), "reason", err)
	return err
}

// startSession ends a sign-in with the session: the cookie, the audit
// entry, and the account as the UI reads it.
func (a *api) startSession(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	slog.InfoContext(logging.Always(r.Context()), "signed in", "user", sess.Username, "address", remoteIP(r))
	if old, err := r.Cookie(SessionCookie); err == nil {
		a.auth.Logout(old.Value)
	}
	a.recordAs(r, sess, audit.SignIn)
	setSessionCookie(w, r, sess)
	writeJSON(w, http.StatusOK, a.describe(sess))
}

// secondFactorDue reports whether signing in as username still takes a
// second step: the account has one and no bypass is running.
func (a *api) secondFactorDue(username string) bool {
	if !a.auth.HasSecondFactor(username) {
		return false
	}
	_, bypassed := a.auth.Bypass()
	return !bypassed
}

// pendingSignIn is what a sign-in that passed the password answers with: the
// token for the second step and what the account can answer it with.
type pendingSignIn struct {
	Pending  string `json:"pending"`
	TOTP     bool   `json:"totp"`
	Passkeys bool   `json:"passkeys"`
	RPID     string `json:"rpId"`
}

func (a *api) askForSecondFactor(w http.ResponseWriter, r *http.Request, username string) error {
	hasTOTP, keys := a.auth.SecondFactorOf(username)
	rp := relyingParty(r)
	token := a.auth.PendingLogin(username)
	if token == "" {
		return errors.New("could not start the second step of the sign-in")
	}
	slog.InfoContext(logging.Always(r.Context()), "second factor due", "user", username, "address", remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]pendingSignIn{"secondFactor": {
		Pending: token, TOTP: hasTOTP, Passkeys: len(passkeysOn(keys, rp.ID)) > 0, RPID: rp.ID,
	}})
	return nil
}

// recordAs records what the account sess belongs to just did, for a
// request that arrived with no session or one the action ended.
func (a *api) recordAs(r *http.Request, sess *auth.Session, action string) {
	p := Principal{Name: sess.Username, Role: a.auth.Role(sess.Username)}
	a.audit.Add(audit.Event{Action: action, By: p.actor(remoteIP(r))})
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(SessionCookie); err == nil {
		a.auth.Logout(c.Value)
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *api) me(w http.ResponseWriter, r *http.Request) error {
	// The session can end between protect's look and this one.
	sess, ok := a.session(r)
	if !ok {
		return errUnauthorized
	}
	writeJSON(w, http.StatusOK, a.describe(sess))
	return nil
}

type passwordChange struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// changePassword verifies the current password (rate limited like a
// login), sets the new one, and re-issues the caller's session since the
// change ends every session for the account.
func (a *api) changePassword(w http.ResponseWriter, r *http.Request) error {
	sess, ok := a.session(r)
	if !ok {
		return errUnauthorized
	}
	var req passwordChange
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	check, err := a.auth.Login(sess.Username, req.Current, remoteIP(r))
	if err != nil {
		return err
	}
	a.auth.Logout(check.ID)
	if err := a.auth.SetPassword(sess.Username, req.New); err != nil {
		return err
	}
	fresh, err := a.auth.SignIn(sess.Username)
	if err != nil {
		return err
	}
	a.recordAs(r, fresh, audit.Password)
	setSessionCookie(w, r, fresh)
	writeJSON(w, http.StatusOK, a.describe(fresh))
	return nil
}

// appName is what authenticator apps and passkey prompts call this router's
// sign-in.
const appName = "Ostiole"

// maxPasskeyName is how many characters a passkey's name may have.
const maxPasskeyName = 40

var (
	// The sign-in page tells this error from the others by its words.
	errInvalidCode = errors.New("invalid code")
	errNeedsAName  = errors.New("passkeys need a name in the address bar, not an address")
)

// refusal is a sign-in step turned down in its own words. It answers 401 as
// errUnauthorized does.
type refusal struct{ err error }

func (e *refusal) Error() string        { return e.err.Error() }
func (e *refusal) Unwrap() error        { return e.err }
func (e *refusal) Is(target error) bool { return target == errUnauthorized }

// relyingParty is the host name the request came to, which is all a passkey
// is bound to. Its ID is empty when the request came to an address, for which
// WebAuthn has no use.
func relyingParty(r *http.Request) webauthn.RelyingParty {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.Contains(host, ":") || net.ParseIP(host) != nil {
		host = ""
	}
	return webauthn.RelyingParty{ID: strings.ToLower(host), Name: appName}
}

// originOf is the origin the browser reports for the request. Browsers leave
// a default port out of the origin and of the Host header alike.
func originOf(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + strings.ToLower(r.Host)
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// credentialOf reads a stored passkey as the verifier takes it.
func credentialOf(k auth.Passkey) (webauthn.Credential, error) {
	id, err := base64.RawURLEncoding.DecodeString(k.ID)
	if err != nil {
		return webauthn.Credential{}, fmt.Errorf("passkey %q: %w", k.Name, err)
	}
	key, err := base64.RawURLEncoding.DecodeString(k.PublicKey)
	if err != nil {
		return webauthn.Credential{}, fmt.Errorf("passkey %q: %w", k.Name, err)
	}
	return webauthn.Credential{ID: id, PublicKey: key, SignCount: k.SignCount, Transports: k.Transports}, nil
}

// passkeysOn picks the passkeys made on a host name, the only ones the
// browser can use there.
func passkeysOn(keys []auth.Passkey, rpID string) []auth.Passkey {
	if rpID == "" {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(keys), func(k auth.Passkey) bool { return k.RPID != rpID })
}

// credentialsOf lists passkeys for the browser; one that will not decode is
// left out.
func credentialsOf(keys []auth.Passkey) []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(keys))
	for _, k := range keys {
		if c, err := credentialOf(k); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// current is the session behind the request, which protect has already let in.
func (a *api) current(r *http.Request) (*auth.Session, error) {
	sess, ok := a.session(r)
	if !ok {
		return nil, errUnauthorized
	}
	return sess, nil
}

// finishSignIn ends a pending sign-in whose second factor checked out.
func (a *api) finishSignIn(w http.ResponseWriter, r *http.Request, pending string) error {
	sess, err := a.auth.ResolvePending(pending)
	if err != nil {
		if errors.Is(err, auth.ErrPendingGone) {
			return errUnauthorized
		}
		return err
	}
	a.startSession(w, r, sess)
	return nil
}

// loginTOTP is the second step with a code from an authenticator app.
func (a *api) loginTOTP(w http.ResponseWriter, r *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	var req struct {
		Pending string `json:"pending"`
		Code    string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	user, ok := a.auth.PendingUser(req.Pending)
	if !ok {
		return errUnauthorized
	}
	secret, ok := a.auth.TOTPSecret(user)
	if !ok {
		return errUnauthorized
	}
	step, match, err := totp.Verify(secret, req.Code, time.Now(), 1)
	if err != nil {
		return err
	}
	if !match {
		a.auth.FailPending(req.Pending, remoteIP(r))
		return a.refused(r, user, &refusal{errInvalidCode})
	}
	switch err := a.auth.UseTOTPStep(user, step); {
	case errors.Is(err, auth.ErrCodeUsed):
		a.auth.FailPending(req.Pending, remoteIP(r))
		return a.refused(r, user, &refusal{err})
	case errors.Is(err, auth.ErrNoSecondFactor):
		return errUnauthorized
	case err != nil:
		return err
	}
	return a.finishSignIn(w, r, req.Pending)
}

// loginPasskeyOptions is the challenge for the second step with a passkey.
func (a *api) loginPasskeyOptions(w http.ResponseWriter, r *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	var req struct {
		Pending string `json:"pending"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	user, ok := a.auth.PendingUser(req.Pending)
	if !ok {
		return errUnauthorized
	}
	rp := relyingParty(r)
	if rp.ID == "" {
		return &badRequest{errNeedsAName}
	}
	allow := credentialsOf(passkeysOn(a.auth.Passkeys(user), rp.ID))
	if len(allow) == 0 {
		return &badRequest{fmt.Errorf("no passkey was made on %s", rp.ID)}
	}
	challenge, err := webauthn.NewChallenge()
	if err != nil {
		return err
	}
	a.auth.SetChallenge(req.Pending, challenge)
	writeJSON(w, http.StatusOK, map[string]any{"publicKey": webauthn.RequestOptions(rp, allow, challenge)})
	return nil
}

// loginPasskey is the second step with a passkey's signed answer.
func (a *api) loginPasskey(w http.ResponseWriter, r *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	var req struct {
		Pending    string                     `json:"pending"`
		Credential webauthn.AssertionResponse `json:"credential"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	user, ok := a.auth.PendingUser(req.Pending)
	if !ok {
		return errUnauthorized
	}
	rp := relyingParty(r)
	if rp.ID == "" {
		return &badRequest{errNeedsAName}
	}
	challenge, ok := a.auth.TakeChallenge(req.Pending)
	if !ok {
		return &badRequest{errors.New("that passkey request timed out; try again")}
	}
	id := strings.TrimRight(cmp.Or(req.Credential.RawID, req.Credential.ID), "=")
	keys := passkeysOn(a.auth.Passkeys(user), rp.ID)
	i := slices.IndexFunc(keys, func(k auth.Passkey) bool { return k.ID == id })
	if i < 0 {
		a.auth.FailPending(req.Pending, remoteIP(r))
		return a.refused(r, user, &refusal{auth.ErrNoSuchPasskey})
	}
	key := keys[i]
	cred, err := credentialOf(key)
	if err != nil {
		return err
	}
	count, err := webauthn.VerifyAssertion(req.Credential, cred, challenge, rp, originOf(r))
	if err != nil {
		a.auth.FailPending(req.Pending, remoteIP(r))
		return a.refused(r, user, &refusal{fmt.Errorf("passkey refused: %w", err)})
	}
	if err := a.auth.PasskeyUsed(user, key.ID, count, time.Now()); err != nil {
		if errors.Is(err, auth.ErrNoSuchPasskey) {
			return errUnauthorized
		}
		return err
	}
	return a.finishSignIn(w, r, req.Pending)
}

// passkeyView is a passkey as the account's own page lists it, with nothing
// that signs in.
type passkeyView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	RPID       string    `json:"rpId"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsed   time.Time `json:"lastUsed,omitzero"`
	Transports []string  `json:"transports,omitempty"`
}

func viewOf(k auth.Passkey) passkeyView {
	return passkeyView{
		ID: k.ID, Name: k.Name, RPID: k.RPID, CreatedAt: k.CreatedAt, LastUsed: k.LastUsed,
		Transports: slices.Clone(k.Transports),
	}
}

type secondFactorResponse struct {
	TOTP     bool          `json:"totp"`
	Passkeys []passkeyView `json:"passkeys"`
	// RPID is the host name a passkey made now would be bound to; empty when
	// the page was reached by address.
	RPID        string    `json:"rpId"`
	BypassUntil time.Time `json:"bypassUntil,omitzero"`
	BypassBy    string    `json:"bypassBy,omitempty"`
}

// secondFactor says what the signed-in account has, never the secrets.
func (a *api) secondFactor(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	hasTOTP, keys := a.auth.SecondFactorOf(sess.Username)
	res := secondFactorResponse{TOTP: hasTOTP, Passkeys: make([]passkeyView, 0, len(keys)), RPID: relyingParty(r).ID}
	for _, k := range keys {
		res.Passkeys = append(res.Passkeys, viewOf(k))
	}
	if b, ok := a.auth.Bypass(); ok {
		res.BypassUntil, res.BypassBy = b.Until, b.By
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// clearSecondFactor turns the signed-in account's second factor off, on its
// password.
func (a *api) clearSecondFactor(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := a.auth.Authenticate(sess.Username, req.Password, remoteIP(r)); err != nil {
		return err
	}
	had := a.auth.HasSecondFactor(sess.Username)
	if err := a.auth.ClearSecondFactor(sess.Username); err != nil {
		return err
	}
	if had {
		a.record(r, audit.SecondFactorClear, "", "")
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// totpSetupKey is where the secret of a setup is kept, against the session
// making it. A key starting "totp:" is held for ten minutes, not the two a
// challenge gets.
func totpSetupKey(sessionID string) string { return "totp:" + sessionID }

// totpStart makes a secret for an authenticator app. Nothing is enabled
// until totpConfirm sees a code from it.
func (a *api) totpStart(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	secret, err := totp.NewSecret()
	if err != nil {
		return err
	}
	a.auth.SetChallenge(totpSetupKey(sess.ID), []byte(secret))
	writeJSON(w, http.StatusOK, map[string]string{"secret": secret, "uri": totp.URI(secret, appName, sess.Username)})
	return nil
}

// totpConfirm turns the authenticator app on once it shows a code the secret
// from totpStart gives. A wrong code leaves the secret for another try: the
// app has it already.
func (a *api) totpConfirm(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	key := totpSetupKey(sess.ID)
	kept, ok := a.auth.TakeChallenge(key)
	if !ok {
		return &badRequest{errors.New("the setup timed out; start it again")}
	}
	secret := string(kept)
	step, match, err := totp.Verify(secret, req.Code, time.Now(), 1)
	if err != nil {
		return err
	}
	if !match {
		a.auth.SetChallenge(key, kept)
		return &badRequest{errInvalidCode}
	}
	if err := a.auth.EnableTOTP(sess.Username, secret, time.Now()); err != nil {
		return err
	}
	// The code that proved the setup is spent, so it cannot also sign in.
	if err := a.auth.UseTOTPStep(sess.Username, step); err != nil {
		return err
	}
	a.record(r, audit.SecondFactorTOTP, "", "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// passkeyOptions is the challenge for making a passkey on this host name.
func (a *api) passkeyOptions(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	hasTOTP, keys := a.auth.SecondFactorOf(sess.Username)
	if !hasTOTP {
		return &badRequest{auth.ErrTOTPFirst}
	}
	rp := relyingParty(r)
	if rp.ID == "" {
		return &badRequest{errNeedsAName}
	}
	handle, err := a.auth.UserID(sess.Username)
	if err != nil {
		return err
	}
	challenge, err := webauthn.NewChallenge()
	if err != nil {
		return err
	}
	a.auth.SetChallenge(sess.ID, challenge)
	options := webauthn.CreationOptions(rp, []byte(handle), sess.Username, credentialsOf(keys), challenge)
	writeJSON(w, http.StatusOK, map[string]any{"publicKey": options})
	return nil
}

// passkeyName is the name a passkey is given: 1 to 40 characters, none of
// them a control character, which a terminal reading the audit log would obey.
func passkeyName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxPasskeyName {
		return "", &badRequest{fmt.Errorf("the name must be 1 to %d characters", maxPasskeyName)}
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", &badRequest{errors.New("the name may not contain control characters")}
	}
	return name, nil
}

// addPasskey checks the browser's answer to passkeyOptions and keeps the
// passkey it made.
func (a *api) addPasskey(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	var req struct {
		Name       string                       `json:"name"`
		Credential webauthn.AttestationResponse `json:"credential"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	name, err := passkeyName(req.Name)
	if err != nil {
		return err
	}
	rp := relyingParty(r)
	if rp.ID == "" {
		return &badRequest{errNeedsAName}
	}
	challenge, ok := a.auth.TakeChallenge(sess.ID)
	if !ok {
		return &badRequest{errors.New("that passkey request timed out; try again")}
	}
	cred, err := webauthn.VerifyRegistration(req.Credential, challenge, rp, originOf(r))
	if err != nil {
		return &badRequest{fmt.Errorf("passkey refused: %w", err)}
	}
	key := auth.Passkey{
		ID: b64url(cred.ID), Name: name, PublicKey: b64url(cred.PublicKey), SignCount: cred.SignCount,
		RPID: rp.ID, CreatedAt: time.Now(), Transports: cred.Transports,
	}
	if err := a.auth.AddPasskey(sess.Username, key); err != nil {
		if errors.Is(err, auth.ErrTOTPFirst) || errors.Is(err, auth.ErrPasskeyExists) {
			return &badRequest{err}
		}
		return err
	}
	a.record(r, audit.PasskeyAdd, name, "")
	writeJSON(w, http.StatusCreated, viewOf(key))
	return nil
}

// removePasskey removes one of the signed-in account's passkeys.
func (a *api) removePasskey(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	id := strings.TrimRight(r.PathValue("id"), "=")
	_, keys := a.auth.SecondFactorOf(sess.Username)
	i := slices.IndexFunc(keys, func(k auth.Passkey) bool { return k.ID == id })
	if i < 0 {
		return &notFound{auth.ErrNoSuchPasskey}
	}
	if err := a.auth.RemovePasskey(sess.Username, id); err != nil {
		if errors.Is(err, auth.ErrNoSuchPasskey) {
			return &notFound{err}
		}
		return err
	}
	a.record(r, audit.PasskeyRemove, keys[i].Name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}
