package notify

import (
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/store"
)

// Condition is a state the router is in, such as a dashboard warning or a
// release waiting. A notice goes out as it begins and, unless Once, as it
// ends.
type Condition struct {
	Kind string
	// Key tells one condition of a kind from another; the title does
	// when it is empty. A certificate's warning names its date, so the
	// certificate is its key.
	Key    string
	Title  string
	Detail string
	// Level is LevelWarn when empty.
	Level string
	// Once is a condition whose end says nothing new: an apply undone at
	// start, a release that has since been installed.
	Once bool
}

func (c Condition) key() string {
	id := c.Key
	if id == "" {
		id = c.Title
	}
	return c.Kind + "\x00" + id
}

// State keeps the tracker's file; the store is one.
type State interface {
	ReadState(name string) ([]byte, error)
	WriteState(name string, data []byte) error
	RemoveState(name string) error
}

// Looks come once a minute. A condition has to be there twice in a row to
// be sent and gone twice in a row to be over, so a unit restarting or a
// line redialling is not a pair of notices. For the first ten looks after
// a start nothing is over: what the daemon learns by watching, a
// gateway's health or a drive's, is not known yet.
const (
	startAfter = 2
	endAfter   = 2
	warmUp     = 10
)

// Tracker remembers which conditions a notice went out for, on disk, so
// that a daemon that starts again does not send them again.
type Tracker struct {
	// State keeps the set; nil keeps it in memory only.
	State State
	Log   *slog.Logger

	mu      sync.Mutex
	loaded  bool
	looks   int
	active  map[string]entry
	absent  map[string]int
	present map[string]int
}

// entry is what is written of a condition: enough to say it ended.
type entry struct {
	Kind  string `json:"kind"`
	Key   string `json:"key,omitempty"`
	Title string `json:"title"`
	Once  bool   `json:"once,omitempty"`
}

func (t *Tracker) load() {
	if t.loaded {
		return
	}
	t.loaded = true
	t.active, t.absent, t.present = map[string]entry{}, map[string]int{}, map[string]int{}
	if t.State == nil {
		return
	}
	raw, err := t.State.ReadState(store.NotifyFile)
	if err != nil {
		return
	}
	var list []entry
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	for _, k := range list {
		t.active[Condition{Kind: k.Kind, Key: k.Key, Title: k.Title}.key()] = k
	}
}

func (t *Tracker) save() {
	if t.State == nil {
		return
	}
	list := make([]entry, 0, len(t.active))
	for _, k := range t.active {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Kind+list[i].Title < list[j].Kind+list[j].Title
	})
	raw, err := json.Marshal(list)
	if err == nil {
		err = t.State.WriteState(store.NotifyFile, raw)
	}
	if err != nil && t.Log != nil {
		t.Log.Warn("could not record which notices went out; a restart may send them again", "err", err)
	}
}

// Observe compares the conditions the router is in now with the ones it
// was in, and returns a notice for each that began and each that ended.
func (t *Tracker) Observe(now []Condition, at time.Time) []Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.load()
	t.looks++
	var out []Event
	changed := false
	seen := map[string]bool{}
	for _, c := range now {
		k := c.key()
		if seen[k] {
			continue
		}
		seen[k] = true
		delete(t.absent, k)
		if e, ok := t.active[k]; ok {
			// The end is reported in the words the condition last had.
			if e.Title != c.Title {
				e.Title = c.Title
				t.active[k] = e
				changed = true
			}
			continue
		}
		if t.present[k]++; t.present[k] < startAfter {
			continue
		}
		delete(t.present, k)
		t.active[k] = entry{Kind: c.Kind, Key: c.Key, Title: c.Title, Once: c.Once}
		changed = true
		level := c.Level
		if level == "" {
			level = LevelWarn
		}
		out = append(out, Event{Kind: c.Kind, Level: level, Title: c.Title, Detail: c.Detail, Time: at})
	}
	for k := range t.present {
		if !seen[k] {
			delete(t.present, k)
		}
	}
	keys := make([]string, 0, len(t.active))
	for k := range t.active {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if seen[k] || t.looks <= warmUp {
			continue
		}
		if t.absent[k]++; t.absent[k] < endAfter {
			continue
		}
		c := t.active[k]
		delete(t.active, k)
		delete(t.absent, k)
		changed = true
		if !c.Once {
			out = append(out, Event{Kind: c.Kind, Level: LevelOK, Title: c.Title, Time: at})
		}
	}
	if changed {
		t.save()
	}
	return out
}

// Reset forgets every condition, which is what turning notifications off
// does: nothing is entry about a router that sends nothing, and turning
// them on again reports what is wrong at that moment.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Before the first load the file may hold an earlier run's set.
	kept := !t.loaded || len(t.active) > 0
	t.loaded = true
	t.active, t.absent, t.present = map[string]entry{}, map[string]int{}, map[string]int{}
	if kept && t.State != nil {
		if err := t.State.RemoveState(store.NotifyFile); err != nil && t.Log != nil {
			t.Log.Warn("could not forget which notices went out", "err", err)
		}
	}
}
