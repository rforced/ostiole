package acme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxAnswer bounds what is read of an answer. A certificate chain is a
// few kilobytes.
const maxAnswer = 1 << 20

// nonceRetries is how long a request goes on being sent again while the
// CA refuses its nonce.
const nonceRetries = 20 * time.Second

// problemPrefix starts every error type RFC 8555 defines.
const problemPrefix = "urn:ietf:params:acme:error:"

// directory is what a CA publishes at its directory URL (RFC 8555 §7.1.1).
type directory struct {
	NewNonce    string `json:"newNonce"`
	NewAccount  string `json:"newAccount"`
	NewOrder    string `json:"newOrder"`
	RenewalInfo string `json:"renewalInfo"`
}

// conn is one conversation with a CA: its directory, the nonces it has
// handed out, and the account that signs.
type conn struct {
	http      *http.Client
	userAgent string
	dir       directory
	signer    *signer
	// kid is the account's URL once it is known.
	kid string

	mu     sync.Mutex
	nonces []string
}

// Problem is an error a CA sent back (RFC 8555 §6.7).
type Problem struct {
	Type        string      `json:"type"`
	Detail      string      `json:"detail"`
	Status      int         `json:"status"`
	Subproblems []Problem   `json:"subproblems"`
	Identifier  *identifier `json:"identifier"`
}

// Error says what the CA said, and the kind of problem it named.
func (p *Problem) Error() string {
	kind := strings.TrimPrefix(p.Type, problemPrefix)
	msg := strings.TrimSuffix(p.Detail, ".")
	if msg == "" {
		msg = kind
	}
	for _, sub := range p.Subproblems {
		if sub.Identifier != nil {
			msg += "; " + sub.Identifier.Value + ": " + strings.TrimSuffix(sub.Detail, ".")
		}
	}
	if kind == "" {
		return msg
	}
	return msg + " (" + kind + ")"
}

// is reports whether err is a problem of the given kind, such as
// "badNonce".
func is(err error, kind string) bool {
	var p *Problem
	return errors.As(err, &p) && p.Type == problemPrefix+kind
}

// answer is what a request brought back.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// decode reads a JSON answer into out.
func (a *answer) decode(out any) error {
	if err := json.Unmarshal(a.body, out); err != nil {
		return fmt.Errorf("the CA's answer does not parse: %w", err)
	}
	return nil
}

// retryAfter is how long the CA asked to be left alone: seconds, or a
// date. Zero when it did not say.
func (a *answer) retryAfter() time.Duration {
	v := strings.TrimSpace(a.header.Get("Retry-After"))
	if s, err := strconv.Atoi(v); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// dial reads the CA's directory.
func (c *Client) dial(ctx context.Context, directoryURL, caCert string, s *signer) (*conn, error) {
	client, err := c.httpClient(caCert)
	if err != nil {
		return nil, err
	}
	cn := &conn{http: client, userAgent: c.UserAgent, signer: s}
	a, err := cn.send(ctx, http.MethodGet, directoryURL, nil)
	if err == nil && a.status != http.StatusOK {
		err = problemIn(a)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the CA's directory: %w", err)
	}
	if err := a.decode(&cn.dir); err != nil {
		return nil, err
	}
	if cn.dir.NewNonce == "" || cn.dir.NewAccount == "" || cn.dir.NewOrder == "" {
		return nil, errors.New("the CA's directory names no newNonce, newAccount or newOrder")
	}
	return cn, nil
}

// post signs payload for url and sends it; a nil payload is a
// POST-as-GET. A refused nonce is the one refusal retried, with a fresh
// nonce, for up to 20 seconds. The answer comes back with a refusal too:
// its headers can still matter.
func (cn *conn) post(ctx context.Context, url string, payload []byte) (*answer, error) {
	give := time.Now().Add(nonceRetries)
	wait := 200 * time.Millisecond
	for {
		nonce, err := cn.nonce(ctx)
		if err != nil {
			return nil, err
		}
		body, err := cn.signer.sign(url, nonce, cn.kid, payload)
		if err != nil {
			return nil, err
		}
		a, err := cn.send(ctx, http.MethodPost, url, body)
		if err != nil {
			return nil, err
		}
		if a.status < 400 {
			return a, nil
		}
		err = problemIn(a)
		if !is(err, "badNonce") || time.Now().Add(wait).After(give) {
			return a, err
		}
		select {
		case <-ctx.Done():
			return a, err
		case <-time.After(wait):
		}
		wait = min(2*wait, 5*time.Second)
	}
}

// postJSON posts v as JSON.
func (cn *conn) postJSON(ctx context.Context, url string, v any) (*answer, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return cn.post(ctx, url, payload)
}

// nonce takes one the CA handed out, or asks newNonce for one.
func (cn *conn) nonce(ctx context.Context) (string, error) {
	if n, ok := cn.pop(); ok {
		return n, nil
	}
	if _, err := cn.send(ctx, http.MethodHead, cn.dir.NewNonce, nil); err != nil {
		return "", fmt.Errorf("asking for a nonce: %w", err)
	}
	if n, ok := cn.pop(); ok {
		return n, nil
	}
	return "", errors.New("the CA sent no nonce")
}

func (cn *conn) pop() (string, bool) {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	if len(cn.nonces) == 0 {
		return "", false
	}
	n := cn.nonces[len(cn.nonces)-1]
	cn.nonces = cn.nonces[:len(cn.nonces)-1]
	return n, true
}

// send makes one request and keeps the nonce the answer brings.
func (cn *conn) send(ctx context.Context, method, url string, body []byte) (*answer, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("%s is not HTTPS", url)
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", cn.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/jose+json")
	}
	resp, err := cn.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if n := resp.Header.Get("Replay-Nonce"); n != "" {
		cn.mu.Lock()
		cn.nonces = append(cn.nonces, n)
		cn.mu.Unlock()
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, err
	}
	return &answer{status: resp.StatusCode, header: resp.Header, body: raw}, nil
}

// problemIn reads the problem an answer carries, or makes one of its
// status when it carries none.
func problemIn(a *answer) error {
	p := &Problem{}
	if json.Unmarshal(a.body, p) != nil || (p.Type == "" && p.Detail == "") {
		p = &Problem{Detail: fmt.Sprintf("the CA answered %d %s", a.status, http.StatusText(a.status))}
	}
	if p.Status == 0 {
		p.Status = a.status
	}
	return p
}
