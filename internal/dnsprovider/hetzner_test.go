package dnsprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// fakeHetzner keeps rrsets as Hetzner's Cloud API does: values are added
// to a name's set and taken from it by actions, each running until it has
// been asked after once.
type fakeHetzner struct {
	t     *testing.T
	token string

	mu      sync.Mutex
	sets    map[string]*fakeHetznerSet // "zone name" → set
	actions map[int64]int              // id → times asked
	next    int64
	fail    bool
	calls   []string
}

type fakeHetznerSet struct {
	TTL     int
	Records []string
}

func newFakeHetzner(t *testing.T) (*fakeHetzner, *httptest.Server) {
	t.Helper()
	f := &fakeHetzner{t: t, token: "h-token", sets: map[string]*fakeHetznerSet{}, actions: map[int64]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeHetzner) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeHetzner) problem(w http.ResponseWriter, status int, code, message string) {
	f.reply(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": map[string]any{}}})
}

func (f *fakeHetzner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.problem(w, http.StatusUnauthorized, "unauthorized", "unable to authenticate")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1"), "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "actions" && r.Method == http.MethodGet:
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		asked, ok := f.actions[id]
		if !ok {
			f.problem(w, http.StatusNotFound, "not_found", "action not found")
			return
		}
		f.actions[id]++
		action := map[string]any{"id": id, "status": "running"}
		switch {
		case asked == 0:
		case f.fail:
			action["status"], action["error"] = "error", map[string]string{"code": "action_failed", "message": "Action failed"}
		default:
			action["status"] = "success"
		}
		f.reply(w, http.StatusOK, map[string]any{"action": action})
	case len(parts) == 7 && parts[0] == "zones" && parts[2] == "rrsets" && parts[4] == "TXT" && parts[5] == "actions":
		var body struct {
			TTL     int `json:"ttl"`
			Records []struct {
				Value string `json:"value"`
			} `json:"records"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("body: %v", err)
		}
		key := parts[1] + " " + parts[3]
		set := f.sets[key]
		switch parts[6] {
		case "add_records":
			if set == nil {
				set = &fakeHetznerSet{TTL: body.TTL}
				f.sets[key] = set
			}
			for _, rec := range body.Records {
				if !slices.Contains(set.Records, rec.Value) {
					set.Records = append(set.Records, rec.Value)
				}
			}
		case "remove_records":
			if set == nil {
				f.problem(w, http.StatusNotFound, "not_found", "RRSet not found")
				return
			}
			for _, rec := range body.Records {
				set.Records = slices.DeleteFunc(set.Records, func(v string) bool { return v == rec.Value })
			}
			if len(set.Records) == 0 {
				delete(f.sets, key)
			}
		}
		f.next++
		f.actions[f.next] = 0
		f.reply(w, http.StatusCreated, map[string]any{"action": map[string]any{"id": f.next, "command": parts[6], "status": "running"}})
	default:
		f.problem(w, http.StatusNotFound, "not_found", "not found")
	}
}

func (f *fakeHetzner) held(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	set, ok := f.sets["example.com "+name]
	if !ok {
		return nil
	}
	var out []string
	for _, v := range set.Records {
		out = append(out, v+" "+strconv.Itoa(set.TTL))
	}
	slices.Sort(out)
	return out
}

func hetznerClient(t *testing.T, srv *httptest.Server, token string) *hetzner {
	t.Helper()
	c, err := Build(model.DNSProvider{ID: "h", Kind: "hetzner", Settings: map[string]string{"token": token}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	h := c.(*hetzner)
	h.base, h.poll = srv.URL+"/v1", 10*time.Millisecond
	return h
}

// Each action is waited for: a record is there once AddTXT returns, and
// gone once RemoveTXT does.
func TestHetznerWaitsForEachAction(t *testing.T) {
	t.Parallel()
	f, srv := newFakeHetzner(t)
	c := hetznerClient(t, srv, "h-token")
	ctx := context.Background()
	name := "_acme-challenge.www.example.com"
	for _, v := range []string{"one", "two"} {
		if err := c.AddTXT(ctx, txtAt(name, v)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := f.held("_acme-challenge.www"), []string{`"one" 60`, `"two" 60`}; !slices.Equal(got, want) {
		t.Errorf("held %q, want %q", got, want)
	}
	for id, asked := range f.actions {
		if asked < 2 {
			t.Errorf("action %d was asked after %d times, want until it finished", id, asked)
		}
	}
	for _, v := range []string{"one", "two", "two"} {
		if err := c.RemoveTXT(ctx, txtAt(name, v)); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.held("_acme-challenge.www"); got != nil {
		t.Errorf("held %q", got)
	}
}

func TestHetznerSaysAnActionFailed(t *testing.T) {
	t.Parallel()
	f, srv := newFakeHetzner(t)
	f.fail = true
	err := hetznerClient(t, srv, "h-token").AddTXT(context.Background(), txtAt("example.com", "v"))
	if err == nil || err.Error() != "Hetzner: add_records for example.com: Action failed" {
		t.Errorf("err = %v", err)
	}
	if !slices.Contains(f.calls, "POST /v1/zones/example.com/rrsets/@/TXT/actions/add_records") {
		t.Errorf("calls %q, want the apex written @", f.calls)
	}
}

func TestHetznerRefusalCarriesNoSecret(t *testing.T) {
	t.Parallel()
	_, srv := newFakeHetzner(t)
	err := hetznerClient(t, srv, "wrong-secret").AddTXT(context.Background(), txtAt("_acme-challenge.example.com", "v"))
	if err == nil || err.Error() != "Hetzner refused the credentials (unauthorized: unable to authenticate)" {
		t.Errorf("err = %v", err)
	}
}
