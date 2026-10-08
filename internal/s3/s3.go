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

	"ostiole/internal/sigv4"
	"ostiole/internal/version"
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

var (
	// ErrTooLarge says the object is bigger than the caller allowed for.
	ErrTooLarge = errors.New("the object is larger than allowed")
	// ErrNoVersions says the service keeps no versions, so a plain delete
	// already removes an object for good.
	ErrNoVersions = errors.New("the service keeps no versions")
)

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

// Version is one entry of a version listing: a version of an object, or
// the hide marker a plain delete leaves on a bucket that keeps versions.
type Version struct {
	Key string
	// ID is what DeleteVersion takes. A bucket that has never kept
	// versions calls each object's only one "null".
	ID string
	// Latest is the newest version of its key.
	Latest bool
	// Marker is a hide marker rather than a stored version.
	Marker       bool
	Size         int64
	LastModified time.Time
}

// Versions reads every version under a prefix, hide markers included,
// following the markers to the end. They come in the service's order:
// each kind by key and each key's newest first, but not always
// interleaved, since Backblaze lists a page's hide markers before its
// versions. A service that keeps no versions is ErrNoVersions, whether it
// says so or answers with a plain listing.
func (c *Client) Versions(ctx context.Context, prefix string) ([]Version, error) {
	out := []Version{}
	keyMarker, idMarker := "", ""
	for range maxPages {
		query := "versions&max-keys=1000"
		if prefix != "" {
			query += "&prefix=" + sigv4.Escape(prefix)
		}
		if keyMarker != "" {
			query += "&key-marker=" + sigv4.Escape(keyMarker)
		}
		if idMarker != "" {
			query += "&version-id-marker=" + sigv4.Escape(idMarker)
		}
		body, err := c.do(ctx, request{method: http.MethodGet, query: query, op: OpListVersions, limit: maxListBytes})
		if e, ok := errors.AsType[*Error](err); ok && (e.Code == "NotImplemented" || e.Status == http.StatusNotImplemented) {
			return nil, ErrNoVersions
		}
		if err != nil {
			return nil, err
		}
		var page struct {
			XMLName     xml.Name
			IsTruncated bool   `xml:"IsTruncated"`
			NextKey     string `xml:"NextKeyMarker"`
			NextID      string `xml:"NextVersionIdMarker"`
			// Versions and hide markers are read as one list, in the
			// order the service sent them.
			Entries []struct {
				XMLName      xml.Name
				Key          string    `xml:"Key"`
				ID           string    `xml:"VersionId"`
				Latest       bool      `xml:"IsLatest"`
				Size         int64     `xml:"Size"`
				LastModified time.Time `xml:"LastModified"`
			} `xml:",any"`
		}
		if err := xml.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("the version listing the service sent could not be read: %w", err)
		}
		// A service that ignores ?versions answers with a plain listing.
		if page.XMLName.Local != "ListVersionsResult" {
			return nil, ErrNoVersions
		}
		for _, e := range page.Entries {
			kind := e.XMLName.Local
			if kind != "Version" && kind != "DeleteMarker" {
				continue
			}
			out = append(out, Version{
				Key: e.Key, ID: e.ID, Latest: e.Latest, Marker: kind == "DeleteMarker",
				Size: e.Size, LastModified: e.LastModified.UTC(),
			})
		}
		if !page.IsTruncated || page.NextKey == "" {
			return out, nil
		}
		keyMarker, idMarker = page.NextKey, page.NextID
	}
	return out, errors.New("the version listing did not end")
}

// DeleteVersion removes one version of an object, or one hide marker,
// for good.
func (c *Client) DeleteVersion(ctx context.Context, key, id string) error {
	_, err := c.do(ctx, request{
		method: http.MethodDelete, key: key, query: "versionId=" + sigv4.Escape(id), op: OpDeleteVersion,
	})
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
			query += "&prefix=" + sigv4.Escape(prefix)
		}
		if token != "" {
			query += "&continuation-token=" + sigv4.Escape(token)
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
	// A service that does not implement something will not five seconds
	// later either.
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Status >= 500 && e.Status != http.StatusNotImplemented
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
	u.RawPath = sigv4.EncodePath(path)
	u.RawQuery = r.query

	req, err := http.NewRequestWithContext(ctx, r.method, c.Endpoint.String(), bytes.NewReader(r.body))
	if err != nil {
		return nil, err
	}
	req.URL = &u
	req.ContentLength = int64(len(r.body))
	agent := c.UserAgent
	if agent == "" {
		agent = version.Agent
	}
	req.Header.Set("User-Agent", agent)
	if r.body != nil {
		req.Header.Set("Content-Type", r.contentType)
		req.Header.Set("Content-Length", strconv.Itoa(len(r.body)))
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	sigv4.Sign(req, c.KeyID, c.Secret, c.Region, "s3", sigv4.HashOf(r.body), c.now())

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	// A redirect is an answer, never followed: a 301 or 302 with a
	// Location turns an upload into a GET of somewhere else, whose 200
	// would read as stored.
	once := *client
	once.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := once.Do(req)
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
