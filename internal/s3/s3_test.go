package s3_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/s3"
	"github.com/rforced/ostiole/internal/s3/s3test"
)

func TestPutAndGetRoundTrip(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	c := b.Client()
	body := []byte("an encrypted backup")
	if err := c.Put(t.Context(), "ostiole/router-20260920-030000.json.age", body, "application/octet-stream"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stored, ok := b.Object("ostiole/router-20260920-030000.json.age")
	if !ok {
		t.Fatalf("nothing was stored; keys are %v", b.Keys())
	}
	if string(stored.Body) != string(body) {
		t.Errorf("stored %q, want %q", stored.Body, body)
	}
	if stored.ContentType != "application/octet-stream" {
		t.Errorf("content type %q", stored.ContentType)
	}
	got, err := c.Get(t.Context(), "ostiole/router-20260920-030000.json.age", 1<<20)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("read back %q, want %q", got, body)
	}
}

func TestGetRefusesAnObjectPastTheLimit(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	c := b.Client()
	if err := c.Put(t.Context(), "big", []byte(strings.Repeat("x", 4096)), ""); err != nil {
		t.Fatal(err)
	}
	before := len(b.Seen())
	_, err := c.Get(t.Context(), "big", 1024)
	if !errors.Is(err, s3.ErrTooLarge) {
		t.Fatalf("Get: %v, want ErrTooLarge", err)
	}
	if n := len(b.Seen()) - before; n != 1 {
		t.Errorf("the bucket saw %d requests, want 1", n)
	}
}

func TestListFollowsTheContinuationTokens(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	c := b.Client()
	when := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	for i := range 5 {
		b.Store(fmt.Sprintf("ostiole/copy-%d", i), []byte("x"), when)
	}
	b.Store("elsewhere/not-ours", []byte("x"), when)

	got, err := c.List(t.Context(), "ostiole/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("listed %d objects, want 5: %v", len(got), got)
	}
	// Three pages of two, so the token was followed twice.
	var lists int
	for _, r := range b.Seen() {
		if strings.Contains(r.Query, "list-type=2") {
			lists++
		}
	}
	if lists != 3 {
		t.Errorf("%d listing requests, want 3", lists)
	}
	if got[0].Key != "ostiole/copy-0" || got[0].Size != 1 {
		t.Errorf("first entry %+v", got[0])
	}
	if !got[0].LastModified.Equal(when) {
		t.Errorf("last modified %v, want %v", got[0].LastModified, when)
	}
}

