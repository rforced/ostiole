package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

type fakeCloudflare struct {
	t         *testing.T
	token     string
	zoneToken string

	// extraZones are zones past the first page, counted but not listed.
	extraZones int

	mu      sync.Mutex
	zones   map[string]string // name → id
	records map[string]*fakeRecord
	next    int
	calls   []string
	patched []map[string]any
	posted  []map[string]any
	deleted []string
}

type fakeRecord struct {
	ID      string `json:"id"`
	Zone    string `json:"-"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
}

func newFakeCloudflare(t *testing.T) (*fakeCloudflare, *httptest.Server) {
	t.Helper()
	f := &fakeCloudflare{t: t, token: "dns-token", zones: map[string]string{"example.com": "zone1"}, records: map[string]*fakeRecord{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeCloudflare) add(typ, name, content string, ttl int, proxied bool, comment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("rec%d", f.next)
	f.records[id] = &fakeRecord{ID: id, Zone: "zone1", Type: typ, Name: name, Content: content, TTL: ttl, Proxied: proxied, Comment: comment}
}

func (f *fakeCloudflare) reply(w http.ResponseWriter, status int, result any, errs ...map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if errs == nil {
		errs = []map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": status/100 == 2, "errors": errs, "messages": []any{}, "result": result,
	})
}

// replyList answers a list with the count of the whole of it.
func (f *fakeCloudflare) replyList(w http.ResponseWriter, result any, total int) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true, "errors": []any{}, "messages": []any{}, "result": result,
		"result_info": map[string]any{"page": 1, "total_count": total},
	})
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/zones":
		want := f.token
		if f.zoneToken != "" {
			want = f.zoneToken
		}
		if auth != want {
			f.reply(w, http.StatusForbidden, nil, map[string]any{"code": 9109, "message": "Unauthorized to access requested resource"})
			return
		}
		out := []map[string]any{}
		for name, id := range f.zones {
			if want := r.URL.Query().Get("name"); want == "" || want == name {
				out = append(out, map[string]any{"id": id, "name": name})
			}
		}
		f.replyList(w, out, len(out)+f.extraZones)
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		if auth != f.token {
			f.reply(w, http.StatusUnauthorized, nil, map[string]any{"code": 10000, "message": "Authentication error"})
			return
		}
		f.serveRecords(w, r, parts[1], parts[3:])
	default:
		f.reply(w, http.StatusNotFound, nil, map[string]any{"code": 7003, "message": "Could not route"})
	}
}

func (f *fakeCloudflare) serveRecords(w http.ResponseWriter, r *http.Request, zone string, rest []string) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		out := []*fakeRecord{}
		for _, rec := range f.records {
			if rec.Zone == zone && rec.Type == q.Get("type") && strings.EqualFold(rec.Name, q.Get("name")) {
				out = append(out, rec)
			}
		}
		slices.SortFunc(out, func(a, b *fakeRecord) int { return strings.Compare(a.ID, b.ID) })
		f.reply(w, http.StatusOK, out)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("POST body: %v", err)
		}
		f.posted = append(f.posted, body)
		rec := &fakeRecord{Zone: zone, TTL: 1}
		rec.Type, _ = body["type"].(string)
		rec.Name, _ = body["name"].(string)
		rec.Content, _ = body["content"].(string)
		rec.Comment, _ = body["comment"].(string)
		if ttl, ok := body["ttl"].(float64); ok {
			rec.TTL = int(ttl)
		}
		for _, old := range f.records {
			if old.Zone == zone && old.Type == rec.Type && strings.EqualFold(old.Name, rec.Name) && old.Content == rec.Content {
				f.reply(w, http.StatusBadRequest, nil, map[string]any{"code": 81058, "message": "An identical record already exists."})
				return
			}
		}
		f.next++
		rec.ID = fmt.Sprintf("rec%d", f.next)
		f.records[rec.ID] = rec
		f.reply(w, http.StatusOK, rec)
	case http.MethodDelete:
		id := strings.Join(rest, "")
		if _, ok := f.records[id]; !ok {
			f.reply(w, http.StatusNotFound, nil, map[string]any{"code": 81044, "message": "Record does not exist."})
			return
		}
		f.deleted = append(f.deleted, id)
		delete(f.records, id)
		f.reply(w, http.StatusOK, map[string]any{"id": id})
	case http.MethodPatch:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("PATCH body: %v", err)
		}
		f.patched = append(f.patched, body)
		rec, ok := f.records[strings.Join(rest, "")]
		if !ok {
			f.reply(w, http.StatusNotFound, nil, map[string]any{"code": 81044, "message": "Record does not exist."})
			return
		}
		if c, ok := body["content"].(string); ok {
			rec.Content = c
		}
		f.reply(w, http.StatusOK, rec)
	}
}

func (f *fakeCloudflare) record(typ, name string) []fakeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRecord
	for _, rec := range f.records {
		if rec.Type == typ && rec.Name == name {
			out = append(out, *rec)
		}
	}
	return out
}

func cfClient(t *testing.T, srv *httptest.Server, settings map[string]string) *cloudflare {
	t.Helper()
	c, err := Build(model.DNSProvider{ID: "cf", Kind: "cloudflare", Settings: settings}, Options{CloudflareAPI: srv.URL, UserAgent: "ostiole/test"})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*cloudflare)
}

var addr7 = netip.MustParseAddr("203.0.113.7")

func TestCloudflareCreatesAMissingRecord(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	// Another name's record is no record of this one.
	f.add("A", "other.example.com", "198.51.100.8", 1, false, "")
	was, err := cfClient(t, srv, map[string]string{"token": "dns-token"}).Set(context.Background(), "example.com", "home.example.com", TypeA, addr7)
	if err != nil {
		t.Fatal(err)
	}
	if len(was) != 0 {
		t.Errorf("was = %v, want nothing", was)
	}
	want := map[string]any{"type": "A", "name": "home.example.com", "content": "203.0.113.7", "ttl": float64(1), "proxied": false, "comment": "Dynamic DNS from Ostiole"}
	if len(f.posted) != 1 || fmt.Sprint(f.posted[0]) != fmt.Sprint(want) {
		t.Errorf("posted %v, want %v", f.posted, want)
	}
}

// The TTL, the proxy status and the comment are the operator's: only the
// address is sent.
func TestCloudflarePatchesTheAddressAlone(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	f.add("A", "home.example.com", "198.51.100.5", 120, true, "mine")
	was, err := cfClient(t, srv, map[string]string{"token": "dns-token"}).Set(context.Background(), "example.com", "Home.Example.com", TypeA, addr7)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(was, []netip.Addr{netip.MustParseAddr("198.51.100.5")}) {
		t.Errorf("was = %v", was)
	}
	if len(f.patched) != 1 || len(f.patched[0]) != 1 || f.patched[0]["content"] != "203.0.113.7" {
		t.Errorf("patched %v, want the content alone", f.patched)
	}
	got := f.record("A", "home.example.com")
	if len(got) != 1 || got[0].TTL != 120 || !got[0].Proxied || got[0].Comment != "mine" {
		t.Errorf("record = %+v", got)
	}
}

func TestCloudflareLeavesACurrentRecordAlone(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	f.add("AAAA", "home.example.com", "2001:db8::7", 1, false, "")
	was, err := cfClient(t, srv, map[string]string{"token": "dns-token"}).Set(context.Background(), "example.com", "home.example.com", TypeAAAA, netip.MustParseAddr("2001:db8::7"))
	if err != nil {
		t.Fatal(err)
	}
	if len(was) != 1 || len(f.posted)+len(f.patched) != 0 {
		t.Errorf("was %v, wrote %v %v", was, f.posted, f.patched)
	}
}

func TestCloudflareWillNotPickBetweenTwoRecords(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	f.add("A", "home.example.com", "198.51.100.5", 1, false, "")
	f.add("A", "home.example.com", "198.51.100.6", 1, false, "")
	_, err := cfClient(t, srv, map[string]string{"token": "dns-token"}).Set(context.Background(), "example.com", "home.example.com", TypeA, addr7)
	if err == nil || !strings.Contains(err.Error(), "holds 2 A records for home.example.com") {
		t.Errorf("err = %v", err)
	}
	if len(f.posted)+len(f.patched) != 0 {
		t.Errorf("wrote %v %v", f.posted, f.patched)
	}
}

// The zone is looked up once, with the zone token when there is one.
func TestCloudflareLooksTheZoneUpOnce(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	f.zoneToken = "zone-token"
	c := cfClient(t, srv, map[string]string{"token": "dns-token", "zoneToken": "zone-token"})
	for range 2 {
		if _, err := c.Lookup(context.Background(), "example.com", "home.example.com", TypeA); err != nil {
			t.Fatal(err)
		}
	}
	zones := 0
	for _, call := range f.calls {
		if call == "GET /zones" {
			zones++
		}
	}
	if zones != 1 {
		t.Errorf("calls %v, want one zone lookup", f.calls)
	}
}

func TestCloudflareSaysWhatWentWrong(t *testing.T) {
	t.Parallel()
	_, srv := newFakeCloudflare(t)
	for name, tc := range map[string]struct {
		settings map[string]string
		zone     string
		want     string
	}{
		"bad token":    {map[string]string{"token": "wrong"}, "example.com", "Cloudflare refused the token for example.com (9109 Unauthorized to access requested resource)"},
		"no such zone": {map[string]string{"token": "dns-token"}, "example.net", "Cloudflare has no zone example.net that this token can see"},
	} {
		_, err := cfClient(t, srv, tc.settings).Lookup(context.Background(), tc.zone, "home."+tc.zone, TypeA)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// A token that reads zones but may not touch records is refused at the
// records call.
func TestCloudflareSaysWhichZoneRefusedTheToken(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	f.zoneToken = "zone-token"
	_, err := cfClient(t, srv, map[string]string{"token": "stale", "zoneToken": "zone-token"}).Lookup(context.Background(), "example.com", "home.example.com", TypeA)
	if err == nil || !strings.Contains(err.Error(), "refused the token for example.com (10000 Authentication error)") {
		t.Errorf("err = %v", err)
	}
}

func TestCloudflareWaitsWhenAsked(t *testing.T) {
	t.Parallel()
	for header, want := range map[string]time.Duration{"7": 7 * time.Second, "": 5 * time.Minute} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if header != "" {
				w.Header().Set("Retry-After", header)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":971,"message":"Please wait and consider throttling your request speed"}],"messages":[],"result":null}`))
		}))
		_, err := cfClient(t, srv, map[string]string{"token": "dns-token"}).Lookup(context.Background(), "example.com", "home.example.com", TypeA)
		srv.Close()
		var pe *Error
		if !errors.As(err, &pe) || pe.RetryAfter != want {
			t.Errorf("Retry-After %q: err = %v, want a wait of %s", header, err, want)
		}
	}
}

