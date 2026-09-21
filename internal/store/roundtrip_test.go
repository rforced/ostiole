package store

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/rforced/ostiole/internal/diff"
	"github.com/rforced/ostiole/internal/model"
)

// A configuration with one of everything survives a save and a load
// unchanged. The next omitempty on a field whose zero means something
// would drop that setting on every apply, with nothing else to notice.
func TestFullConfigSurvivesSaveAndLoad(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../nft/testdata/full.json")
	if err != nil {
		t.Fatal(err)
	}
	var want model.Config
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	st := New(t.TempDir())
	if _, err := st.Save(&want, "table inet ostiole {}\n"); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	changes, err := diff.Compare(&want, got)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		t.Errorf("changed by a round trip: %+v", c)
	}
	a, _ := json.Marshal(&want)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Error("the loaded configuration marshals differently from the saved one")
	}
}
