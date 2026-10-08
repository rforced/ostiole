// Package s3test plays an S3 service in memory, well enough for a
// backup. Every test in the tree that needs a bucket uses this one, so
// no test anywhere needs a real key.
//
// It keeps versions when told to, as every Backblaze B2 bucket does: a
// PUT adds a version, a plain DELETE adds a hide marker, and only a
// DELETE naming a version removes anything for good.
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

	"ostiole/internal/s3"
	"ostiole/internal/sigv4"
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

// Version is one version the bucket holds of a key.
type Version struct {
	ID string
	// Marker is a hide marker, which a plain delete leaves when the
	// bucket keeps versions.
	Marker bool
}

// version is what the bucket holds: a stored object or a hide marker.
type version struct {
	id     string
	marker bool
	object Stored
}

// Answer is how the bucket answers a request for versions.
type Answer int

const (
	// VersionsListed lists them, as a service that keeps versions does.
	VersionsListed Answer = iota
	// VersionsNotImplemented refuses with a 501.
	VersionsNotImplemented
	// VersionsIgnored lists the objects instead, as a service that has
	// never heard of versions does.
	VersionsIgnored
	// VersionsMarkersFirst lists each page's hide markers before its
	// versions, as Backblaze does. Amazon interleaves them.
	VersionsMarkersFirst
)

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

	mu sync.Mutex
	// objects holds each key's versions, oldest first. Without versions
	// a key has one, called "null".
	objects   map[string][]version
	versioned bool
	answer    Answer
	seq       int
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
		objects: map[string][]version{},
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

// SetVersioned makes the bucket keep versions from now on.
func (b *Bucket) SetVersioned(on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.versioned = on
}

// AnswerVersions says how a request for versions is answered.
func (b *Bucket) AnswerVersions(a Answer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.answer = a
}

// Store seeds an object, as if a previous run had uploaded it.
func (b *Bucket) Store(key string, body []byte, when time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store(key, Stored{Body: body, ContentType: "application/octet-stream", Stored: when.UTC()})
}

// Hide puts a hide marker on a key, as a plain delete does when the
// bucket keeps versions.
func (b *Bucket) Hide(key string, when time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = append(b.objects[key], version{id: b.nextID(), marker: true, object: Stored{Stored: when.UTC()}})
}

// Keys are the names a listing shows, sorted: not hidden ones.
func (b *Bucket) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, k := range slices.Sorted(maps.Keys(b.objects)) {
		if _, ok := b.current(k); ok {
			out = append(out, k)
		}
	}
	return out
}

// Held is every key the bucket holds anything of, hidden versions and
// hide markers included, sorted.
func (b *Bucket) Held() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Sorted(maps.Keys(b.objects))
}

// Versions is what the bucket holds of a key, oldest first.
func (b *Bucket) Versions(key string) []Version {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Version
	for _, v := range b.objects[key] {
		out = append(out, Version{ID: v.id, Marker: v.marker})
	}
	return out
}

// Object is what a read of a key gets.
func (b *Bucket) Object(key string) (Stored, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current(key)
}

// current is a key's newest version, unless that is a hide marker.
func (b *Bucket) current(key string) (Stored, bool) {
	vs := b.objects[key]
	if len(vs) == 0 || vs[len(vs)-1].marker {
		return Stored{}, false
	}
	return vs[len(vs)-1].object, true
}

// store adds a version, or without versions replaces the one there is.
func (b *Bucket) store(key string, o Stored) {
	if !b.versioned {
		b.objects[key] = []version{{id: "null", object: o}}
		return
	}
	b.objects[key] = append(b.objects[key], version{id: b.nextID(), object: o})
}

