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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
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

// Roles lists them in order, strongest first.
var Roles = []Role{RoleAdmin, RoleOperator, RoleViewer}

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
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Role        Role       `json:"role"`
	Hash        string     `json:"hash"`
	CreatedAt   time.Time  `json:"createdAt"`
	CreatedBy   string     `json:"createdBy,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
	Description string     `json:"description,omitempty"`
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
	if info, err := os.Stat(t.path); err == nil {
		t.loadedAt, t.loadedSz = info.ModTime(), info.Size()
	}
	return nil
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
	f := tokensFile{Version: 1}
	for _, tok := range t.tokens {
		f.Tokens = append(f.Tokens, tok)
	}
	sort.Slice(f.Tokens, func(i, j int) bool { return f.Tokens[i].CreatedAt.Before(f.Tokens[j].CreatedAt) })
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
	if err := os.WriteFile(t.path, raw, 0o600); err != nil {
		return err
	}
	if info, err := os.Stat(t.path); err == nil {
		t.loadedAt, t.loadedSz = info.ModTime(), info.Size()
	}
	return nil
}

// List reports every token, oldest first. The hashes are cleared: nothing
// outside this package needs them.
func (t *Tokens) List() []Token {
	t.refresh()
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Token, 0, len(t.tokens))
	for _, tok := range t.tokens {
		tok.Hash = ""
		out = append(out, tok)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Create mints a token and returns it with the secret filled in. That is
// the only time the secret exists outside the caller's hands.
func (t *Tokens) Create(name string, role Role, ttl time.Duration, createdBy string) (Token, string, error) {
	if !tokenNameRe.MatchString(name) {
		return Token{}, "", errors.New("a token name is 1-64 characters of letters, digits, spaces, dots, or dashes")
	}
	if !role.Valid() {
		return Token{}, "", fmt.Errorf("unknown role %q", role)
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
		ID:        id,
		Name:      name,
		Role:      role,
		Hash:      hashToken(full),
		CreatedAt: t.now().UTC().Truncate(time.Second),
		CreatedBy: createdBy,
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
