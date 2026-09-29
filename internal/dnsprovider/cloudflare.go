package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// cloudflareAPI is the root of Cloudflare's v4 API.
const cloudflareAPI = "https://api.cloudflare.com/client/v4"

// cloudflareComment is written on an address record Ostiole creates, so
// whoever finds it in the dashboard knows what keeps it.
const cloudflareComment = "Dynamic DNS from Ostiole"

// cloudflareChallenge is written on a challenge record, so one left behind
// says where it came from.
const cloudflareChallenge = "ACME challenge from Ostiole"

// cloudflareTTL is what a challenge record is written with, the shortest
// Cloudflare takes other than its automatic TTL.
const cloudflareTTL = 120

// cfIdentical is Cloudflare's code for a record that is already there.
const cfIdentical = 81058

// cloudflareWait is how long a 429 is taken to mean when it does not say:
// Cloudflare blocks a token over its limit for five minutes.
const cloudflareWait = 5 * time.Minute

// cloudflare writes records through Cloudflare's API, with the token the
// provider holds. A zone's id is looked up once and kept.
type cloudflare struct {
	base      string
	token     string
	zoneToken string
	http      *http.Client
	userAgent string

	mu    sync.Mutex
	zones map[string]string
}

func newCloudflare(p model.DNSProvider, o Options) (Client, error) {
	token := strings.TrimSpace(p.Settings["token"])
	if token == "" {
		return nil, errors.New("the Cloudflare provider has no API token")
	}
	base := cloudflareAPI
	if o.CloudflareAPI != "" {
		base = strings.TrimSuffix(o.CloudflareAPI, "/")
	}
	zoneToken := strings.TrimSpace(p.Settings["zoneToken"])
	if zoneToken == "" {
		zoneToken = token
	}
	return &cloudflare{
		base:      base,
		token:     token,
		zoneToken: zoneToken,
		http:      o.client(),
		userAgent: o.UserAgent,
		zones:     map[string]string{},
	}, nil
}

// cfRecord is the part of a DNS record this client reads.
type cfRecord struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// AddTXT creates a record for the value. One that is already there, left
// by an order cut short, is the record wanted.
func (c *cloudflare) AddTXT(ctx context.Context, r Record) error {
	id, err := c.zone(ctx, r.Zone)
	if err != nil {
		return err
	}
	body := map[string]any{
		"type": "TXT", "name": r.Name, "content": `"` + r.Value + `"`,
		"ttl": cloudflareTTL, "comment": cloudflareChallenge,
	}
	err = c.do(ctx, c.token, r.Zone, http.MethodPost, "/zones/"+url.PathEscape(id)+"/dns_records", body, nil)
	if e := (*Error)(nil); errors.As(err, &e) && e.code == cfIdentical {
		return nil
	}
	return err
}

