package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ostiole/internal/logsearch"
	"ostiole/internal/logsearch/logsearchtest"
)

var (
	alice = Actor{Name: "alice", Kind: Account, Role: "admin", Address: "192.0.2.5"}
	bob   = Actor{Name: "bob", Kind: Account, Role: "operator", Address: "192.0.2.6"}
)

func seqs(events []Event) []uint64 {
	out := make([]uint64, len(events))
	for i, e := range events {
		out[i] = e.Seq
	}
	return out
}

func equal(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEntriesAreNumberedAndKeptInOrder(t *testing.T) {
	l := Open(t.TempDir())
	l.Add(Event{Action: SignIn, By: alice})
	l.Add(Event{Action: Apply, By: alice})
	l.Add(Event{Action: Confirm, By: bob, Target: "alice"})
	got, err := l.Events()
	if err != nil {
		t.Fatal(err)
	}
	if !equal(seqs(got), []uint64{1, 2, 3}) {
		t.Fatalf("seqs = %v", seqs(got))
	}
	if got[2].By != bob || got[2].Target != "alice" || got[2].Time.IsZero() {
		t.Errorf("last = %+v", got[2])
	}
	if got[0].Text != "" {
		t.Errorf("the sentence was kept in the file: %q", got[0].Text)
	}
}

func TestAReopenedLogNumbersOn(t *testing.T) {
	dir := t.TempDir()
	Open(dir).Add(Event{Action: SignIn, By: alice})
	Open(dir).Add(Event{Action: SignIn, By: bob})
	got, err := Open(dir).Events()
	if err != nil {
		t.Fatal(err)
	}
	if !equal(seqs(got), []uint64{1, 2}) {
		t.Fatalf("seqs = %v", seqs(got))
	}
}

func TestTwoWritersNeverShareANumber(t *testing.T) {
	dir := t.TempDir()
	daemon, shell := Open(dir), Open(dir)
	var wg sync.WaitGroup
	for _, l := range []*Log{daemon, shell} {
		wg.Go(func() {
			for range 20 {
				l.Add(Event{Action: SignIn, By: alice})
			}
		})
	}
	wg.Wait()
	got, err := daemon.Events()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 40 {
		t.Fatalf("%d entries, want 40", len(got))
	}
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d numbered %d", i, e.Seq)
		}
	}
}

