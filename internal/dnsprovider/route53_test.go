package dnsprovider

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/sigv4"
)

const (
	r53KeyID  = "AKIAIOSFODNN7EXAMPLE"
	r53Secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

// fakeRoute53 keeps record sets the way Route 53 does: one set per name
// and type, an UPSERT writes it whole, a DELETE must name it exactly, and
// a change is PENDING until it has been asked after once. Every request
// has its signature checked.
type fakeRoute53 struct {
	t *testing.T

	mu       sync.Mutex
	sets     map[string]*r53Set // "zone id|name" → set
	changes  map[string]int
	next     int
	throttle int
	calls    []string
	comments []string
}

func newFakeRoute53(t *testing.T) (*fakeRoute53, *httptest.Server) {
	t.Helper()
	f := &fakeRoute53{t: t, sets: map[string]*r53Set{}, changes: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

// zones are the hosted zones the fake holds, a private one among them.
var fakeZones = []struct{ id, name string }{
	{"Z0PRIVATE", "example.com."},
	{"Z1D633PJN98FT9", "example.com."},
	{"Z2OTHER", "example.net."},
}

func (f *fakeRoute53) fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0"?>
<ErrorResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>r</RequestId></ErrorResponse>`, code, message)
}

func (f *fakeRoute53) reply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/xml")
	_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+body)
}

func (f *fakeRoute53) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if err := sigv4.Verify(r, r53KeyID, r53Secret, body); err != nil {
		f.fail(w, http.StatusForbidden, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.")
		return
	}
	if !strings.Contains(r.Header.Get("Authorization"), "/us-east-1/route53/aws4_request") {
		f.t.Errorf("signed for %q", r.Header.Get("Authorization"))
	}
	if f.throttle > 0 {
		f.throttle--
		f.fail(w, http.StatusBadRequest, "Throttling", "Rate exceeded")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/2013-04-01/hostedzonesbyname":
		var b strings.Builder
		for _, z := range fakeZones {
			fmt.Fprintf(&b, "<HostedZone><Id>/hostedzone/%s</Id><Name>%s</Name><Config><PrivateZone>%v</PrivateZone></Config></HostedZone>", z.id, z.name, z.id == "Z0PRIVATE")
		}
		f.reply(w, `<ListHostedZonesByNameResponse><HostedZones>`+b.String()+`</HostedZones><IsTruncated>false</IsTruncated><MaxItems>100</MaxItems></ListHostedZonesByNameResponse>`)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "change":
		f.changes[parts[2]]++
		status := "PENDING"
		if f.changes[parts[2]] > 1 {
			status = "INSYNC"
		}
		f.reply(w, fmt.Sprintf(`<GetChangeResponse><ChangeInfo><Id>/change/%s</Id><Status>%s</Status></ChangeInfo></GetChangeResponse>`, parts[2], status))
	case r.Method == http.MethodGet && len(parts) == 4 && parts[3] == "rrset":
		var out []string
		// The list starts at the name; a set there comes first.
		if set, ok := f.sets[parts[2]+"|"+r.URL.Query().Get("name")]; ok {
			raw, _ := xml.Marshal(set)
			out = append(out, strings.ReplaceAll(string(raw), "r53Set", "ResourceRecordSet"))
		} else {
			out = append(out, `<ResourceRecordSet><Name>zzz.example.com.</Name><Type>A</Type><TTL>300</TTL><ResourceRecords><ResourceRecord><Value>192.0.2.1</Value></ResourceRecord></ResourceRecords></ResourceRecordSet>`)
		}
		f.reply(w, `<ListResourceRecordSetsResponse><ResourceRecordSets>`+strings.Join(out, "")+`</ResourceRecordSets><IsTruncated>false</IsTruncated><MaxItems>1</MaxItems></ListResourceRecordSetsResponse>`)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[3] == "rrset":
		var req struct {
			Comment string `xml:"ChangeBatch>Comment"`
			Changes []struct {
				Action string `xml:"Action"`
				Set    r53Set `xml:"ResourceRecordSet"`
			} `xml:"ChangeBatch>Changes>Change"`
		}
		if err := xml.Unmarshal(body, &req); err != nil {
			f.fail(w, http.StatusBadRequest, "MalformedInput", err.Error())
			return
		}
		f.comments = append(f.comments, req.Comment)
		for _, ch := range req.Changes {
			key := parts[2] + "|" + ch.Set.Name
			switch ch.Action {
			case "UPSERT":
				set := ch.Set
				f.sets[key] = &set
			case "DELETE":
				held, ok := f.sets[key]
				if !ok || held.TTL != ch.Set.TTL || !slices.Equal(values(held), values(&ch.Set)) {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `<InvalidChangeBatch><Messages><Message>Tried to delete resource record set [name='%s', type='TXT'] but the values provided do not match the current values</Message></Messages></InvalidChangeBatch>`, ch.Set.Name)
					return
				}
				delete(f.sets, key)
			}
		}
		f.next++
		f.reply(w, fmt.Sprintf(`<ChangeResourceRecordSetsResponse><ChangeInfo><Id>/change/C%d</Id><Status>PENDING</Status></ChangeInfo></ChangeResourceRecordSetsResponse>`, f.next))
	default:
		f.fail(w, http.StatusNotFound, "NoSuchHostedZone", "No hosted zone found")
	}
}

// held is the TXT set at name in the public zone as "value ttl" lines.
func (f *fakeRoute53) held(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	set, ok := f.sets["Z1D633PJN98FT9|"+name]
	if !ok {
		return nil
	}
	var out []string
	for _, rec := range set.Records {
		out = append(out, fmt.Sprintf("%s %d", rec.Value, set.TTL))
	}
	slices.Sort(out)
	return out
}

func route53Client(t *testing.T, srv *httptest.Server, settings map[string]string) *route53 {
	t.Helper()
	full := map[string]string{"accessKeyId": r53KeyID, "secretAccessKey": r53Secret}
	for k, v := range settings {
		full[k] = v
	}
	c, err := Build(model.DNSProvider{ID: "r53", Kind: "route53", Settings: full}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := c.(*route53)
	r.base, r.poll = srv.URL, 10*time.Millisecond
	return r
}

// Values go into one set and come out of it one at a time; the last one
// out deletes the set. Each change is waited for until it is INSYNC.
func TestRoute53KeepsTheSet(t *testing.T) {
	t.Parallel()
	f, srv := newFakeRoute53(t)
	c := route53Client(t, srv, nil)
	ctx := context.Background()
	name := "_acme-challenge.www.example.com"
	for _, v := range []string{"one", "two", "two"} {
		if err := c.AddTXT(ctx, txtAt(name, v)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := f.held(name+"."), []string{`"one" 10`, `"two" 10`}; !slices.Equal(got, want) {
		t.Errorf("held %q, want %q", got, want)
	}
	for _, v := range []string{"one", "two", "two"} {
		if err := c.RemoveTXT(ctx, txtAt(name, v)); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.held(name + "."); got != nil {
		t.Errorf("held %q after the last", got)
	}
	for id, asked := range f.changes {
		if asked < 2 {
			t.Errorf("change %s was asked after %d times, want until it was INSYNC", id, asked)
		}
	}
	if f.comments[0] != "ACME challenge from Ostiole" {
		t.Errorf("comment %q", f.comments[0])
	}
	// The public zone was looked up once, past the private one.
	lookups := 0
	for _, call := range f.calls {
		if call == "GET /2013-04-01/hostedzonesbyname" {
			lookups++
		}
	}
	if lookups != 1 {
		t.Errorf("%d zone lookups", lookups)
	}
}

// A set someone else made keeps its TTL, and the DELETE of the last value
// names it as it is.
func TestRoute53KeepsAnotherSetsTTL(t *testing.T) {
	t.Parallel()
	f, srv := newFakeRoute53(t)
	name := "_acme-challenge.example.com."
	f.sets["Z1D633PJN98FT9|"+name] = &r53Set{Name: name, Type: "TXT", TTL: 300, Records: []r53Record{{Value: `"theirs"`}}}
	c := route53Client(t, srv, map[string]string{"hostedZoneId": "/hostedzone/Z1D633PJN98FT9"})
	ctx := context.Background()
	if err := c.AddTXT(ctx, txtAt(name, "ours")); err != nil {
		t.Fatal(err)
	}
	if got, want := f.held(name), []string{`"ours" 300`, `"theirs" 300`}; !slices.Equal(got, want) {
		t.Errorf("held %q, want %q", got, want)
	}
	if err := c.RemoveTXT(ctx, txtAt(name, "ours")); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveTXT(ctx, txtAt(name, "theirs")); err != nil {
		t.Fatal(err)
	}
	if got := f.held(name); got != nil {
		t.Errorf("held %q", got)
	}
	if slices.Contains(f.calls, "GET /2013-04-01/hostedzonesbyname") {
		t.Error("the zone was looked up although the provider names it")
	}
}

// A throttled request goes again.
func TestRoute53WaitsWhenThrottled(t *testing.T) {
	t.Parallel()
	f, srv := newFakeRoute53(t)
	f.throttle = 2
	if err := route53Client(t, srv, nil).AddTXT(context.Background(), txtAt("_acme-challenge.example.com", "v")); err != nil {
		t.Fatal(err)
	}
}

func TestRoute53SaysWhatWentWrong(t *testing.T) {
	t.Parallel()
	_, srv := newFakeRoute53(t)
	err := route53Client(t, srv, map[string]string{"secretAccessKey": "wrong-secret"}).AddTXT(context.Background(), txtAt("_acme-challenge.example.com", "v"))
	if err == nil || err.Error() != "Route 53 refused the key (SignatureDoesNotMatch: The request signature we calculated does not match the signature you provided)" {
		t.Errorf("err = %v", err)
	}
	err = route53Client(t, srv, nil).AddTXT(context.Background(), Record{Zone: "example.org", Name: "_acme-challenge.example.org", Value: "v"})
	if err == nil || err.Error() != "Route 53 holds no public hosted zone example.org that this key can see" {
		t.Errorf("err = %v", err)
	}
	for _, region := range []string{"cn-north-1", "us-gov-west-1"} {
		_, err := Build(model.DNSProvider{ID: "r53", Kind: "route53", Settings: map[string]string{
			"accessKeyId": r53KeyID, "secretAccessKey": r53Secret, "region": region,
		}}, Options{})
		if err == nil || !strings.Contains(err.Error(), "global endpoint only") {
			t.Errorf("%s: err = %v", region, err)
		}
	}
}