// RemoveTXT deletes the records holding the value, found by listing the
// name's TXT records, so a record left by an order cut short goes too.
func (c *cloudflare) RemoveTXT(ctx context.Context, r Record) error {
	id, err := c.zone(ctx, r.Zone)
	if err != nil {
		return err
	}
	records, err := c.records(ctx, r.Zone, id, r.Name, "TXT")
	if err != nil {
		return err
	}
	for _, rec := range records {
		if unquote(rec.Content) != r.Value {
			continue
		}
		path := "/zones/" + url.PathEscape(id) + "/dns_records/" + url.PathEscape(rec.ID)
		if err := c.do(ctx, c.token, r.Zone, http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// unquote reads a TXT record's content, which Cloudflare keeps quoted.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func (c *cloudflare) Lookup(ctx context.Context, zone, name string, t RecordType) ([]netip.Addr, error) {
	id, err := c.zone(ctx, zone)
	if err != nil {
		return nil, err
	}
	records, err := c.records(ctx, zone, id, name, string(t))
	if err != nil {
		return nil, err
	}
	return addresses(records), nil
}

func (c *cloudflare) Set(ctx context.Context, zone, name string, t RecordType, addr netip.Addr) ([]netip.Addr, error) {
	id, err := c.zone(ctx, zone)
	if err != nil {
		return nil, err
	}
	records, err := c.records(ctx, zone, id, name, string(t))
	if err != nil {
		return nil, err
	}
	was := addresses(records)
	path := "/zones/" + url.PathEscape(id) + "/dns_records"
	switch len(records) {
	case 0:
		// Created DNS only, with the automatic TTL; the proxy status and
		// the TTL are the operator's to change in Cloudflare afterwards.
		body := map[string]any{
			"type": string(t), "name": name, "content": addr.String(),
			"ttl": 1, "proxied": false, "comment": cloudflareComment,
		}
		return was, c.do(ctx, c.token, zone, http.MethodPost, path, body, nil)
	case 1:
		if slices.Equal(was, []netip.Addr{addr}) {
			return was, nil
		}
		// The address alone, so the TTL, the proxy status and the comment
		// stay as they were set.
		body := map[string]any{"content": addr.String()}
		return was, c.do(ctx, c.token, zone, http.MethodPatch, path+"/"+url.PathEscape(records[0].ID), body, nil)
	default:
		return was, &Error{Message: fmt.Sprintf("Cloudflare holds %d %s records for %s; Ostiole keeps one", len(records), t, name)}
	}
}

// testPage is how many zones a test lists.
const testPage = 50

func (c *cloudflare) Test(ctx context.Context, domains []string) (*TestResult, error) {
	var zones []struct {
		Name string `json:"name"`
	}
	info, err := c.doPaged(ctx, c.zoneToken, "/zones?per_page="+strconv.Itoa(testPage), &zones)
	if err != nil {
		return nil, err
	}
	out := &TestResult{Zones: []string{}, Domains: []DomainTest{}}
	for _, z := range zones {
		out.Zones = append(out.Zones, z.Name)
	}
	out.More = max(info.TotalCount-len(zones), 0)
	for _, d := range domains {
		res := DomainTest{Domain: d}
		if err := c.readable(ctx, d); err != nil {
			res.Error = err.Error()
		}
		out.Domains = append(out.Domains, res)
	}
	return out, nil
}

// readable finds a domain's zone and reads a record from it, with the
// token that writes records.
func (c *cloudflare) readable(ctx context.Context, domain string) error {
	id, err := c.zone(ctx, domain)
	if err != nil {
		return err
	}
	var records []cfRecord
	return c.do(ctx, c.token, domain, http.MethodGet, "/zones/"+url.PathEscape(id)+"/dns_records?per_page=1", nil, &records)
}

// zone finds the id Cloudflare knows a zone by.
func (c *cloudflare) zone(ctx context.Context, name string) (string, error) {
	c.mu.Lock()
	id, ok := c.zones[name]
	c.mu.Unlock()
	if ok {
		return id, nil
	}
	var zones []struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, c.zoneToken, name, http.MethodGet, "/zones?name="+url.QueryEscape(name), nil, &zones); err != nil {
		return "", err
	}
	if len(zones) == 0 || zones[0].ID == "" {
		return "", &Error{Message: fmt.Sprintf("Cloudflare has no zone %s that this token can see", name)}
	}
	c.mu.Lock()
	c.zones[name] = zones[0].ID
	c.mu.Unlock()
	return zones[0].ID, nil
}

// records lists the name's records of one type. The name filter is exact
// and ignores case.
func (c *cloudflare) records(ctx context.Context, zone, id, name, t string) ([]cfRecord, error) {
	q := url.Values{"type": {t}, "name": {name}, "per_page": {"100"}}
	var out []cfRecord
	err := c.do(ctx, c.token, zone, http.MethodGet, "/zones/"+url.PathEscape(id)+"/dns_records?"+q.Encode(), nil, &out)
	return out, err
}

func addresses(records []cfRecord) []netip.Addr {
	out := []netip.Addr{}
	for _, r := range records {
		if ip, err := netip.ParseAddr(r.Content); err == nil {
			out = append(out, ip)
		}
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return out
}

// cfEnvelope is what every answer comes in.
type cfEnvelope struct {
	Success    bool            `json:"success"`
	Errors     []cfError       `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo cfResultInfo    `json:"result_info"`
}

// cfResultInfo says how much of a list came back.
type cfResultInfo struct {
	TotalCount int `json:"total_count"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// maxAnswer bounds what is read of an answer. A page of records is a few
// kilobytes.
const maxAnswer = 1 << 20

// do sends one request and decodes the result into out. zone names the
// zone in an error, so a refused token says which zone it was refused for.
func (c *cloudflare) do(ctx context.Context, token, zone, method, path string, body, out any) error {
	_, err := c.send(ctx, token, zone, method, path, body, out)
	return err
}

// doPaged gets a list and says how long the whole of it is.
func (c *cloudflare) doPaged(ctx context.Context, token, path string, out any) (cfResultInfo, error) {
	return c.send(ctx, token, "", http.MethodGet, path, nil, out)
}

func (c *cloudflare) send(ctx context.Context, token, zone, method, path string, body, out any) (cfResultInfo, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return cfResultInfo{}, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return cfResultInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The URL carries no secret, but the error is shorter without it.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return cfResultInfo{}, fmt.Errorf("could not reach Cloudflare: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return cfResultInfo{}, fmt.Errorf("reading Cloudflare's answer: %w", err)
	}
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return cfResultInfo{}, &Error{Message: fmt.Sprintf("Cloudflare answered %s with no API response", resp.Status)}
	}
	if resp.StatusCode/100 != 2 || !env.Success {
		return cfResultInfo{}, refusal(resp, env.Errors, zone)
	}
	if out == nil {
		return env.ResultInfo, nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return cfResultInfo{}, fmt.Errorf("reading Cloudflare's answer: %w", err)
	}
	return env.ResultInfo, nil
}

// refusal says what Cloudflare refused, in the words the page shows.
func refusal(resp *http.Response, errs []cfError, zone string) error {
	var parts []string
	code := 0
	if len(errs) > 0 {
		code = errs[0].Code
	}
	token := resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("%d %s", e.Code, strings.TrimSuffix(e.Message, ".")))
		switch e.Code {
		case 1000, 1001, 6003, 9109, 10000:
			token = true
		}
	}
	detail := strings.Join(parts, "; ")
	if detail == "" {
		detail = resp.Status
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := cloudflareWait
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		return &Error{Message: fmt.Sprintf("Cloudflare asked to wait %s (%s)", wait, detail), RetryAfter: wait, code: code}
	case token && zone == "":
		return &Error{Message: fmt.Sprintf("Cloudflare refused the token (%s)", detail), code: code}
	case token:
		return &Error{Message: fmt.Sprintf("Cloudflare refused the token for %s (%s)", zone, detail), code: code}
	}
	return &Error{Message: fmt.Sprintf("Cloudflare: %s", detail), code: code}
}
