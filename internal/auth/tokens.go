package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ostiole/internal/atomicfile"
)

// Role decides what an account or a token may do. There are three
// because an appliance has three kinds of user: whoever owns the router,
// whoever changes the rules, and whatever is reading the dashboard.
type Role string

// Roles, from most to least.
const (
	// RoleAdmin can do everything, including managing accounts, tokens,
	// certificates, and updates.
	RoleAdmin Role = "admin"
	// RoleOperator can change and apply the configuration, but not the
	// things that decide who gets in or what the router runs.
	RoleOperator Role = "operator"
	// RoleViewer can look, and nothing else.
	RoleViewer Role = "viewer"
)

var roleRank = map[Role]int{RoleAdmin: 3, RoleOperator: 2, RoleViewer: 1}

// Allows reports whether this role is at least as strong as need.
func (r Role) Allows(need Role) bool { return roleRank[r] >= roleRank[need] }

// Valid reports whether the role is one of the three.
func (r Role) Valid() bool { _, ok := roleRank[r]; return ok }

// TokenPrefix marks an Ostiole API token, so one that leaks into a log
// or a repository is recognisable for what it is.
const TokenPrefix = "ost_"

// TokensFile holds the API tokens, beside the users file.
const TokensFile = "tokens.json"

// Errors tokens can produce.
var (
	ErrTokenNotFound = errors.New("no such token")
	ErrInvalidToken  = errors.New("invalid API token")
	ErrTokenExpired  = errors.New("this API token has expired")
)