func TestDeleteRemoves(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	c := b.Client()
	b.Store("ostiole/old", []byte("x"), time.Now())
	if err := c.Delete(t.Context(), "ostiole/old"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if keys := b.Keys(); len(keys) != 0 {
		t.Errorf("keys left: %v", keys)
	}
}

func TestAwkwardKeysRoundTrip(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	c := b.Client()
	key := "ostiole/a copy+of naïve.json.age"
	if err := c.Put(t.Context(), key, []byte("x"), ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := c.List(t.Context(), "ostiole/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Key != key {
		t.Fatalf("listed %+v, want the one key %q", got, key)
	}
	if _, err := c.Get(t.Context(), key, 1<<20); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestLifecycleOnAFreshBucketIsEmpty(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	l, err := b.Client().Lifecycle(t.Context())
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}
	if len(l.Rules) != 0 {
		t.Errorf("rules on a fresh bucket: %+v", l.Rules)
	}
}

func TestLifecycleKeepsAForeignRuleAsItWas(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	c := b.Client()
	const foreign = `<ID>someone-elses</ID><Filter><Prefix>logs/</Prefix></Filter>` +
		`<Status>Enabled</Status><Expiration><Days>7</Days></Expiration>`
	b.SetRules(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Rule>` + foreign + `</Rule></LifecycleConfiguration>`)

	l, err := c.Lifecycle(t.Context())
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}
	l.Set(s3.ExpiryRule("ostiole-ostiole", s3.Expiry{Prefix: "ostiole/", Days: 30, NoncurrentDays: 1}))
	if err := c.PutLifecycle(t.Context(), l); err != nil {
		t.Fatalf("PutLifecycle: %v", err)
	}
	last := b.Seen()[len(b.Seen())-1]
	if last.ContentMD5 == "" {
		t.Error("the lifecycle write carried no Content-MD5")
	}
	if !strings.Contains(last.SignedHeaders, "content-md5") {
		t.Errorf("signed headers %q, want content-md5 among them", last.SignedHeaders)
	}
	if !strings.Contains(string(b.Rules()), foreign) {
		t.Errorf("the foreign rule was rewritten:\n%s", b.Rules())
	}

	back, err := c.Lifecycle(t.Context())
	if err != nil {
		t.Fatalf("Lifecycle: %v", err)
	}
	if len(back.Rules) != 2 {
		t.Fatalf("%d rules came back, want 2", len(back.Rules))
	}
	ours, ok := back.Rule("ostiole-ostiole")
	if !ok {
		t.Fatal("Ostiole's rule did not come back")
	}
	e, err := ours.Expiry()
	if err != nil {
		t.Fatal(err)
	}
	want := s3.Expiry{Prefix: "ostiole/", Days: 30, NoncurrentDays: 1, Status: s3.Enabled}
	if e != want {
		t.Errorf("expiry %+v, want %+v", e, want)
	}
	if !back.Remove("someone-elses") || len(back.Rules) != 1 {
		t.Errorf("Remove left %+v", back.Rules)
	}
}

func TestDeleteLifecycleEmptiesTheBucketsRules(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	c := b.Client()
	b.SetRules(`<LifecycleConfiguration><Rule><ID>x</ID><Status>Enabled</Status></Rule></LifecycleConfiguration>`)
	if err := c.DeleteLifecycle(t.Context()); err != nil {
		t.Fatalf("DeleteLifecycle: %v", err)
	}
	if b.Rules() != nil {
		t.Errorf("rules left: %s", b.Rules())
	}
}

func TestARefusedKeyReadsAsASentence(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	b.FailOnce(s3.OpUpload, http.StatusForbidden, "AccessDenied")
	err := b.Client().Put(t.Context(), "ostiole/copy", []byte("x"), "")
	var serr *s3.Error
	if !errors.As(err, &serr) {
		t.Fatalf("Put: %v, want an *s3.Error", err)
	}
	if got := serr.Error(); got != "the key may not upload" {
		t.Errorf("error = %q", got)
	}
}

func TestAWrongClockSaysSo(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	b.SetSkew(true)
	err := b.Client().Put(t.Context(), "ostiole/copy", []byte("x"), "")
	if got := errString(err); got != "the service rejected the request time; the router's clock is wrong" {
		t.Errorf("error = %q", got)
	}
}

func TestAMissingBucketNamesIt(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	c := b.Client()
	c.Bucket = "not-the-one"
	err := c.Put(t.Context(), "ostiole/copy", []byte("x"), "")
	if got := errString(err); got != "there is no bucket called not-the-one" {
		t.Errorf("error = %q", got)
	}
}

func TestAServiceFaultIsRetriedOnceAndThenSurfaced(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	b.FailOnce(s3.OpUpload, http.StatusServiceUnavailable, "SlowDown")
	c := b.Client()
	// The failure is armed once, so the retry is what stores the object.
	if err := c.Put(t.Context(), "ostiole/copy", []byte("x"), ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := b.Object("ostiole/copy"); !ok {
		t.Error("the retry did not store the object")
	}

	b.FailOnce(s3.OpDelete, http.StatusServiceUnavailable, "SlowDown")
	b.FailOnce(s3.OpDelete, http.StatusServiceUnavailable, "SlowDown")
	err := c.Delete(t.Context(), "ostiole/copy")
	var serr *s3.Error
	if !errors.As(err, &serr) || serr.Status != http.StatusServiceUnavailable {
		t.Fatalf("Delete: %v, want the 503 surfaced", err)
	}
}

func TestARedirectIsNotASuccess(t *testing.T) {
	b := s3test.New(t, "key", "secret")
	// A wrong-region request is answered with a 301 and no Location, which
	// the transport hands back as an ordinary response.
	b.FailOnce(s3.OpUpload, http.StatusMovedPermanently, "PermanentRedirect")
	err := b.Client().Put(t.Context(), "ostiole/copy", []byte("x"), "")
	var serr *s3.Error
	if !errors.As(err, &serr) || serr.Status != http.StatusMovedPermanently {
		t.Fatalf("Put: %v, want the redirect surfaced", err)
	}
	if got := err.Error(); !strings.Contains(got, "another endpoint") {
		t.Errorf("error = %q", got)
	}
	if _, ok := b.Object("ostiole/copy"); ok {
		t.Error("a redirected upload was stored")
	}
}

func TestNewRefusesAnEndpointThatIsNotJustAService(t *testing.T) {
	for _, endpoint := range []string{
		"http://s3.us-west-004.backblazeb2.com",
		"https://s3.us-west-004.backblazeb2.com/my-bucket",
		"https://s3.us-west-004.backblazeb2.com?x=1",
		"s3.us-west-004.backblazeb2.com",
		"",
	} {
		if _, err := s3.New(endpoint, "us-west-004", "bucket", "key", "secret"); err == nil {
			t.Errorf("New(%q) was accepted", endpoint)
		}
	}
	c, err := s3.New("https://s3.us-west-004.backblazeb2.com/", "", "bucket", "key", "secret")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Region != s3.DefaultRegion {
		t.Errorf("region %q, want %q", c.Region, s3.DefaultRegion)
	}
	if c.Endpoint.String() != "https://s3.us-west-004.backblazeb2.com" {
		t.Errorf("endpoint %q", c.Endpoint)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