// nextID names a version. Amazon's ids carry '/', '+' and '=', so these
// do too, and every one a client sends back has been through its
// escaping.
func (b *Bucket) nextID() string {
	b.seq++
	return fmt.Sprintf("v%d/+=", b.seq)
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
// "download", "list", "delete", "list versions", "delete versions", "read
// the retention rule", "write the retention rule".
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
	if err := sigv4.Verify(r, b.keyID, b.secret, body); err != nil {
		b.t.Errorf("s3test: %s %s: %v", r.Method, r.URL.RequestURI(), err)
		b.fail4xx(w, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return
	}
	bucket, key, ok := split(r.URL.Path)
	if !ok || bucket != b.Name {
		b.fail4xx(w, http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist.")
		return
	}
	q := r.URL.Query()
	if q.Has("lifecycle") {
		b.serveLifecycle(w, r, body)
		return
	}
	switch {
	case r.Method == http.MethodGet && key == "" && q.Has("versions"):
		b.listVersions(w, r)
	case r.Method == http.MethodGet && key == "":
		b.list(w, r)
	case r.Method == http.MethodGet:
		b.get(w, key)
	case r.Method == http.MethodPut:
		b.put(w, r, key, body)
	case r.Method == http.MethodDelete && q.Has("versionId"):
		b.removeVersion(w, key, q.Get("versionId"))
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
	b.store(key, Stored{Body: body, ContentType: r.Header.Get("Content-Type"), Stored: b.now})
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
	b.mu.Lock()
	if b.versioned {
		b.objects[key] = append(b.objects[key], version{id: b.nextID(), marker: true, object: Stored{Stored: b.now}})
	} else {
		delete(b.objects, key)
	}
	b.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (b *Bucket) removeVersion(w http.ResponseWriter, key, id string) {
	if b.refuse(w, s3.OpDeleteVersion) {
		return
	}
	b.mu.Lock()
	vs := b.objects[key]
	i := slices.IndexFunc(vs, func(v version) bool { return v.id == id })
	if i >= 0 {
		vs = slices.Delete(vs, i, i+1)
		if len(vs) == 0 {
			delete(b.objects, key)
		} else {
			b.objects[key] = vs
		}
	}
	b.mu.Unlock()
	if i < 0 {
		b.fail4xx(w, http.StatusNotFound, "NoSuchVersion", "The specified version does not exist.")
		return
	}
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
	b.writeList(w, q.Get("prefix"), q.Get("continuation-token"))
}

// writeList answers with a page of the objects a read would find.
func (b *Bucket) writeList(w http.ResponseWriter, prefix, token string) {
	var keys []string
	for _, k := range b.Keys() {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	if token != "" {
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

// listVersions answers ListObjectVersions: every version and hide marker
// under the prefix, by key and each key's newest first, a page at a time.
// The next page starts after the entry the markers name.
func (b *Bucket) listVersions(w http.ResponseWriter, r *http.Request) {
	if b.refuse(w, s3.OpListVersions) {
		return
	}
	q := r.URL.Query()
	prefix := q.Get("prefix")
	b.mu.Lock()
	answer := b.answer
	b.mu.Unlock()
	switch answer {
	case VersionsNotImplemented:
		b.fail4xx(w, http.StatusNotImplemented, "NotImplemented", "Versions are not something this service keeps.")
		return
	case VersionsIgnored:
		b.writeList(w, prefix, "")
		return
	}

	type entry struct {
		key    string
		v      version
		latest bool
	}
	var entries []entry
	b.mu.Lock()
	for _, k := range slices.Sorted(maps.Keys(b.objects)) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		vs := b.objects[k]
		for i := len(vs) - 1; i >= 0; i-- {
			entries = append(entries, entry{key: k, v: vs[i], latest: i == len(vs)-1})
		}
	}
	b.mu.Unlock()
	if key := q.Get("key-marker"); key != "" {
		// After the version the markers name, or after the whole key when
		// they name none.
		id := q.Get("version-id-marker")
		start := slices.IndexFunc(entries, func(e entry) bool { return e.key == key && e.v.id == id }) + 1
		if id == "" || start == 0 {
			start = slices.IndexFunc(entries, func(e entry) bool { return e.key > key })
			if start < 0 {
				start = len(entries)
			}
		}
		entries = entries[start:]
	}
	page, truncated := entries, false
	if len(page) > pageSize {
		page, truncated = entries[:pageSize], true
	}
	// The next page follows on from the service's order, whatever order
	// this one is written in.
	var last entry
	if truncated {
		last = page[len(page)-1]
	}
	if answer == VersionsMarkersFirst {
		page = slices.Clone(page)
		slices.SortStableFunc(page, func(x, y entry) int {
			switch {
			case x.v.marker == y.v.marker:
				return 0
			case x.v.marker:
				return -1
			}
			return 1
		})
	}
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	body.WriteString(`<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&body, "<Name>%s</Name><Prefix>%s</Prefix><MaxKeys>%d</MaxKeys>", b.Name, xmlEscape(prefix), pageSize)
	fmt.Fprintf(&body, "<IsTruncated>%t</IsTruncated>", truncated)
	if truncated {
		fmt.Fprintf(&body, "<NextKeyMarker>%s</NextKeyMarker><NextVersionIdMarker>%s</NextVersionIdMarker>",
			xmlEscape(last.key), xmlEscape(last.v.id))
	}
	for _, e := range page {
		when := e.v.object.Stored.UTC().Format(time.RFC3339)
		if e.v.marker {
			fmt.Fprintf(&body, "<DeleteMarker><Key>%s</Key><VersionId>%s</VersionId><IsLatest>%t</IsLatest>"+
				"<LastModified>%s</LastModified></DeleteMarker>", xmlEscape(e.key), xmlEscape(e.v.id), e.latest, when)
			continue
		}
		fmt.Fprintf(&body, "<Version><Key>%s</Key><VersionId>%s</VersionId><IsLatest>%t</IsLatest>"+
			"<LastModified>%s</LastModified><Size>%d</Size></Version>",
			xmlEscape(e.key), xmlEscape(e.v.id), e.latest, when, len(e.v.object.Body))
	}
	body.WriteString("</ListVersionsResult>")
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
