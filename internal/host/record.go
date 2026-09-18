package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The four steps of preparing a router, in the order they have to happen.
// Packages come before anything else, because the first apply needs the
// services it turns on. The firewall takeover comes after a ruleset has
// been loaded, or it would leave the router with no firewall at all
// between masking the old one and loading ours. The leftovers can go
// whenever. Networking is last because it is the step that can drop the
// session it is being driven from.
const (
	StepPackages = "packages"
	StepFirewall = "firewall"
	StepLegacy   = "legacy"
	StepNetwork  = "network"
)

// Steps in order, for a page that lists them and a command that walks
// them.
func Steps() []string { return []string{StepPackages, StepFirewall, StepLegacy, StepNetwork} }

// Step states. A step is outstanding until the facts say otherwise, done
// when they do, and skipped when the operator has said this router is
// deliberately staying as it is — which stops the gate nagging without
// pretending the work happened.
const (
	StateOutstanding = "outstanding"
	StateDone        = "done"
	StateSkipped     = "skipped"
)

// StepState is one step, as a page shows it.
type StepState struct {
	Step  string `json:"step"`
	State string `json:"state"`
	// Detail says what is outstanding, or why it cannot be done yet.
	Detail string `json:"detail,omitempty"`
	// Skipped is when somebody chose to leave it, and who.
	Skipped time.Time `json:"skipped,omitzero"`
	By      string    `json:"by,omitempty"`
}

// Record is what an operator has chosen to leave alone. Only the skips
// are kept: whether the work is done is read from the router every time,
// because a competitor that was masked can be unmasked and a package can
// be removed by somebody else.
type Record struct {
	// Skips are the steps to stop asking about, by step name.
	Skips map[string]Skip `json:"skips,omitempty"`
}

// Skip is one deliberate omission.
type Skip struct {
	At time.Time `json:"at"`
	By string    `json:"by,omitempty"`
}

// RecordFile is the record's name inside the configuration directory.
const RecordFile = "host-setup.json"

// Load reads the record, or returns an empty one. A router that has never
// skipped anything has no file, which is not an error.
func Load(dir string) Record {
	var rec Record
	if dir == "" {
		return rec
	}
	raw, err := os.ReadFile(filepath.Join(dir, RecordFile))
	if err != nil {
		return rec
	}
	_ = json.Unmarshal(raw, &rec)
	return rec
}

// Save writes the record atomically, so a daemon that dies mid-write
// leaves the old file rather than half of a new one.
func Save(dir string, rec Record) error {
	if dir == "" {
		return errors.New("no configuration directory to write the record in")
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, RecordFile)
	tmp, err := os.CreateTemp(dir, "."+RecordFile+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// SetSkip records that a step is being left alone, or takes that back.
func SetSkip(dir, step, by string, skip bool) error {
	if !validStep(step) {
		return fmt.Errorf("%q is not a step of the host setup", step)
	}
	rec := Load(dir)
	if rec.Skips == nil {
		rec.Skips = map[string]Skip{}
	}
	if skip {
		rec.Skips[step] = Skip{At: time.Now().UTC(), By: by}
	} else {
		delete(rec.Skips, step)
	}
	return Save(dir, rec)
}

func validStep(step string) bool {
	for _, s := range Steps() {
		if s == step {
			return true
		}
	}
	return false
}

// steps works out where each step stands from what the router said and
// what the operator has chosen to leave.
func steps(rep Report, rec Record) []StepState {
	out := make([]StepState, 0, len(Steps()))
	for _, step := range Steps() {
		st := StepState{Step: step, State: StateDone}
		if detail := outstanding(rep, step); detail != "" {
			st.State, st.Detail = StateOutstanding, detail
		}
		if skip, ok := rec.Skips[step]; ok && st.State == StateOutstanding {
			st.State, st.Skipped, st.By = StateSkipped, skip.At, skip.By
		}
		out = append(out, st)
	}
	return out
}

// outstanding says what is left to do for one step, or "" when there is
// nothing.
func outstanding(rep Report, step string) string {
	switch step {
	case StepPackages:
		var missing []string
		for _, c := range rep.Components {
			if c.Outstanding() {
				missing = append(missing, c.Label)
			}
		}
		return list(missing, "%s is not set up", "%s are not set up")
	case StepFirewall:
		var names []string
		for _, c := range rep.Competitors {
			if c.Kind == "firewall" && c.Conflicts {
				names = append(names, c.Name)
			}
		}
		return list(names, "%s still filters traffic on this router",
			"%s still filter traffic on this router")
	case StepLegacy:
		var names []string
		for _, t := range rep.Legacy.Sweepable() {
			names = append(names, t.ID())
		}
		return list(names, "%s was left behind by an older firewall",
			"%s were left behind by an older firewall")
	case StepNetwork:
		if rep.Network.Pending {
			return "a network takeover is waiting to be confirmed"
		}
		if rep.Network.Owned || rep.Network.Backend == "none" {
			return ""
		}
		return list(rep.Network.Managers, "%s still owns this router's addresses",
			"%s still own this router's addresses")
	}
	return ""
}

// list renders a sentence about none, one, or several things.
func list(names []string, one, many string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf(one, names[0])
	}
	return fmt.Sprintf(many, join(names))
}

// join reads a list the way a person would say it.
func join(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// prepared reports whether the gate should let the browser past. Only
// the packages step holds it: that is the one an apply fails without.
// The old firewall cannot be retired before a ruleset is loaded, the
// leftovers can wait, and addressing is a choice, so sending somebody to
// a page whose buttons refuse to work is the wrong kind of help.
func prepared(steps []StepState) bool {
	for _, s := range steps {
		if s.Step == StepPackages && s.State == StateOutstanding {
			return false
		}
	}
	return true
}
