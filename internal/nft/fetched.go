package nft

import (
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// WithoutFetched is a ruleset rendered from cfg less the elements of the
// sets whose contents are fetched: aliases read from a URL or a country or
// AS list, and the bogon list. A refresh puts those in the kernel without
// an apply, so two renders of one configuration a refresh apart differ in
// them alone.
func WithoutFetched(cfg *model.Config, ruleset string) string {
	fetched := map[string]bool{bogonSetV4: true, bogonSetV6: true}
	for _, a := range cfg.Aliases {
		if !a.Fetched() {
			continue
		}
		for _, s := range AliasSets(a, nil) {
			fetched[s.Name] = true
		}
	}
	var b strings.Builder
	in := false
	for line := range strings.Lines(ruleset) {
		t := strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(t, "set "); ok && strings.HasSuffix(name, " {") {
			in = fetched[strings.TrimSuffix(name, " {")]
		} else if t == "}" {
			in = false
		} else if in && strings.HasPrefix(t, "elements = ") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}
