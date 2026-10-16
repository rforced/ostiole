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
	mux.HandleFunc("POST /api/v1/auth/login/start", a.public(a.loginStart))
	mux.HandleFunc("POST /api/v1/auth/login", a.public(a.login))
	mux.HandleFunc("POST /api/v1/auth/login/totp", a.public(a.loginTOTP))
	mux.HandleFunc("POST /api/v1/auth/login/passkey/options", a.public(a.loginPasskeyOptions))
	mux.HandleFunc("POST /api/v1/auth/login/passkey", a.public(a.loginPasskey))
	mux.HandleFunc("POST /api/v1/auth/logout", a.protect(a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.protect(a.me))
	mux.HandleFunc("POST /api/v1/auth/password", a.protect(a.changePassword))
	mux.HandleFunc("GET /api/v1/auth/mfa", a.protect(a.mfa))
	mux.HandleFunc("POST /api/v1/auth/mfa/clear", a.protect(a.clearMFA))
	mux.HandleFunc("POST /api/v1/auth/mfa/totp/start", a.protect(a.totpStart))
	mux.HandleFunc("POST /api/v1/auth/mfa/totp/confirm", a.protect(a.totpConfirm))
	mux.HandleFunc("POST /api/v1/auth/mfa/passkeys/options", a.protect(a.passkeyOptions))
	mux.HandleFunc("POST /api/v1/auth/mfa/passkeys", a.protect(a.addPasskey))
	mux.HandleFunc("PATCH /api/v1/auth/mfa/passkeys/{id}", a.protect(a.renamePasskey))
	mux.HandleFunc("DELETE /api/v1/auth/mfa/passkeys/{id}", a.protect(a.removePasskey))
	a.registerSessions(mux)
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

// clientOf is where a sign-in came from, as its session keeps it.
func clientOf(r *http.Request) auth.Client {
	return auth.Client{Address: remoteIP(r), Agent: r.UserAgent()}
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
	// MFA says the account has an authenticator app or passkeys.
	MFA bool `json:"mfa"`
	// BypassUntil and BypassBy tell of a bypass of MFA,
	// started at the command line, for as long as it runs.
	BypassUntil time.Time `json:"bypassUntil,omitzero"`
	BypassBy    string    `json:"bypassBy,omitempty"`
}

func (a *api) describe(sess *auth.Session) sessionResponse {
	res := sessionResponse{
		Username: sess.Username, Role: a.auth.Role(sess.Username),
		Created: sess.Created, LastSeen: sess.LastSeen, Expires: sess.Expires,
		MFA: a.auth.HasMFA(sess.Username),
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
	sess, err := a.auth.SignInFrom(c.Username, clientOf(r))
	if err != nil {
		return err
	}
	a.recordAs(r, sess, audit.Setup, "")
	setSessionCookie(w, r, sess)
	writeJSON(w, http.StatusCreated, a.describe(sess))
	return nil
}

// loginPageResponse is what the sign-in page shows before anyone signs in.
type loginPageResponse struct {
	Hostname string `json:"hostname,omitempty"`
	// Passkeys says an account has a passkey made on the host name the page
	// was opened at, so the page offers to sign in with one.
	Passkeys bool `json:"passkeys,omitempty"`
}

// loginPage names the router on the sign-in page by the saved
// configuration's hostname, the one the sidebar shows.
func (a *api) loginPage(w http.ResponseWriter, r *http.Request) error {
	var res loginPageResponse
	// A configuration that cannot be read is reported after sign-in, not
	// to whoever opens this page.
	if a.engine != nil {
		if cfg, err := a.engine.Store().Load(); err == nil {
			res.Hostname = cfg.System.Hostname
		}
	}
	if a.auth != nil {
		res.Passkeys = a.auth.PasskeysOn(relyingParty(r).ID)
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// login checks the password. An account with MFA gets a token
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
	if a.mfaDue(c.Username) {
		return a.askForMFA(w, r, c.Username)
	}
	sess, err := a.auth.SignInFrom(c.Username, clientOf(r))
	if err != nil {
		return err
	}
	a.startSession(w, r, sess, "")
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
// entry, and the account as the UI reads it. how is the audit entry's
// detail: "passkey" for a passkey alone.
func (a *api) startSession(w http.ResponseWriter, r *http.Request, sess *auth.Session, how string) {
	slog.InfoContext(logging.Always(r.Context()), "signed in", "user", sess.Username, "address", remoteIP(r))
	if old, err := r.Cookie(SessionCookie); err == nil {
		a.auth.Logout(old.Value)
	}
	a.recordAs(r, sess, audit.SignIn, how)
	setSessionCookie(w, r, sess)
	writeJSON(w, http.StatusOK, a.describe(sess))
}

// mfaDue reports whether signing in as username still takes a
// second step: the account has one and no bypass is running.
func (a *api) mfaDue(username string) bool {
	if !a.auth.HasMFA(username) {
		return false
	}
	_, bypassed := a.auth.Bypass()
	return !bypassed
}

// pendingSignIn is what a sign-in that passed the password answers with: the
// token for the second step and what the account can answer it with.
type pendingSignIn struct {
	Token    string `json:"token"`
	TOTP     bool   `json:"totp"`
	Passkeys bool   `json:"passkeys"`
	RPID     string `json:"rpId"`
}

func (a *api) askForMFA(w http.ResponseWriter, r *http.Request, username string) error {
	app, keys := a.auth.MFAOf(username)
	rp := relyingParty(r)
	token := a.auth.PendingLogin(username)
	if token == "" {
		return errors.New("could not start the second step of the sign-in")
	}
	slog.InfoContext(logging.Always(r.Context()), "MFA due", "user", username, "address", remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]pendingSignIn{"pending": {
		Token: token, TOTP: app != nil, Passkeys: len(passkeysOn(keys, rp.ID)) > 0, RPID: rp.ID,
	}})
	return nil
}

// recordAs records what the account sess belongs to just did, for a
// request that arrived with no session or one the action ended.
func (a *api) recordAs(r *http.Request, sess *auth.Session, action, detail string) {
	p := Principal{Name: sess.Username, Role: a.auth.Role(sess.Username)}
	a.audit.Add(audit.Event{Action: action, By: p.actor(remoteIP(r)), Detail: detail})
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
	check, err := a.auth.LoginFrom(sess.Username, req.Current, clientOf(r))
	if err != nil {
		return err
	}
	a.auth.Logout(check.ID)
	if err := a.auth.SetPassword(sess.Username, req.New); err != nil {
		return err
	}
	fresh, err := a.auth.SignInFrom(sess.Username, clientOf(r))
	if err != nil {
		return err
	}
	a.recordAs(r, fresh, audit.Password, "")
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
	errInvalidCode   = errors.New("invalid code")
	errNeedsAName    = errors.New("passkeys need a name in the address bar, not an address")
	errWrongPassword = errors.New("password is wrong")
	errNotVerified   = errors.New("the device did not check that it was you")
	errNotItsHandle  = errors.New("the passkey answered for another account")
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

// finishSignIn ends a pending sign-in whose MFA checked out.
func (a *api) finishSignIn(w http.ResponseWriter, r *http.Request, pending, how string) error {
	sess, err := a.auth.ResolvePendingFrom(pending, clientOf(r))
	if err != nil {
		if errors.Is(err, auth.ErrPendingGone) {
			return errUnauthorized
		}
		return err
	}
	a.startSession(w, r, sess, how)
	return nil
}

// passkeyStart is a sign-in a passkey finishes on its own: the token and
// the browser's request options.
type passkeyStart struct {
	Token     string         `json:"token"`
	PublicKey map[string]any `json:"publicKey"`
}

// loginStart opens a sign-in with a passkey alone. It names no account and
// lists no passkey, so the browser offers every passkey it keeps for this
// host name, and the one picked names the account.
func (a *api) loginStart(w http.ResponseWriter, r *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	rp := relyingParty(r)
	if rp.ID == "" {
		return &badRequest{errNeedsAName}
	}
	if a.auth.Blocked(remoteIP(r)) {
		return auth.ErrRateLimited
	}
	challenge, err := webauthn.NewChallenge()
	if err != nil {
		return err
	}
	token := a.auth.StartPasskeyLogin(remoteIP(r))
	if token == "" {
		return errors.New("could not start the sign-in")
	}
	a.auth.SetChallenge(token, challenge)
	writeJSON(w, http.StatusOK, passkeyStart{Token: token, PublicKey: webauthn.RequestOptions(rp, nil, challenge, true)})
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
	user, checked, ok := a.auth.PendingUser(req.Pending)
	if !ok || !checked {
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
	case errors.Is(err, auth.ErrNoMFA):
		return errUnauthorized
	case err != nil:
		return err
	}
	return a.finishSignIn(w, r, req.Pending, "")
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
	user, checked, ok := a.auth.PendingUser(req.Pending)
	if !ok || !checked {
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
	writeJSON(w, http.StatusOK, map[string]any{"publicKey": webauthn.RequestOptions(rp, allow, challenge, false)})
	return nil
}

// loginPasskey is the second step with a passkey's signed answer, or the
// whole sign-in for a token from loginStart.
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
	user, checked, ok := a.auth.PendingUser(req.Pending)
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
	if !checked {
		return a.passkeyAlone(w, r, req.Pending, id, req.Credential, challenge)
	}
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
	got, err := webauthn.VerifyAssertion(req.Credential, cred, challenge, rp, originOf(r))
	if err != nil {
		a.auth.FailPending(req.Pending, remoteIP(r))
		return a.refused(r, user, &refusal{fmt.Errorf("passkey refused: %w", err)})
	}
	if err := a.auth.PasskeyUsed(user, key.ID, got.SignCount, time.Now()); err != nil {
		if errors.Is(err, auth.ErrNoSuchPasskey) {
			return errUnauthorized
		}
		return err
	}
	return a.finishSignIn(w, r, req.Pending, "")
}

// passkeyAlone finishes a sign-in that named no account. The passkey names
// it, and the user handle the device keeps with the passkey must be that
// account's (WebAuthn's step for a user not named first). The device must
// have checked it was the user, by PIN or biometric.
func (a *api) passkeyAlone(w http.ResponseWriter, r *http.Request, pending, id string, resp webauthn.AssertionResponse, challenge []byte) error {
	rp := relyingParty(r)
	user, handle, key, ok := a.auth.PasskeyOwner(id)
	if !ok || key.RPID != rp.ID {
		a.auth.FailPending(pending, remoteIP(r))
		return a.refused(r, "", &refusal{auth.ErrNoSuchPasskey})
	}
	held, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(resp.Response.UserHandle, "="))
	if err != nil || handle == "" || string(held) != handle {
		a.auth.FailPending(pending, remoteIP(r))
		return a.refused(r, user, &refusal{fmt.Errorf("passkey refused: %w", errNotItsHandle)})
	}
	cred, err := credentialOf(key)
	if err != nil {
		return err
	}
	got, err := webauthn.VerifyAssertion(resp, cred, challenge, rp, originOf(r))
	if err == nil && !got.UserVerified {
		err = errNotVerified
	}
	if err != nil {
		a.auth.FailPending(pending, remoteIP(r))
		return a.refused(r, user, &refusal{fmt.Errorf("passkey refused: %w", err)})
	}
	if err := a.auth.PasskeyUsed(user, key.ID, got.SignCount, time.Now()); err != nil {
		if errors.Is(err, auth.ErrNoSuchPasskey) || errors.Is(err, auth.ErrNoSuchUser) {
			return errUnauthorized
		}
		return err
	}
	sess, err := a.auth.ResolvePasskeyFrom(pending, user, clientOf(r))
	if err != nil {
		if errors.Is(err, auth.ErrPendingGone) || errors.Is(err, auth.ErrNoSuchUser) {
			return errUnauthorized
		}
		return err
	}
	a.startSession(w, r, sess, "passkey")
	return nil
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

// totpView is the authenticator app as the account's own page shows it.
type totpView struct {
	EnabledAt time.Time `json:"enabledAt"`
	LastUsed  time.Time `json:"lastUsed,omitzero"`
}

type mfaResponse struct {
	TOTP     *totpView     `json:"totp"`
	Passkeys []passkeyView `json:"passkeys"`
	// RPID is the host name a passkey made now would be bound to; empty when
	// the page was reached by address.
	RPID        string    `json:"rpId"`
	BypassUntil time.Time `json:"bypassUntil,omitzero"`
	BypassBy    string    `json:"bypassBy,omitempty"`
}

// mfa says what the signed-in account has, never the secrets.
func (a *api) mfa(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a.mfaOf(r, sess.Username))
	return nil
}

func (a *api) mfaOf(r *http.Request, username string) mfaResponse {
	app, keys := a.auth.MFAOf(username)
	res := mfaResponse{Passkeys: make([]passkeyView, 0, len(keys)), RPID: relyingParty(r).ID}
	if app != nil {
		res.TOTP = &totpView{EnabledAt: app.EnabledAt}
		if app.LastStep > 0 {
			res.TOTP.LastUsed = time.Unix(app.LastStep*30, 0).UTC()
		}
	}
	for _, k := range keys {
		res.Passkeys = append(res.Passkeys, viewOf(k))
	}
	if b, ok := a.auth.Bypass(); ok {
		res.BypassUntil, res.BypassBy = b.Until, b.By
	}
	return res
}

// clearMFA turns the signed-in account's MFA off, on its
// password.
func (a *api) clearMFA(w http.ResponseWriter, r *http.Request) error {
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
	had := a.auth.HasMFA(sess.Username)
	if err := a.auth.ClearMFA(sess.Username); err != nil {
		return err
	}
	if had {
		a.record(r, audit.MFAClear, "", "")
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
	a.record(r, audit.MFATOTP, "", "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// passkeyOptions is the challenge for making a passkey on this host name,
// on the password: the browser's prompt comes after this, so a wrong
// password is told before a device makes a passkey the router would refuse.
// addPasskey needs the challenge, so it needs the password too.
func (a *api) passkeyOptions(w http.ResponseWriter, r *http.Request) error {
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
	app, keys := a.auth.MFAOf(sess.Username)
	if app == nil {
		return &badRequest{auth.ErrTOTPFirst}
	}
	rp := relyingParty(r)
	if rp.ID == "" {
		return &badRequest{errNeedsAName}
	}
	if err := a.checkPassword(r, sess.Username, req.Password); err != nil {
		return err
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
	_, keys := a.auth.MFAOf(sess.Username)
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

// checkPassword asks for the account's password again before a change that
// lets a device sign in, rate limited as a sign-in is.
func (a *api) checkPassword(r *http.Request, username, password string) error {
	err := a.auth.Authenticate(username, password, remoteIP(r))
	if errors.Is(err, auth.ErrInvalidCredentials) {
		return &refusal{errWrongPassword}
	}
	return err
}

// renamePasskey gives one of the signed-in account's passkeys a new name.
func (a *api) renamePasskey(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	name, err := passkeyName(req.Name)
	if err != nil {
		return err
	}
	id := strings.TrimRight(r.PathValue("id"), "=")
	keys := a.auth.Passkeys(sess.Username)
	i := slices.IndexFunc(keys, func(k auth.Passkey) bool { return k.ID == id })
	if i < 0 {
		return &notFound{auth.ErrNoSuchPasskey}
	}
	key := keys[i]
	if err := a.auth.RenamePasskey(sess.Username, id, name); err != nil {
		if errors.Is(err, auth.ErrNoSuchPasskey) {
			return &notFound{err}
		}
		return err
	}
	if key.Name != name {
		a.record(r, audit.PasskeyRename, key.Name, name)
	}
	key.Name = name
	writeJSON(w, http.StatusOK, viewOf(key))
	return nil
}
