package dnsprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ostiole/internal/model"
)

// fakePorkbun keeps a record each, as Porkbun's API does: every call a
// POST with both keys in the body, ids as numbers from a create and as
// strings from a lookup.
type fakePorkbun struct {
	t *testing.T

	mu      sync.Mutex
	records map[int]*fakePBRecord
	next    int
	calls   []string
}

type fakePBRecord struct {
	Name, Type, Content, TTL string
}

func newFakePorkbun(t *testing.T) (*fakePorkbun, *httptest.Server) {
	t.Helper()
	f := &fakePorkbun{t: t, records: map[int]*fakePBRecord{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakePorkbun) reply(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakePorkbun) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("body: %v", err)
	}
	if r.Method != http.MethodPost || body["apikey"] != "pk1_key" || body["secretapikey"] != "sk1_secret" {
		f.reply(w, http.StatusForbidden, map[string]any{"status": "ERROR", "message": "Invalid API key. (002)"})
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/json/v3"), "/"), "/")
	switch {
	case len(parts) == 3 && parts[0] == "dns" && parts[1] == "create":
		f.next++
		name := body["name"] + "." + parts[2]
		if body["name"] == "" {
			name = parts[2]
		}
		f.records[f.next] = &fakePBRecord{Name: name, Type: body["type"], Content: body["content"], TTL: body["ttl"]}
		f.reply(w, http.StatusOK, map[string]any{"status": "SUCCESS", "id": f.next})
	case len(parts) >= 4 && parts[0] == "dns" && parts[1] == "retrieveByNameType":
		name := parts[2]
		if len(parts) == 5 {
			name = parts[4] + "." + parts[2]
		}
		out := []map[string]string{}
		for id, rec := range f.records {
			if rec.Name == name && rec.Type == parts[3] {
				out = append(out, map[string]string{"id": strconv.Itoa(id), "name": rec.Name, "type": rec.Type, "content": rec.Content, "ttl": rec.TTL, "prio": "0", "notes": ""})
			}
		}
		f.reply(w, http.StatusOK, map[string]any{"status": "SUCCESS", "records": out})
	case len(parts) == 4 && parts[0] == "dns" && parts[1] == "delete":
		id, _ := strconv.Atoi(parts[3])
		if _, ok := f.records[id]; !ok {
			f.reply(w, http.StatusBadRequest, map[string]any{"status": "ERROR", "message": "Invalid record ID."})
			return
		}
		delete(f.records, id)
		f.reply(w, http.StatusOK, map[string]any{"status": "SUCCESS"})
	default:
		f.reply(w, http.StatusNotFound, map[string]any{"status": "ERROR", "message": "Not found."})
	}
}

func (f *fakePorkbun) held(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, rec := range f.records {
		if rec.Name == name {
			out = append(out, fmt.Sprintf("%s %s %s", rec.Type, rec.Content, rec.TTL))
		}
	}
	slices.Sort(out)
	return out
}

func porkbunClient(t *testing.T, srv *httptest.Server, secret string) *porkbun {
	t.Helper()
	c, err := Build(model.DNSProvider{ID: "pb", Kind: "porkbun", Settings: map[string]string{"apiKey": "pk1_key", "secretKey": secret}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := c.(*porkbun)
	p.base = srv.URL + "/api/json/v3"
	return p
}

func TestPorkbunAddsAndRemovesByID(t *testing.T) {
	t.Parallel()
	f, srv := newFakePorkbun(t)
	c := porkbunClient(t, srv, "sk1_secret")
	ctx := context.Background()
	name := "_acme-challenge.www.example.com"
	for _, v := range []string{"one", "two"} {
		if err := c.AddTXT(ctx, txtAt(name, v)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := f.held(name), []string{"TXT one 300", "TXT two 300"}; !slices.Equal(got, want) {
		t.Errorf("held %q, want %q", got, want)
	}
	if err := c.RemoveTXT(ctx, txtAt(name, "one")); err != nil {
		t.Fatal(err)
	}
	if got := f.held(name); !slices.Equal(got, []string{"TXT two 300"}) {
		t.Errorf("held %q", got)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "retrieveByNameType") {
			t.Errorf("calls %q: the id kept from the create was not used", f.calls)
		}
	}
}

// A record made before a restart is found by its name and value.
func TestPorkbunFindsARecordItDidNotKeep(t *testing.T) {
	t.Parallel()
	f, srv := newFakePorkbun(t)
	ctx := context.Background()
	for _, r := range []Record{txtAt("_acme-challenge.example.com", "one"), txtAt("_acme-challenge.example.com", "two"), txtAt("example.com", "one")} {
		if err := porkbunClient(t, srv, "sk1_secret").AddTXT(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []Record{txtAt("_acme-challenge.example.com", "one"), txtAt("example.com", "one")} {
		if err := porkbunClient(t, srv, "sk1_secret").RemoveTXT(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.held("_acme-challenge.example.com"); !slices.Equal(got, []string{"TXT two 300"}) {
		t.Errorf("held %q", got)
	}
	if got := f.held("example.com"); got != nil {
		t.Errorf("held %q at the apex", got)
	}
}

// The keys travel in the body, and neither is in an error.
func TestPorkbunRefusalCarriesNoSecret(t *testing.T) {
	t.Parallel()
	_, srv := newFakePorkbun(t)
	err := porkbunClient(t, srv, "wrong-secret").AddTXT(context.Background(), txtAt("_acme-challenge.example.com", "v"))
	if err == nil || err.Error() != "Porkbun refused the credentials (Invalid API key. (002))" {
		t.Errorf("err = %v", err)
	}
}