func TestQueryPagesNewestFirstAndSearches(t *testing.T) {
	l := Open(t.TempDir())
	for range 3 {
		l.Add(Event{Action: SignIn, By: alice})
	}
	l.Add(Event{Action: AccountRole, By: alice, Target: "bob", Detail: "viewer"})
	l.Add(Event{Action: SignIn, By: bob})
	ctx := context.Background()

	page, held, oldest, err := l.Query(ctx, logsearch.Parse(""), 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if held != 5 || oldest.IsZero() || !equal(seqs(page.Entries), []uint64{5, 4}) || !page.More {
		t.Fatalf("first page = %v more %v, held %d", seqs(page.Entries), page.More, held)
	}
	if page.Entries[1].Text != "Changed bob's role to viewer" {
		t.Errorf("text = %q", page.Entries[1].Text)
	}
	page, _, _, err = l.Query(ctx, logsearch.Parse(""), page.Next, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(seqs(page.Entries), []uint64{3, 2}) {
		t.Fatalf("second page = %v", seqs(page.Entries))
	}

	for q, want := range map[string][]uint64{
		"192.0.2.6":    {5},
		"role viewer":  {4},
		"signed alice": {3, 2, 1},
		"operator":     {5},
		"nobody":       {},
	} {
		page, _, _, err := l.Query(ctx, logsearch.Parse(q), 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if !equal(seqs(page.Entries), want) {
			t.Errorf("%q found %v, want %v", q, seqs(page.Entries), want)
		}
	}
}

func TestClearKeepsTheNumbering(t *testing.T) {
	l := Open(t.TempDir())
	l.Add(Event{Action: SignIn, By: alice})
	l.Add(Event{Action: SignIn, By: bob})
	if err := l.Clear(); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.Events(); len(got) != 0 {
		t.Fatalf("after Clear: %v", got)
	}
	l.Add(Event{Action: LogClear, By: alice, Target: "audit log"})
	got, err := Open(l.dir).Events()
	if err != nil {
		t.Fatal(err)
	}
	if !equal(seqs(got), []uint64{3}) {
		t.Fatalf("seqs = %v, want [3]", seqs(got))
	}
}

func TestEntriesPastAYearGo(t *testing.T) {
	l := Open(t.TempDir())
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	l.Add(Event{Action: SignIn, By: alice, Time: now.Add(-Keep - time.Hour)})
	l.Add(Event{Action: SignIn, By: bob, Time: now.Add(-time.Hour)})
	got, err := l.Events()
	if err != nil {
		t.Fatal(err)
	}
	if !equal(seqs(got), []uint64{2}) {
		t.Fatalf("seqs = %v", seqs(got))
	}
	l.Add(Event{Action: SignIn, By: bob})
	raw, err := os.ReadFile(filepath.Join(l.dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if n := countLines(raw); n != 2 {
		t.Errorf("the file kept %d lines, want 2:\n%s", n, raw)
	}
}

func TestTheOldestGoPastTheCap(t *testing.T) {
	l := Open(t.TempDir())
	l.limit = 3
	for range 5 {
		l.Add(Event{Action: SignIn, By: alice})
	}
	got, err := l.Events()
	if err != nil {
		t.Fatal(err)
	}
	if !equal(seqs(got), []uint64{3, 4, 5}) {
		t.Fatalf("seqs = %v", seqs(got))
	}
}

func TestALineCutShortIsSkipped(t *testing.T) {
	l := Open(t.TempDir())
	l.Add(Event{Action: SignIn, By: alice})
	f, err := os.OpenFile(filepath.Join(l.dir, FileName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":2,"time":"2026-10`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := l.Events()
	if err != nil {
		t.Fatal(err)
	}
	if !equal(seqs(got), []uint64{1}) {
		t.Fatalf("seqs = %v", seqs(got))
	}
}

func TestSubscribersHearWhatIsRecorded(t *testing.T) {
	l := Open(t.TempDir())
	ch, cancel := l.Subscribe(4)
	defer cancel()
	l.Add(Event{Action: Reboot, By: alice})
	select {
	case e := <-ch:
		if e.Seq != 1 || e.Text != "Rebooted the router" {
			t.Errorf("heard %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("heard nothing")
	}
}

func TestANilLogRecordsNothing(t *testing.T) {
	var l *Log
	l.Add(Event{Action: SignIn, By: alice})
	if err := l.Clear(); err != nil {
		t.Fatal(err)
	}
}

func TestSearchMatchesWhatThePageShows(t *testing.T) {
	for _, c := range logsearchtest.Cases(t, "audit") {
		var e Event
		if err := json.Unmarshal(c.Entry, &e); err != nil {
			t.Fatal(err)
		}
		var got logsearchtest.Recorder
		e.Search(&got)
		if len(got) != len(c.Values) {
			t.Errorf("%s: values %q, want %q", c.Why, got, c.Values)
			continue
		}
		for i := range got {
			if got[i] != c.Values[i] {
				t.Errorf("%s: values %q, want %q", c.Why, got, c.Values)
				break
			}
		}
	}
}

func TestShellActorIsWhoLoggedIn(t *testing.T) {
	names := map[string]string{"0": "root", "1000": "carol"}
	lookup := func(uid string) string {
		if n, ok := names[uid]; ok {
			return n
		}
		return uid
	}
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		why      string
		loginUID string
		env      map[string]string
		uid      int
		want     Actor
	}{
		{"sudo after an SSH login", "1000", map[string]string{"SUDO_USER": "carol"}, 0,
			Actor{Name: "carol", Kind: Shell}},
		{"root over SSH", "0", map[string]string{"SSH_CONNECTION": "192.0.2.7 50000 192.0.2.1 22"}, 0,
			Actor{Name: "root", Kind: Shell, Address: "192.0.2.7"}},
		{"no login recorded, sudo", unsetLoginUID, map[string]string{"SUDO_USER": "carol"}, 0,
			Actor{Name: "carol", Kind: Shell}},
		{"no login recorded", "", map[string]string{"SSH_CLIENT": "2001:db8::7 50000 22"}, 1000,
			Actor{Name: "carol", Kind: Shell, Address: "2001:db8::7"}},
		{"a user ID with no name", unsetLoginUID, nil, 4242, Actor{Name: "4242", Kind: Shell}},
	}
	for _, c := range cases {
		got := shellActor(func() string { return c.loginUID }, env(c.env), lookup, c.uid)
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.why, got, c.want)
		}
	}
}

func countLines(raw []byte) int {
	n := 0
	for _, b := range raw {
		if b == '\n' {
			n++
		}
	}
	return n
}
