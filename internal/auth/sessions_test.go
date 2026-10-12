package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const agentFox = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Quokka/140.0"

func newTestStore(t *testing.T, dir string) (*sessionStore, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	return newSessionStore(dir, func() time.Time { return now }), &now
}

func TestSessionCreateFromKeepsTheClient(t *testing.T) {
	t.Parallel()
	st, _ := newTestStore(t, "")
	sess := st.createFrom("ada", Client{Address: "192.0.2.10", Agent: agentFox})
	if sess.Address != "192.0.2.10" || sess.Agent != agentFox {
		t.Fatalf("session = %+v", sess)
	}
	got, ok := st.get(sess.ID)
	if !ok || got.Address != "192.0.2.10" || got.Agent != agentFox {
		t.Fatalf("get = %+v, %v", got, ok)
	}
	if plain := st.create("ada"); plain.Address != "" || plain.Agent != "" {
		t.Errorf("create = %+v", plain)
	}
}

func TestSessionAgentIsCleaned(t *testing.T) {
	t.Parallel()
	st, _ := newTestStore(t, "")
	sess := st.createFrom("ada", Client{Agent: " Quokka\x00/1.0\r\n\tGecko\x7f "})
	if sess.Agent != "Quokka/1.0Gecko" {
		t.Errorf("agent = %q", sess.Agent)
	}
	long := strings.Repeat("a", 199) + "é" + strings.Repeat("b", 50)
	sess = st.createFrom("ada", Client{Agent: long})
	if len(sess.Agent) > maxAgent || sess.Agent != strings.Repeat("a", 199) {
		t.Errorf("agent = %q (%d bytes)", sess.Agent, len(sess.Agent))
	}
}

func TestSessionListOrderAndRef(t *testing.T) {
	t.Parallel()
	st, now := newTestStore(t, "")
	first := st.createFrom("ada", Client{Address: "192.0.2.1"})
	*now = now.Add(time.Minute)
	second := st.createFrom("ada", Client{Address: "192.0.2.2"})
	st.createFrom("bob", Client{Address: "198.51.100.1"})
	*now = now.Add(time.Minute)
	if _, ok := st.get(first.ID); !ok {
		t.Fatal("first session gone")
	}

	got := st.list("ada")
	if len(got) != 2 {
		t.Fatalf("list = %+v", got)
	}
	if got[0].Address != "192.0.2.1" || got[1].Address != "192.0.2.2" {
		t.Errorf("order = %s, %s", got[0].Address, got[1].Address)
	}
	if got[0].Ref != sessionKey(first.ID)[:16] || got[1].Ref != sessionKey(second.ID)[:16] {
		t.Errorf("refs = %s, %s", got[0].Ref, got[1].Ref)
	}
	*now = now.Add(SessionIdleTimeout + time.Second)
	if got := st.list("ada"); len(got) != 0 {
		t.Errorf("idle sessions listed: %+v", got)
	}
}

func TestSessionDeleteRefOnlyItsOwner(t *testing.T) {
	t.Parallel()
	st, _ := newTestStore(t, "")
	ada := st.createFrom("ada", Client{})
	bob := st.createFrom("bob", Client{})
	ref := sessionRef(sessionKey(ada.ID))

	if st.deleteRef("bob", ref) {
		t.Error("bob ended ada's session")
	}
	if st.deleteRef("ada", "") || st.deleteRef("ada", ref[:8]) {
		t.Error("a short ref ended a session")
	}
	if !st.deleteRef("ada", ref) {
		t.Fatal("ada could not end her session")
	}
	if _, ok := st.get(ada.ID); ok {
		t.Error("the session survived deleteRef")
	}
	if st.deleteRef("ada", ref) {
		t.Error("deleteRef twice")
	}
	if _, ok := st.get(bob.ID); !ok {
		t.Error("bob's session went too")
	}
}

func TestSessionDeleteOthersKeepsOne(t *testing.T) {
	t.Parallel()
	st, _ := newTestStore(t, "")
	keep := st.createFrom("ada", Client{})
	other := st.createFrom("ada", Client{})
	bob := st.createFrom("bob", Client{})
	st.deleteOthers("ada", keep.ID)
	if _, ok := st.get(keep.ID); !ok {
		t.Error("the kept session went")
	}
	if _, ok := st.get(other.ID); ok {
		t.Error("the other session stayed")
	}
	if _, ok := st.get(bob.ID); !ok {
		t.Error("bob's session went")
	}
}

func TestSessionClientSurvivesARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, _ := newTestStore(t, dir)
	sess := st.createFrom("ada", Client{Address: "2001:db8::7", Agent: agentFox})

	again, _ := newTestStore(t, dir)
	if err := again.load(); err != nil {
		t.Fatal(err)
	}
	got, ok := again.get(sess.ID)
	if !ok || got.Address != "2001:db8::7" || got.Agent != agentFox {
		t.Fatalf("get = %+v, %v", got, ok)
	}
}

func TestSessionOldFileLoads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := "made-up-session-id"
	old := `{"sessions":[{"hash":"` + sessionKey(id) + `","username":"ada",` +
		`"created":"2026-09-15T11:00:00Z","lastSeen":"2026-09-15T11:30:00Z","expires":"2026-09-22T11:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(dir, SessionsFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := newTestStore(t, dir)
	if err := st.load(); err != nil {
		t.Fatal(err)
	}
	got, ok := st.get(id)
	if !ok || got.Username != "ada" || got.Address != "" || got.Agent != "" {
		t.Fatalf("get = %+v, %v", got, ok)
	}
	if list := st.list("ada"); len(list) != 1 || list[0].Ref != sessionKey(id)[:16] {
		t.Errorf("list = %+v", list)
	}
}

func TestServiceSessions(t *testing.T) {
	t.Parallel()
	s, now := newService(t)
	if err := s.Setup("ada", goodPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SignInFrom("nobody", Client{}); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("SignInFrom an unknown account: %v", err)
	}
	here, err := s.SignInFrom("ada", Client{Address: "203.0.113.5", Agent: agentFox})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	there, err := s.SignInFrom("ada", Client{Address: "203.0.113.6"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.SignInFrom("ada", Client{Address: "203.0.113.7"})
	if err != nil {
		t.Fatal(err)
	}

	list := s.Sessions("ada")
	if len(list) != 3 {
		t.Fatalf("Sessions = %+v", list)
	}
	if s.RefOf(here.ID) != sessionKey(here.ID)[:16] || s.RefOf("") != "" {
		t.Errorf("RefOf = %q", s.RefOf(here.ID))
	}
	if s.Sessions("nobody") != nil {
		t.Error("an unknown account has sessions")
	}

	if err := s.EndSession("ada", "0000000000000000"); !errors.Is(err, ErrNoSuchSession) {
		t.Errorf("EndSession unknown ref: %v", err)
	}
	if err := s.EndSession("nobody", s.RefOf(there.ID)); !errors.Is(err, ErrNoSuchSession) {
		t.Errorf("EndSession for another account: %v", err)
	}
	if err := s.EndSession("ada", s.RefOf(there.ID)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Session(there.ID); ok {
		t.Error("an ended session still works")
	}

	s.EndOtherSessions("ada", here.ID)
	if _, ok := s.Session(third.ID); ok {
		t.Error("another session survived EndOtherSessions")
	}
	list = s.Sessions("ada")
	if len(list) != 1 || list[0].Ref != s.RefOf(here.ID) || list[0].Agent != agentFox {
		t.Errorf("Sessions = %+v", list)
	}
}