// A redirect is reported, never followed with the token on it.
func TestCloudflareFollowsNoRedirect(t *testing.T) {
	t.Parallel()
	followed := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusMovedPermanently)
	}))
	defer srv.Close()
	_, err := cfClient(t, srv, map[string]string{"token": "dns-token"}).Lookup(context.Background(), "example.com", "home.example.com", TypeA)
	if err == nil || !strings.Contains(err.Error(), "301") || followed {
		t.Errorf("err = %v, followed = %v", err, followed)
	}
}

// A test lists what the token can see and reads each domain with the token
// that writes records; a domain it cannot see says so.
func TestCloudflareTestsTheToken(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	f.extraZones = 3
	got, err := cfClient(t, srv, map[string]string{"token": "dns-token"}).Test(context.Background(), []string{"example.com", "example.net"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Zones, []string{"example.com"}) || got.More != 3 {
		t.Errorf("zones %v and %d more", got.Zones, got.More)
	}
	if len(got.Domains) != 2 || got.Domains[0].Error != "" ||
		got.Domains[1].Error != "Cloudflare has no zone example.net that this token can see" {
		t.Errorf("domains %+v", got.Domains)
	}

	_, err = cfClient(t, srv, map[string]string{"token": "wrong"}).Test(context.Background(), nil)
	if err == nil || err.Error() != "Cloudflare refused the token (9109 Unauthorized to access requested resource)" {
		t.Errorf("a bad token: %v", err)
	}
}

// With a zone token the zones are listed with it, and the records are
// read with the other: a stale record token shows on the domain.
func TestCloudflareTestsBothTokens(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	f.zoneToken = "zone-token"
	got, err := cfClient(t, srv, map[string]string{"token": "stale", "zoneToken": "zone-token"}).Test(context.Background(), []string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Domains) != 1 || !strings.Contains(got.Domains[0].Error, "refused the token for example.com (10000 Authentication error)") {
		t.Errorf("domains %+v", got.Domains)
	}
}

var challenge = Record{Zone: "example.com", Name: "_acme-challenge.example.com", Value: "LoqXcYV8q5ONbJQxbmR7SCTNo3tiAXDfowyjxAjEuX0"}

// A challenge record goes in quoted, with a two-minute TTL and a comment saying
// what wrote it; an identical one already there is the record wanted.
func TestCloudflareAddsATXTRecord(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	c := cfClient(t, srv, map[string]string{"token": "dns-token"})
	for range 2 {
		if err := c.AddTXT(context.Background(), challenge); err != nil {
			t.Fatal(err)
		}
	}
	got := f.record("TXT", challenge.Name)
	if len(got) != 1 || got[0].Content != `"`+challenge.Value+`"` || got[0].TTL != 120 || got[0].Comment != "ACME challenge from Ostiole" {
		t.Errorf("records = %+v", got)
	}
}

// A wildcard and its apex put two values at one name; each is removed on
// its own, and a record Ostiole did not write stays.
func TestCloudflareRemovesOnlyItsValue(t *testing.T) {
	t.Parallel()
	f, srv := newFakeCloudflare(t)
	f.add("TXT", challenge.Name, `"someone else's"`, 300, false, "")
	c := cfClient(t, srv, map[string]string{"token": "dns-token"})
	other := challenge
	other.Value = "other-value"
	for _, r := range []Record{challenge, other} {
		if err := c.AddTXT(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.RemoveTXT(context.Background(), challenge); err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, rec := range f.record("TXT", challenge.Name) {
		left = append(left, rec.Content)
	}
	slices.Sort(left)
	if want := []string{`"other-value"`, `"someone else's"`}; !slices.Equal(left, want) {
		t.Errorf("left %q, want %q", left, want)
	}
	// A value already gone is nothing to do.
	if err := c.RemoveTXT(context.Background(), challenge); err != nil {
		t.Errorf("removing again: %v", err)
	}
}

// A refused token says which zone it was refused for, and carries no
// secret.
func TestCloudflareTXTRefusalNamesTheZone(t *testing.T) {
	t.Parallel()
	_, srv := newFakeCloudflare(t)
	err := cfClient(t, srv, map[string]string{"token": "wrong-secret-token"}).AddTXT(context.Background(), challenge)
	if err == nil || !strings.Contains(err.Error(), "refused the token for example.com") || strings.Contains(err.Error(), "wrong-secret-token") {
		t.Errorf("err = %v", err)
	}
}
