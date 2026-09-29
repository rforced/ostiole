package notify

import (
	"sync"
	"testing"

	"github.com/rforced/ostiole/internal/store"
)

// memState is a store directory in memory.
type memState struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *memState) ReadState(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.files[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return raw, nil
}

func (m *memState) WriteState(name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[name] = data
	return nil
}

func (m *memState) RemoveState(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, name)
	return nil
}

func titles(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Headline()
	}
	return out
}

// looks runs n looks at the same conditions and returns what they sent.
func looks(tr *Tracker, n int, now ...Condition) []Event {
	var out []Event
	for range n {
		out = append(out, tr.Observe(now, at)...)
	}
	return out
}

var (
	gateway = Condition{Kind: "gateway-down", Title: "Gateway wan is down", Detail: "timeout"}
	undone  = Condition{Kind: "apply-undone", Title: "An unconfirmed apply was undone", Once: true}
	drive   = Condition{Kind: "drive-failing", Title: "Drive sda reports it is failing"}
)

func TestTheTrackerReportsWhatBeginsAndEnds(t *testing.T) {
	t.Parallel()
	tr := &Tracker{}
	// Once is a blip.
	if got := looks(tr, 1, gateway, undone); len(got) != 0 {
		t.Fatalf("first look = %v", titles(got))
	}
	got := looks(tr, 1, gateway, undone)
	if len(got) != 2 || got[0].Level != LevelWarn || got[0].Detail != "timeout" {
		t.Fatalf("second look = %+v", got)
	}
	// Nothing is over while the daemon is still learning what it watches,
	// however long the undo has been gone.
	if got := looks(tr, warmUp-2, gateway); len(got) != 0 {
		t.Errorf("warming up = %v", titles(got))
	}
	// Gone for one look is a flap, not an end.
	if got := looks(tr, 1); len(got) != 0 {
		t.Errorf("gone once = %v", titles(got))
	}
	if got := looks(tr, 1, gateway); len(got) != 0 {
		t.Errorf("back after a flap = %v", titles(got))
	}
	// The undo ends quietly; the gateway says it is over.
	got = looks(tr, endAfter)
	if len(got) != 1 || got[0].Headline() != "Resolved: Gateway wan is down" || got[0].Level != LevelOK {
		t.Errorf("ended = %v", titles(got))
	}
}

// A restart does not send again what already went out, and says it ended
// if it did so while nothing watched, once the daemon has had time to
// learn what it watches.
func TestTheTrackerRemembersAcrossARestart(t *testing.T) {
	t.Parallel()
	state := &memState{}
	if got := looks(&Tracker{State: state}, startAfter, gateway, drive); len(got) != 2 {
		t.Fatalf("began = %v", titles(got))
	}
	again := &Tracker{State: state}
	if got := looks(again, warmUp, drive); len(got) != 0 {
		t.Errorf("after the restart = %v", titles(got))
	}
	if got := looks(again, endAfter, drive); len(got) != 1 || got[0].Level != LevelOK || got[0].Kind != gateway.Kind {
		t.Errorf("the gateway came back while nothing watched: %v", titles(got))
	}

	// Off forgets it all, and on again reports what is wrong then.
	again.Reset()
	if _, err := state.ReadState(store.NotifyFile); err == nil {
		t.Error("the file outlived notifications being turned off")
	}
	if got := looks(&Tracker{State: state}, startAfter, drive); len(got) != 1 {
		t.Errorf("on again = %v", titles(got))
	}

	// A tracker that never looked still clears what an earlier run kept.
	if got := looks(&Tracker{State: state}, 1); len(got) != 0 {
		t.Fatal(titles(got))
	}
	(&Tracker{State: state}).Reset()
	if _, err := state.ReadState(store.NotifyFile); err == nil {
		t.Error("Reset before any look left the file")
	}
}

// A condition with a key is the same one while its title moves on, and its
// end is told in the words it last had.
func TestAKeyedConditionKeepsItsNotice(t *testing.T) {
	t.Parallel()
	tr := &Tracker{}
	soon := Condition{Kind: "certificate", Key: "web", Title: "Certificate web expires 5 October"}
	gone := Condition{Kind: "certificate", Key: "web", Title: "Certificate web has expired"}
	if got := looks(tr, startAfter, soon); len(got) != 1 {
		t.Fatalf("began = %v", titles(got))
	}
	if got := looks(tr, warmUp, gone); len(got) != 0 {
		t.Errorf("a new title = %v", titles(got))
	}
	if got := looks(tr, endAfter); len(got) != 1 || got[0].Headline() != "Resolved: Certificate web has expired" {
		t.Errorf("ended = %v", titles(got))
	}
}
