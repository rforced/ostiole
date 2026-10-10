package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/engine"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
)

// auditRig is a server whose accounts are signed in already: alice and bob
// admins, carol an operator, dave a viewer. Requests go straight to the
// handler, so each can come from its own address.
type auditRig struct {
	h        http.Handler
	log      *audit.Log
	store    *store.Store
	sessions map[string]string
}

func auditServer(t *testing.T) *auditRig {
	t.Helper()
	dir := t.TempDir()
	log := audit.Open(dir)
	st := store.New(dir)
	eng := engine.New(st, &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler)).WithAudit(log)
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := as.Setup("alice", testPassword); err != nil {
		t.Fatal(err)
	}
	for name, role := range map[string]auth.Role{"bob": auth.RoleAdmin, "carol": auth.RoleOperator, "dave": auth.RoleViewer} {
		if err := as.CreateUser(name, testPassword, role); err != nil {
			t.Fatal(err)
		}
	}
	rig := &auditRig{h: Handler(Deps{Engine: eng, Auth: as, Audit: log}), log: log, store: st, sessions: map[string]string{}}
	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		sess, err := as.Login(name, testPassword, "192.0.2.1")
		if err != nil {
			t.Fatal(err)
		}
		rig.sessions[name] = sess.ID
	}
	return rig
}

// send sends a request as the account from address.
func (rig *auditRig) send(t *testing.T, as, address, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = address + ":40000"
	req.Header.Set(RequestHeader, RequestHeaderValue)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: rig.sessions[as]})
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, req)
	return rec
}

func (rig *auditRig) do(t *testing.T, as, address, method, path string, body any) (int, []byte) {
	t.Helper()
	rec := rig.send(t, as, address, method, path, body)
	return rec.Code, rec.Body.Bytes()
}

