// Package s3test plays an S3 service in memory, well enough for a
// backup. Every test in the tree that needs a bucket uses this one, so
// no test anywhere needs a real key.
package s3test

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/s3"
)

// pageSize is deliberately small, so a listing of a handful of objects
// still goes round the continuation token.
const pageSize = 2

// Stored is one object in the bucket.
type Stored struct {
	Body        []byte
	ContentType string
	Stored      time.Time
}

// Request is what the bucket was asked to do.
type Request struct {
	Method        string
	Path          string
	Query         string
	SignedHeaders string
	ContentMD5    string
}

// Failure is an S3 error one operation answers with.
type Failure struct {
	Status int
	Code   string
}

// Bucket is the service. It is read from the test goroutine while it is
// serving on another, so everything on it goes through a method.
type Bucket struct {
	Server *httptest.Server
	Name   string

	t      testing.TB
	keyID  string
	secret string
	now    time.Time

	mu        sync.Mutex
	objects   map[string]Stored
	lifecycle []byte
	requests  []Request
	fail      map[string][]Failure
	skew      bool
}

// New starts a bucket and stops it when the test ends.
func New(t testing.TB, keyID, secret string) *Bucket {
	t.Helper()
	b := &Bucket{
		Name:    "test-bucket",
		t:       t,
		keyID:   keyID,
		secret:  secret,
		now:     time.Now().UTC().Truncate(time.Second),
		objects: map[string]Stored{},
		fail:    map[string][]Failure{},
	}
	b.Server = httptest.NewServer(http.HandlerFunc(b.serve))
	t.Cleanup(b.Server.Close)
	return b
}

// Client addresses the bucket over its own transport, signed the way a
// real one is. The endpoint is built rather than parsed because the test
// server speaks plain HTTP and s3.New insists on https.
func (b *Bucket) Client() *s3.Client {
	u, err := url.Parse(b.Server.URL)
	if err != nil {
		b.t.Fatalf("s3test: %v", err)
	}
	return &s3.Client{
		Endpoint:  &url.URL{Scheme: u.Scheme, Host: u.Host},
		Region:    "us-west-004",
		Bucket:    b.Name,
		KeyID:     b.keyID,
		Secret:    b.secret,
		HTTP:      b.Server.Client(),
		UserAgent: "ostiole/test",
		Retry:     time.Millisecond,
	}
}

// Store seeds an object, as if a previous run had uploaded it.
func (b *Bucket) Store(key string, body []byte, when time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = Stored{Body: body, ContentType: "application/octet-stream", Stored: when.UTC()}
}

// Keys are the object names, sorted.
func (b *Bucket) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Sorted(maps.Keys(b.objects))
}

// Object is what is stored under a key.
func (b *Bucket) Object(key string) (Stored, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.objects[key]
	return o, ok
}

// Seen is every request the bucket has been sent.
func (b *Bucket) Seen() []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.requests)
}

// Rules is the last rule set written, or nil.
func (b *Bucket) Rules() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.lifecycle)
}

// SetRules puts a rule set in the bucket, as if something else had.
func (b *Bucket) SetRules(doc string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lifecycle = []byte(doc)
}

// FailOnce makes the next call of one operation answer with an S3 error.
// Called twice it refuses twice, which is how the retry is tested. The op
// names are the ones the client names its requests with: "upload",
// "download", "list", "delete", "read the retention rule", "write the
// retention rule".
func (b *Bucket) FailOnce(op string, status int, code string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail[op] = append(b.fail[op], Failure{Status: status, Code: code})
}

// SetSkew makes every request fail the way a wrong clock does.
func (b *Bucket) SetSkew(on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.skew = on
}