// Token is an API credential. Only its hash is kept: the secret is shown
// once, when it is created, and cannot be recovered afterwards.
type Token struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy,omitempty"`
	// Owner is the account the token belongs to; empty for a router token.
	Owner       string     `json:"owner,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	Description string     `json:"description,omitempty"`
	// Certificates the token may fetch, and nothing else.
	Certificates []string `json:"certificates,omitempty"`
	// Metrics has the token read the metrics, and nothing else: what a
	// monitoring system scrapes with needs no logs or configuration.
	Metrics bool `json:"metrics,omitempty"`
}

// Limits narrows what a token may do below its role.
type Limits struct {
	// Certificates the token may fetch, and nothing else.
	Certificates []string
	// Metrics has the token read the metrics, and nothing else.
	Metrics bool
}

// Expired reports whether the token is past its expiry.
func (t Token) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && now.After(*t.ExpiresAt)
}

type tokensFile struct {
	Version int     `json:"version"`
	Tokens  []Token `json:"tokens"`
}

// Tokens stores API tokens. It is separate from the users file so that
// the two can be backed up and restored apart.
type Tokens struct {
	path string
	now  func() time.Time

	mu       sync.RWMutex
	tokens   map[string]Token
	loadedAt time.Time
	loadedSz int64
}

var tokenNameRe = regexp.MustCompile(`^[\w][\w .-]{0,63}$`)

// certIDRe is what a certificate is called. It is the configuration's
// pattern, repeated here so that a restricted token cannot name
// something that could never exist.
var certIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// MaxTokenCertificates bounds the list a restricted token carries.
const MaxTokenCertificates = 32

// NewTokens opens (or lazily creates) the tokens file in dir.
func NewTokens(dir string) (*Tokens, error) {
	t := &Tokens{path: filepath.Join(dir, TokensFile), now: time.Now, tokens: map[string]Token{}}
	if err := t.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return t, nil
}

func (t *Tokens) load() error {
	raw, err := os.ReadFile(t.path)
	if err != nil {
		return err
	}
	var f tokensFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("%s: %w", t.path, err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tokens = make(map[string]Token, len(f.Tokens))
	for _, tok := range f.Tokens {
		t.tokens[tok.ID] = tok
	}
	t.sweep(t.now())
	if info, err := os.Stat(t.path); err == nil {
		t.loadedAt, t.loadedSz = info.ModTime(), info.Size()
	}
	return nil
}

// pruneAfter is how long an expired token stays in the file. It is
// refused from the moment it expires; the wait is for a clock that is
// ahead at boot, before chrony steps it, which would prune live tokens.
const pruneAfter = 30 * 24 * time.Hour

// sweep drops tokens expired for longer than pruneAfter; called with the
// lock held. Only save takes that to the file: a read that wrote could put
// back a token another process had just deleted.
func (t *Tokens) sweep(now time.Time) {
	maps.DeleteFunc(t.tokens, func(_ string, tok Token) bool { return tok.Expired(now.Add(-pruneAfter)) })
}

// refresh re-reads the file when something else has changed it, so the
// CLI and the daemon agree without talking to each other.
func (t *Tokens) refresh() {
	info, err := os.Stat(t.path)
	if err != nil {
		return
	}
	t.mu.RLock()
	same := info.ModTime().Equal(t.loadedAt) && info.Size() == t.loadedSz
	t.mu.RUnlock()
	if !same {
		_ = t.load()
	}
}

func (t *Tokens) save() error {
	t.sweep(t.now())
	f := tokensFile{Version: 1}
	for _, tok := range t.tokens {
		f.Tokens = append(f.Tokens, tok)
	}
	sort.Slice(f.Tokens, func(i, j int) bool { return earlier(f.Tokens[i], f.Tokens[j]) })
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return err
	}
	// 0600: these hashes are the only thing between a copy of the file and
	// an authenticated request.
	if err := atomicfile.Write(t.path, raw, 0o600); err != nil {
		return err
	}
	if info, err := os.Stat(t.path); err == nil {
		t.loadedAt, t.loadedSz = info.ModTime(), info.Size()
	}
	return nil
}

// List reports the tokens, oldest first, less those expired for longer
// than pruneAfter. The hashes are cleared: nothing outside this package
// needs them.
func (t *Tokens) List() []Token {
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweep(t.now())
	out := make([]Token, 0, len(t.tokens))
	for _, tok := range t.tokens {
		tok.Hash = ""
		out = append(out, tok)
	}
	sort.Slice(out, func(i, j int) bool { return earlier(out[i], out[j]) })
	return out
}

// earlier orders tokens oldest first; two made in the same second by name.
func earlier(a, b Token) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.Name < b.Name
}

// Create mints a token and returns it with the secret filled in. That is
// the only time the secret exists outside the caller's hands. A token
// given certificates fetches those and can do nothing else, whatever its
// role says, and one limited to the metrics reads them and nothing else.
func (t *Tokens) Create(name string, role Role, ttl time.Duration, createdBy, owner string, limits Limits) (Token, string, error) {
	certificates := limits.Certificates
	if !tokenNameRe.MatchString(name) {
		return Token{}, "", errors.New("a token name is 1-64 characters of letters, digits, spaces, dots, or dashes")
	}
	if !role.Valid() {
		return Token{}, "", fmt.Errorf("unknown role %q", role)
	}
	if len(certificates) > MaxTokenCertificates {
		return Token{}, "", fmt.Errorf("a token is limited to at most %d certificates", MaxTokenCertificates)
	}
	for _, id := range certificates {
		if !certIDRe.MatchString(id) {
			return Token{}, "", fmt.Errorf("%q is not a certificate name", id)
		}
	}
	if limits.Metrics && len(certificates) > 0 {
		return Token{}, "", errors.New("a token reads the metrics or fetches certificates, not both")
	}
	if limits.Metrics && role != RoleViewer {
		return Token{}, "", errors.New("a token that reads the metrics is a viewer")
	}
	id, err := randomHex(4)
	if err != nil {
		return Token{}, "", err
	}
	secret, err := randomSecret()
	if err != nil {
		return Token{}, "", err
	}
	full := TokenPrefix + id + "_" + secret
	tok := Token{
		ID:           id,
		Name:         name,
		Role:         role,
		Hash:         hashToken(full),
		CreatedAt:    t.now().UTC().Truncate(time.Second),
		CreatedBy:    createdBy,
		Owner:        owner,
		Certificates: certificates,
		Metrics:      limits.Metrics,
	}
	if ttl > 0 {
		expires := tok.CreatedAt.Add(ttl)
		tok.ExpiresAt = &expires
	}
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, existing := range t.tokens {
		if strings.EqualFold(existing.Name, name) {
			return Token{}, "", fmt.Errorf("a token called %q already exists", name)
		}
	}
	t.tokens[tok.ID] = tok
	if err := t.save(); err != nil {
		delete(t.tokens, tok.ID)
		return Token{}, "", err
	}
	shown := tok
	shown.Hash = ""
	return shown, full, nil
}

// Delete removes a token by id or by name.
func (t *Tokens) Delete(ref string) error {
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, tok := range t.tokens {
		if id == ref || strings.EqualFold(tok.Name, ref) {
			delete(t.tokens, id)
			return t.save()
		}
	}
	return ErrTokenNotFound
}

// ListOwned reports the owner's tokens as List does.
func (t *Tokens) ListOwned(owner string) []Token {
	out := []Token{}
	for _, tok := range t.List() {
		if owner != "" && tok.Owner == owner {
			out = append(out, tok)
		}
	}
	return out
}

// DeleteOf removes a token by id or by name when owner owns it.
func (t *Tokens) DeleteOf(owner, ref string) error {
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, tok := range t.tokens {
		if owner != "" && tok.Owner == owner && (id == ref || strings.EqualFold(tok.Name, ref)) {
			delete(t.tokens, id)
			return t.save()
		}
	}
	return ErrTokenNotFound
}

// DeleteOwned removes every token the owner has.
func (t *Tokens) DeleteOwned(owner string) error {
	return t.change(func(tok *Token) bool { return owner != "" && tok.Owner == owner }, nil)
}

// Rename moves the owner's tokens to the account's new name.
func (t *Tokens) Rename(owner, next string) error {
	if owner == next {
		return nil
	}
	return t.change(nil, func(tok *Token) bool {
		if owner == "" || tok.Owner != owner {
			return false
		}
		tok.Owner = next
		return true
	})
}

// Adopt gives a token made before owners existed to the account that made
// it, when that account is still there.
func (t *Tokens) Adopt(exists func(username string) bool) error {
	return t.change(nil, func(tok *Token) bool {
		if tok.Owner != "" || tok.CreatedBy == "" || tok.CreatedBy == "cli" || !exists(tok.CreatedBy) {
			return false
		}
		tok.Owner = tok.CreatedBy
		return true
	})
}

// change drops the tokens drop reports and edits the rest with edit, and
// saves only when either did something.
func (t *Tokens) change(drop, edit func(tok *Token) bool) error {
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	changed := false
	for id, tok := range t.tokens {
		if drop != nil && drop(&tok) {
			delete(t.tokens, id)
			changed = true
			continue
		}
		if edit != nil && edit(&tok) {
			t.tokens[id] = tok
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return t.save()
}

// Authenticate checks a presented token and returns it. A token that is
// recognised has its last use recorded, which is how an unused one is
// spotted and removed.
func (t *Tokens) Authenticate(presented string) (Token, error) {
	presented = strings.TrimSpace(presented)
	if !strings.HasPrefix(presented, TokenPrefix) {
		return Token{}, ErrInvalidToken
	}
	id := tokenID(presented)
	sum := hashToken(presented)
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	tok, ok := t.tokens[id]
	if !ok {
		return Token{}, ErrInvalidToken
	}
	if subtle.ConstantTimeCompare([]byte(tok.Hash), []byte(sum)) != 1 {
		return Token{}, ErrInvalidToken
	}
	now := t.now().UTC()
	if tok.Expired(now) {
		return Token{}, ErrTokenExpired
	}
	// Recording every request would write the file constantly; a minute's
	// resolution is enough to answer "is this still used".
	if tok.LastUsedAt == nil || now.Sub(*tok.LastUsedAt) > time.Minute {
		stamp := now.Truncate(time.Second)
		tok.LastUsedAt = &stamp
		t.tokens[id] = tok
		_ = t.save()
	}
	out := tok
	out.Hash = ""
	return out, nil
}

// tokenID pulls the visible id out of a presented token. It is only a
// lookup key; the hash decides whether the token is real.
func tokenID(full string) string {
	rest := strings.TrimPrefix(full, TokenPrefix)
	if i := strings.IndexByte(rest, '_'); i > 0 {
		return rest[:i]
	}
	return ""
}

func hashToken(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