func (rig *auditRig) events(t *testing.T) []audit.Event {
	t.Helper()
	events, err := rig.log.Events()
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// apply applies starter() under hostname as the account, with a confirm
// window in seconds.
func (rig *auditRig) apply(t *testing.T, as, address, hostname string, window int) engine.ApplyResult {
	t.Helper()
	rec := rig.send(t, as, address, http.MethodGet, "/api/v1/config", nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Fatalf("read config: %d %s", rec.Code, rec.Body)
	}
	base, err := strconv.Unquote(rec.Header().Get("ETag"))
	if err != nil {
		t.Fatalf("ETag %q: %v", rec.Header().Get("ETag"), err)
	}
	cfg := starter()
	cfg.System.Hostname = hostname
	code, raw := rig.do(t, as, address, http.MethodPost, "/api/v1/apply",
		applyRequest{BaseRevision: base, Config: (*draftConfig)(cfg), ConfirmTimeoutSeconds: window})
	if code != http.StatusOK {
		t.Fatalf("apply as %s: %d %s", as, code, raw)
	}
	var res engine.ApplyResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAuditRoutesAreAdminOnly(t *testing.T) {
	t.Parallel()
	rig := auditServer(t)
	routes := [][2]string{
		{http.MethodGet, "/api/v1/audit"},
		{http.MethodGet, "/api/v1/audit/stream"},
		{http.MethodDelete, "/api/v1/audit"},
		{http.MethodGet, "/api/v1/config/applied"},
	}
	for _, as := range []string{"carol", "dave"} {
		for _, rt := range routes {
			if code, raw := rig.do(t, as, "192.0.2.30", rt[0], rt[1], nil); code != http.StatusForbidden {
				t.Errorf("%s %s as %s: %d %s, want 403", rt[0], rt[1], as, code, raw)
			}
		}
	}
	if code, raw := rig.do(t, "alice", "192.0.2.10", http.MethodGet, "/api/v1/audit", nil); code != http.StatusOK {
		t.Errorf("read as an admin: %d %s", code, raw)
	}
	if code, raw := rig.do(t, "alice", "192.0.2.10", http.MethodGet, "/api/v1/config/applied", nil); code != http.StatusOK || string(bytes.TrimSpace(raw)) != "null" {
		t.Errorf("applied before any apply: %d %s, want null", code, raw)
	}
	if events := rig.events(t); len(events) != 0 {
		t.Errorf("refused calls were recorded: %+v", events)
	}
}

func TestAuditPageAndSearch(t *testing.T) {
	t.Parallel()
	rig := auditServer(t)
	alice := audit.Actor{Name: "alice", Kind: audit.Account, Role: "admin", Address: "192.0.2.10"}
	bob := audit.Actor{Name: "bob", Kind: audit.Account, Role: "admin", Address: "192.0.2.20"}
	rig.log.Add(audit.Event{Action: audit.AccountCreate, By: alice, Target: "erin", Detail: "viewer"})
	rig.log.Add(audit.Event{Action: audit.Reboot, By: bob})
	rig.log.Add(audit.Event{Action: audit.CronRun, By: alice, Target: "nightly"})

	type page struct {
		Entries []audit.Event `json:"entries"`
		Next    uint64        `json:"next"`
		More    bool          `json:"more"`
		Held    int           `json:"held"`
	}
	read := func(query string) page {
		t.Helper()
		code, raw := rig.do(t, "alice", "192.0.2.10", http.MethodGet, "/api/v1/audit"+query, nil)
		if code != http.StatusOK {
			t.Fatalf("read %s: %d %s", query, code, raw)
		}
		var p page
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	p := read("")
	if len(p.Entries) != 3 || p.Held != 3 || p.More {
		t.Fatalf("page = %+v", p)
	}
	if p.Entries[0].Seq != 3 || p.Entries[2].Seq != 1 {
		t.Errorf("not newest first: %+v", p.Entries)
	}
	for _, e := range p.Entries {
		if e.Text == "" {
			t.Errorf("entry %d has no sentence", e.Seq)
		}
	}

	if p = read("?q=192.0.2.20"); len(p.Entries) != 1 || p.Entries[0].Action != audit.Reboot || p.Entries[0].By != bob {
		t.Errorf("search by address = %+v", p.Entries)
	}
	if p = read("?q=erin"); len(p.Entries) != 1 || p.Entries[0].Action != audit.AccountCreate {
		t.Errorf("search by target = %+v", p.Entries)
	}

	p = read("?limit=2")
	if len(p.Entries) != 2 || !p.More || p.Next != 2 {
		t.Fatalf("first of two pages = %+v", p)
	}
	if p = read("?limit=2&before=" + strconv.FormatUint(p.Next, 10)); len(p.Entries) != 1 || p.Entries[0].Seq != 1 || p.More {
		t.Errorf("second page = %+v", p)
	}
}

func TestAuditClearLeavesWhoCleared(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, action, target string
	}{
		{"the audit log", "/api/v1/audit", audit.LogClear, "audit log"},
		{"every log", "/api/v1/system/logs", audit.LogsClear, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := auditServer(t)
			for range 3 {
				rig.log.Add(audit.Event{Action: audit.Reboot, By: audit.Actor{Name: "alice", Kind: audit.Account}})
			}
			if code, raw := rig.do(t, "bob", "192.0.2.20", http.MethodDelete, tc.path, nil); code != http.StatusOK {
				t.Fatalf("clear: %d %s", code, raw)
			}
			events := rig.events(t)
			if len(events) != 1 {
				t.Fatalf("after a Clear the log holds %+v, want one entry", events)
			}
			e := events[0]
			want := audit.Actor{Name: "bob", Kind: audit.Account, Role: "admin", Address: "192.0.2.20"}
			if e.Action != tc.action || e.Target != tc.target || e.By != want {
				t.Errorf("entry = %+v, want %s of %q by %+v", e, tc.action, tc.target, want)
			}
			if e.Seq != 4 {
				t.Errorf("the Clear is numbered %d, want 4: the numbering carries on", e.Seq)
			}
		})
	}
}

func TestAuditRevisionsShowWhoAppliedToAdminsOnly(t *testing.T) {
	t.Parallel()
	rig := auditServer(t)
	rig.apply(t, "alice", "192.0.2.10", "first", 0)
	if res := rig.apply(t, "alice", "192.0.2.10", "second", 0); res.Archived == nil || res.Archived.Applied == nil ||
		res.Archived.Applied.By.Name != "alice" {
		t.Errorf("an admin's apply archived %+v, want who applied it", res.Archived)
	}
	if res := rig.apply(t, "carol", "192.0.2.30", "third", 0); res.Archived == nil || res.Archived.Applied != nil {
		t.Errorf("an operator's apply archived %+v, want no record", res.Archived)
	}

	revisions := func(as string) []store.Revision {
		t.Helper()
		code, raw := rig.do(t, as, "192.0.2.40", http.MethodGet, "/api/v1/config/revisions", nil)
		if code != http.StatusOK {
			t.Fatalf("revisions as %s: %d %s", as, code, raw)
		}
		var revs []store.Revision
		if err := json.Unmarshal(raw, &revs); err != nil {
			t.Fatal(err)
		}
		if len(revs) != 2 {
			t.Fatalf("revisions as %s = %s, want two", as, raw)
		}
		return revs
	}
	for _, r := range revisions("alice") {
		if r.Applied == nil || r.Applied.By.Address == "" {
			t.Errorf("an admin's revision %s has no record: %+v", r.ID, r.Applied)
		}
	}
	for _, as := range []string{"carol", "dave"} {
		for _, r := range revisions(as) {
			if r.Applied != nil {
				t.Errorf("%s sees who applied %s: %+v", as, r.ID, r.Applied)
			}
		}
	}

	code, raw := rig.do(t, "alice", "192.0.2.10", http.MethodGet, "/api/v1/config/applied", nil)
	var rec store.Applied
	if err := json.Unmarshal(raw, &rec); code != http.StatusOK || err != nil {
		t.Fatalf("applied: %d %s (%v)", code, raw, err)
	}
	if want := (audit.Actor{Name: "carol", Kind: audit.Account, Role: "operator", Address: "192.0.2.30"}); rec.By != want {
		t.Errorf("in force applied by %+v, want %+v", rec.By, want)
	}
}

func TestAuditApplyConfirmedByAnother(t *testing.T) {
	t.Parallel()
	rig := auditServer(t)
	if res := rig.apply(t, "alice", "192.0.2.10", "first", 60); !res.Pending {
		t.Fatalf("apply with a window is not pending: %+v", res)
	}
	code, raw := rig.do(t, "bob", "192.0.2.20", http.MethodPost, "/api/v1/apply/confirm", nil)
	if code != http.StatusOK {
		t.Fatalf("confirm: %d %s", code, raw)
	}

	alice := audit.Actor{Name: "alice", Kind: audit.Account, Role: "admin", Address: "192.0.2.10"}
	bob := audit.Actor{Name: "bob", Kind: audit.Account, Role: "admin", Address: "192.0.2.20"}
	rec, err := rig.store.Applied()
	if err != nil || rec == nil {
		t.Fatalf("applied = %+v (%v)", rec, err)
	}
	if rec.By != alice || rec.ConfirmedBy == nil || *rec.ConfirmedBy != bob {
		t.Errorf("saved as applied by %+v confirmed by %+v, want %+v and %+v", rec.By, rec.ConfirmedBy, alice, bob)
	}

	events := rig.events(t)
	if len(events) != 2 || events[0].Action != audit.Apply || events[0].By != alice ||
		events[1].Action != audit.Confirm || events[1].By != bob || events[1].Target != "alice" {
		t.Errorf("audit log = %+v", events)
	}
	if events[0].Detail != "60" {
		t.Errorf("the apply's window = %q, want 60 seconds", events[0].Detail)
	}
}
