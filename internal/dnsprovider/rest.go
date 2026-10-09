package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// api is one provider's JSON API: where it is, how a request carries the
// credentials, and how an error reads. A refused request is not tried
// again.
type api struct {
	// name is the provider as errors name it.
	name      string
	base      string
	http      *http.Client
	userAgent string
	// auth puts the credentials on a request.
	auth func(h http.Header)
	// detail reads the provider's own message out of an error's body.
	detail func(body []byte) string
}

// do sends one request and decodes a JSON answer into out. The status
// comes back with a refusal too, for the callers to which a 404 is an
// answer.
func (a *api) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return 0, err
		}
	}
	status, header, answer, err := a.send(ctx, method, path, raw)
	if err != nil {
		return status, err
	}
	if status/100 != 2 {
		return status, a.refusal(status, header, answer)
	}
	if out == nil || len(bytes.TrimSpace(answer)) == 0 {
		return status, nil
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return status, fmt.Errorf("reading %s's answer: %w", a.name, err)
	}
	return status, nil
}

func (a *api) send(ctx context.Context, method, path string, body []byte) (int, http.Header, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.userAgent != "" {
		req.Header.Set("User-Agent", a.userAgent)
	}
	if a.auth != nil {
		a.auth(req.Header)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return 0, nil, nil, fmt.Errorf("could not reach %s: %w", a.name, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("reading %s's answer: %w", a.name, err)
	}
	return resp.StatusCode, resp.Header, answer, nil
}

// refusal says what the provider refused, in the words the page shows.
func (a *api) refusal(status int, header http.Header, body []byte) error {
	detail := ""
	if a.detail != nil {
		detail = a.detail(body)
	}
	if detail == "" {
		detail = strconv.Itoa(status) + " " + http.StatusText(status)
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		if wait := retryAfter(header); wait > 0 {
			return &Error{Message: fmt.Sprintf("%s asked to wait %s (%s)", a.name, wait, detail), RetryAfter: wait}
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &Error{Message: fmt.Sprintf("%s refused the credentials (%s)", a.name, detail)}
	}
	return &Error{Message: fmt.Sprintf("%s: %s", a.name, detail)}
}

// retryAfter reads a Retry-After header in seconds; zero when there is
// none.
func retryAfter(h http.Header) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After")))
	if err != nil || s <= 0 {
		return 0
	}
	return time.Duration(s) * time.Second
}

// relative is name within zone, empty at the zone's apex.
func relative(name, zone string) (string, error) {
	n, z := strings.TrimSuffix(name, "."), strings.TrimSuffix(zone, ".")
	switch {
	case strings.EqualFold(n, z):
		return "", nil
	case len(n) > len(z)+1 && strings.EqualFold(n[len(n)-len(z)-1:], "."+z):
		return n[:len(n)-len(z)-1], nil
	}
	return "", fmt.Errorf("%s is not in the zone %s", name, zone)
}

// quoted is a TXT value as a zone file writes it, which is how the
// rrset APIs take one.
func quoted(value string) string { return strconv.Quote(value) }

// oneLine keeps what a provider said to one line of bounded length.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