func (b *Bucket) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		b.fail4xx(w, http.StatusBadRequest, "MalformedXML", err.Error())
		return
	}
	b.mu.Lock()
	b.requests = append(b.requests, Request{
		Method:        r.Method,
		Path:          r.URL.Path,
		Query:         r.URL.RawQuery,
		SignedHeaders: signedHeaders(r.Header.Get("Authorization")),
		ContentMD5:    r.Header.Get("Content-MD5"),
	})
	skew := b.skew
	b.mu.Unlock()

	if skew {
		b.fail4xx(w, http.StatusForbidden, "RequestTimeTooSkewed",
			"The difference between the request time and the current time is too large.")
		return
	}
	if err := s3.Verify(r, b.keyID, b.secret, body); err != nil {
		b.t.Errorf("s3test: %s %s: %v", r.Method, r.URL.RequestURI(), err)
		b.fail4xx(w, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return
	}
	bucket, key, ok := split(r.URL.Path)
	if !ok || bucket != b.Name {
		b.fail4xx(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist.")
		return
	}
	if r.URL.Query().Has("lifecycle") {
		b.serveLifecycle(w, r, body)
		return
	}
	switch {
	case r.Method == http.MethodGet && key == "":
		b.list(w, r)
	case r.Method == http.MethodGet:
		b.get(w, key)
	case r.Method == http.MethodPut:
		b.put(w, r, key, body)
	case r.Method == http.MethodDelete:
		b.remove(w, key)
	default:
		b.fail4xx(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "That is not something a bucket does.")
	}
}

func (b *Bucket) put(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	if b.refuse(w, s3.OpUpload) {
		return
	}
	b.mu.Lock()
	b.objects[key] = Stored{Body: body, ContentType: r.Header.Get("Content-Type"), Stored: b.now}
	b.mu.Unlock()
	w.Header().Set("ETag", `"`+strconv.Itoa(len(body))+`"`)
	w.WriteHeader(http.StatusOK)
}

func (b *Bucket) get(w http.ResponseWriter, key string) {
	if b.refuse(w, s3.OpDownload) {
		return
	}
	o, ok := b.Object(key)
	if !ok {
		b.fail4xx(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		return
	}
	w.Header().Set("Content-Type", o.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(o.Body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(o.Body)
}

func (b *Bucket) remove(w http.ResponseWriter, key string) {
	if b.refuse(w, s3.OpDelete) {
		return
	}
	// A versioned bucket would hide the newest version and keep the
	// bytes; the retention rule is what deals with that, and emulating it
	// here would test nothing.
	b.mu.Lock()
	delete(b.objects, key)
	b.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (b *Bucket) list(w http.ResponseWriter, r *http.Request) {
	if b.refuse(w, s3.OpList) {
		return
	}
	q := r.URL.Query()
	if q.Get("list-type") != "2" {
		b.fail4xx(w, http.StatusBadRequest, "InvalidArgument", "Only ListObjectsV2 is served here.")
		return
	}
	prefix := q.Get("prefix")
	var keys []string
	for _, k := range b.Keys() {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	if token := q.Get("continuation-token"); token != "" {
		after, err := base64.StdEncoding.DecodeString(token)
		if err != nil {
			b.fail4xx(w, http.StatusBadRequest, "InvalidToken", "That continuation token is not one of ours.")
			return
		}
		for len(keys) > 0 && keys[0] != string(after) {
			keys = keys[1:]
		}
	}
	page, truncated := keys, false
	if len(page) > pageSize {
		page, truncated = keys[:pageSize], true
	}
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	body.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&body, "<Name>%s</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount>",
		b.Name, xmlEscape(prefix), len(page))
	fmt.Fprintf(&body, "<IsTruncated>%t</IsTruncated>", truncated)
	if truncated {
		next := base64.StdEncoding.EncodeToString([]byte(keys[pageSize]))
		fmt.Fprintf(&body, "<NextContinuationToken>%s</NextContinuationToken>", xmlEscape(next))
	}
	for _, k := range page {
		o, _ := b.Object(k)
		fmt.Fprintf(&body, "<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size></Contents>",
			xmlEscape(k), o.Stored.UTC().Format(time.RFC3339), len(o.Body))
	}
	body.WriteString("</ListBucketResult>")
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body.String())
}

func (b *Bucket) serveLifecycle(w http.ResponseWriter, r *http.Request, body []byte) {
	switch r.Method {
	case http.MethodGet:
		if b.refuse(w, s3.OpReadRule) {
			return
		}
		rules := b.Rules()
		if rules == nil {
			b.fail4xx(w, http.StatusNotFound, "NoSuchLifecycleConfiguration",
				"The lifecycle configuration does not exist.")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(rules)
	case http.MethodPut:
		if b.refuse(w, s3.OpWriteRule) {
			return
		}
		if r.Header.Get("Content-MD5") == "" {
			b.fail4xx(w, http.StatusBadRequest, "InvalidRequest", "A Content-MD5 is required here.")
			return
		}
		b.mu.Lock()
		b.lifecycle = body
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if b.refuse(w, s3.OpWriteRule) {
			return
		}
		b.mu.Lock()
		b.lifecycle = nil
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		b.fail4xx(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "That is not something a bucket does.")
	}
}

// refuse answers with the failure set for an operation, once.
func (b *Bucket) refuse(w http.ResponseWriter, op string) bool {
	b.mu.Lock()
	queued := b.fail[op]
	if len(queued) == 0 {
		b.mu.Unlock()
		return false
	}
	f := queued[0]
	b.fail[op] = queued[1:]
	b.mu.Unlock()
	b.fail4xx(w, f.Status, f.Code, "The bucket was told to refuse this.")
	return true
}

func (b *Bucket) fail4xx(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>",
		xmlEscape(code), xmlEscape(message))
}

// split takes the bucket and the key out of a path-style request.
func split(path string) (bucket, key string, ok bool) {
	rest, found := strings.CutPrefix(path, "/")
	if !found {
		return "", "", false
	}
	bucket, key, _ = strings.Cut(rest, "/")
	return bucket, key, bucket != ""
}

func signedHeaders(auth string) string {
	for part := range strings.SplitSeq(auth, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "SignedHeaders="); ok {
			return v
		}
	}
	return ""
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
