package feeds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"unicode"
)

// bom is what some publishers put before a JSON document. encoding/json
// refuses it, and on a text list it spoils the first line.
var bom = []byte("\xef\xbb\xbf")

// isJSON reports whether a body is a JSON document rather than a list: no
// line of a list starts with a brace or a bracket.
func isJSON(raw []byte) bool {
	raw = bytes.TrimLeftFunc(raw, unicode.IsSpace)
	return len(raw) > 0 && (raw[0] == '{' || raw[0] == '[')
}

// ParseJSON reads the addresses out of a JSON document: every string that
// is one, wherever it sits, so no publisher's layout has to be known.
func ParseJSON(raw []byte) ([]string, error) {
	w := &walker{kept: map[string]bool{}}
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, bom)))
	dec.UseNumber()
	// Documents one after another, one per line, are read alike.
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not valid JSON: %w", err)
		}
		if err := w.walk(doc); err != nil {
			return nil, err
		}
	}
	if len(w.out) == 0 {
		return nil, errors.New("no addresses in this JSON")
	}
	slices.Sort(w.out)
	return w.out, nil
}

// walker collects the addresses in a document, each once.
type walker struct {
	out  []string
	kept map[string]bool
}

func (w *walker) walk(v any) error {
	switch v := v.(type) {
	case map[string]any:
		for _, x := range v {
			if err := w.walk(x); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range v {
			if err := w.walk(x); err != nil {
				return err
			}
		}
	case string:
		norm, err := normalizeAddress(v)
		if err != nil || w.kept[norm] {
			return nil // most strings are names, not addresses
		}
		w.kept[norm] = true
		w.out = append(w.out, norm)
		if len(w.out) > MaxEntries {
			return fmt.Errorf("more than %d entries", MaxEntries)
		}
	}
	return nil
}
