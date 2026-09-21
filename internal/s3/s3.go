// Package s3 is the little of the S3 API a backup needs, signed with
// Signature Version 4 on net/http. It speaks to any S3-compatible
// service; Backblaze B2 is the one it was written against.
//
// Requests are path-style and https only. The bodies are small enough to
// hold in memory, so every request is signed over its real payload hash
// rather than sent unsigned.
package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds one request. A backup is a few tens of kilobytes;
// anything this slow is a service that is not answering.
const DefaultTimeout = 60 * time.Second

// DefaultRegion is what a service that has no regions is told it is.
// Every S3-compatible implementation accepts it.
const DefaultRegion = "us-east-1"

const (
	// defaultRetry is the pause before the one retry.
	defaultRetry = 5 * time.Second
	// maxErrorBytes bounds an error document, which is a few lines of XML
	// unless something in the way is answering with a web page.
	maxErrorBytes = 64 << 10
	// maxListBytes bounds a listing page and a lifecycle document.
	maxListBytes = 8 << 20
	// maxPages bounds a listing, so a service that keeps handing back a
	// continuation token cannot spin here forever.
	maxPages = 100
)

// ErrTooLarge says the object is bigger than the caller allowed for.
var ErrTooLarge = errors.New("the object is larger than allowed")

// Client addresses one bucket at one endpoint.
type Client struct {
	// Endpoint is scheme and host only; path-style requests are built
	// under it.
	Endpoint *url.URL
	Region   string
	Bucket   string
	KeyID    string
	Secret   string
	// HTTP is the transport; nil is a client with DefaultTimeout.
	HTTP      *http.Client
	UserAgent string
	// Now is the clock the requests are signed against; nil is time.Now.
	Now func() time.Time
	// Retry is the pause before the one retry; zero is defaultRetry.
	// Tests shorten it.
	Retry time.Duration
}

// New builds a client for one bucket. The endpoint is the service and
// nothing else: https, a host name, no path.
func New(endpoint, region, bucket, keyID, secret string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return nil, fmt.Errorf("%q is not a URL: %w", endpoint, err)
	}
	if u.Scheme != "https" {
		return nil, errors.New("the endpoint has to be an https:// URL")
	}
	if u.Host == "" {
		return nil, errors.New("the endpoint has no host name")
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("the endpoint is the service, without a path")
	}
	if bucket == "" {
		return nil, errors.New("no bucket was named")
	}
	if region == "" {
		region = DefaultRegion
	}
	return &Client{
		Endpoint: &url.URL{Scheme: u.Scheme, Host: u.Host},
		Region:   region,
		Bucket:   bucket,
		KeyID:    keyID,
		Secret:   secret,
	}, nil
}

// Object is one entry of a listing.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Put writes an object.
func (c *Client) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := c.do(ctx, request{
		method: http.MethodPut, key: key, body: body, contentType: contentType, op: OpUpload,
	})
	return err
}

// Get reads an object, refusing one longer than limit rather than
// holding it.
func (c *Client) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return c.do(ctx, request{method: http.MethodGet, key: key, op: OpDownload, limit: limit})
}

// Delete removes an object. On a bucket that keeps versions this hides
// the newest one, which is what the retention rule is for.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.do(ctx, request{method: http.MethodDelete, key: key, op: OpDelete})
	return err
}

// List reads every object under a prefix, following the continuation
// tokens to the end.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	out := []Object{}
	token := ""
	for range maxPages {
		query := "list-type=2&max-keys=1000"
		if prefix != "" {
			query += "&prefix=" + escape(prefix)
		}
		if token != "" {
			query += "&continuation-token=" + escape(token)
		}
		body, err := c.do(ctx, request{method: http.MethodGet, query: query, op: OpList, limit: maxListBytes})
		if err != nil {
			return nil, err
		}
		var page struct {
			IsTruncated bool   `xml:"IsTruncated"`
			Next        string `xml:"NextContinuationToken"`
			Contents    []struct {
				Key          string    `xml:"Key"`
				Size         int64     `xml:"Size"`
				LastModified time.Time `xml:"LastModified"`
			} `xml:"Contents"`
		}
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("the listing the service sent could not be read: %w", err)
		}
		for _, o := range page.Contents {
			out = append(out, Object{Key: o.Key, Size: o.Size, LastModified: o.LastModified.UTC()})
		}
		if !page.IsTruncated || page.Next == "" {
			return out, nil
		}
		token = page.Next
	}
	return out, errors.New("the listing did not end")
}

// request is one call, before it becomes an http.Request.
type request struct {
	method      string
	key         string
	query       string
	body        []byte
	contentType string
	headers     map[string]string
	op          string
	// limit bounds the answer; zero means an answer that is not read.
	limit int64
}

// do makes the request, retrying once on a service that did not answer
// or answered with a fault of its own. A refusal is not retried: a key
// that may not upload will not be allowed to five seconds later.
func (c *Client) do(ctx context.Context, r request) ([]byte, error) {
	body, err := c.attempt(ctx, r)
	if err == nil || !retryable(err) {
		return body, err
	}
	retry := c.Retry
	if retry <= 0 {
		retry = defaultRetry
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(retry):
	}
	return c.attempt(ctx, r)
}

func retryable(err error) bool {
	if errors.Is(err, ErrTooLarge) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Status >= 500
	}
	return true
}

func (c *Client) attempt(ctx context.Context, r request) ([]byte, error) {
	path := "/" + c.Bucket
	if r.key != "" {
		path += "/" + r.key
	}
	u := *c.Endpoint
	u.Path = path
	// The path goes on the wire exactly as it was signed; Go's own
	// escaping leaves characters alone that S3 expects encoded.
	u.RawPath = encodePath(path)
	u.RawQuery = r.query

	req, err := http.NewRequestWithContext(ctx, r.method, c.Endpoint.String(), bytes.NewReader(r.body))
	if err != nil {
		return nil, err
	}
	req.URL = &u
	req.ContentLength = int64(len(r.body))
	agent := c.UserAgent
	if agent == "" {
		agent = "ostiole"
	}
	req.Header.Set("User-Agent", agent)
	if r.body != nil {
		req.Header.Set("Content-Type", r.contentType)
		req.Header.Set("Content-Length", strconv.Itoa(len(r.body)))
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	sign(req, c.KeyID, c.Secret, c.Region, hashOf(r.body), c.now())

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not %s: %w", r.op, err)
	}
	defer func() { _ = res.Body.Close() }()

	// Anything but a 2xx is a failure, a redirect included. A service
	// answering a wrong-region request with a 301 and no Location header
	// comes back from the transport as an ordinary response, and it has
	// stored nothing.
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBytes))
		return nil, c.errorFrom(res.StatusCode, r.op, raw)
	}
	if r.limit <= 0 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxErrorBytes))
		return nil, nil
	}
	out, err := io.ReadAll(io.LimitReader(res.Body, r.limit+1))
	if err != nil {
		return nil, fmt.Errorf("could not %s: %w", r.op, err)
	}
	if int64(len(out)) > r.limit {
		return nil, fmt.Errorf("%w: %d bytes at most", ErrTooLarge, r.limit)
	}
	return out, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
