package feeds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/rforced/ostiole/internal/model"
)

// Choice is one way a JSON list can be narrowed: a field and the values it
// holds, or, with no field, the keys addresses are listed under.
type Choice struct {
	Field  string   `json:"field,omitempty"`
	Values []string `json:"values"`
}

// Bounds on what a document offers to select on. A field with hundreds of
// values is an ID or a timestamp, not something anybody picks from, and a
// long value is prose.
const (
	maxChoiceFields = 32
	maxChoiceValues = 256
	maxChoiceLen    = 64
	// A field holding a longer array is data rather than a label, and an
	// object with more fields than this is a record, not a grouping.
	maxFactArray  = 16
	maxFactFields = 64
	// maxSurveyFields bounds what is counted while the choices are worked
	// out, before the ones that narrow nothing are dropped.
	maxSurveyFields = 128
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
// is one, wherever it sits, so no publisher's layout has to be known. sel
// keeps some of them, as model.Alias.Select describes. The choices are what
// the whole document can be narrowed by, whatever sel kept, so a picker can
// offer all of it.
func ParseJSON(raw []byte, sel []string) ([]string, []Choice, error) {
	groups, err := compileSelect(sel)
	if err != nil {
		return nil, nil, err
	}
	w := &walker{groups: groups, full: 1<<len(groups) - 1, kept: map[string]bool{}, survey: newSurvey()}
	if len(groups) > 0 {
		w.all = map[string]bool{}
	}
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
			return nil, nil, fmt.Errorf("not valid JSON: %w", err)
		}
		if err := w.walk(doc, nil, 0); err != nil {
			return nil, nil, err
		}
	}
	found := len(w.kept)
	if w.all != nil {
		found = len(w.all)
	}
	choices := w.survey.choices()
	switch {
	case found == 0:
		return nil, nil, errors.New("no addresses in this JSON")
	case len(w.out) == 0:
		return nil, choices, fmt.Errorf("none of the %d addresses in it matched the selection", found)
	}
	slices.Sort(w.out)
	return w.out, choices, nil
}

// group is the conditions on one field, any of which will do, or, with no
// field, the key names.
type group struct {
	field string
	exact map[string]bool // lower case
	globs []*regexp.Regexp
}

func (g *group) match(s, lower string) bool {
	if g.exact[lower] {
		return true
	}
	for _, re := range g.globs {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func compileSelect(sel []string) ([]*group, error) {
	var groups []*group
	byField := map[string]*group{}
	for _, s := range sel {
		c, err := model.ParseCondition(s)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(c.Field)
		g := byField[key]
		if g == nil {
			if len(groups) == model.MaxSelectFields {
				return nil, fmt.Errorf("conditions on at most %d different fields", model.MaxSelectFields)
			}
			g = &group{field: c.Field, exact: map[string]bool{}}
			byField[key] = g
			groups = append(groups, g)
		}
		if strings.Contains(c.Value, "*") {
			g.globs = append(g.globs, glob(c.Value))
		} else {
			g.exact[strings.ToLower(c.Value)] = true
		}
	}
	return groups, nil
}

// glob turns a value with a * in it into a pattern that ignores case.
func glob(v string) *regexp.Regexp {
	parts := strings.Split(v, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile("(?is)^" + strings.Join(parts, ".*") + "$")
}

// walker goes through a document, keeping the addresses the selection
// wants and counting what could be selected.
type walker struct {
	groups []*group
	full   uint64 // one bit per group, all set: every condition holds
	out    []string
	kept   map[string]bool
	// all is every address found, when a selection may leave some out.
	all    map[string]bool
	survey *survey
	// met numbers each address as it is met, so a count taken twice for
	// one address, from two levels of the document, is taken once.
	met int
}

// scope is one step on the way down to a value: an object and its plain
// fields, or the key a value was reached through.
type scope struct {
	up     *scope
	key    string
	lkey   string
	pick   bool // the key can be offered to select on
	fields []fact
}

// fact is a plain field of an object: a string, a number or a boolean, or
// one of a short array of them. Addresses are not facts; they are what the
// facts describe.
type fact struct {
	name, value   string
	lname, lvalue string
	pick          bool
}

func (w *walker) walk(v any, at *scope, done uint64) error {
	switch v := v.(type) {
	case map[string]any:
		obj := &scope{up: at, fields: plainFields(v)}
		done |= w.matchFields(obj.fields)
		for k, x := range v {
			switch x.(type) {
			case map[string]any, []any, string:
			default:
				continue // a number or a boolean is never an address
			}
			lk := strings.ToLower(k)
			in := &scope{up: obj, key: k, lkey: lk, pick: pickableName(k)}
			if err := w.walk(x, in, done|w.matchKey(k, lk)); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range v {
			if err := w.walk(x, at, done); err != nil {
				return err
			}
		}
	case string:
		return w.address(v, at, done)
	}
	return nil
}

func (w *walker) address(s string, at *scope, done uint64) error {
	norm, err := normalizeAddress(s)
	if err != nil {
		return nil // most strings are names, not addresses
	}
	w.met++
	w.survey.count(norm, at, w.met)
	if w.all != nil {
		w.all[norm] = true
	}
	if done != w.full || w.kept[norm] {
		return nil
	}
	w.kept[norm] = true
	w.out = append(w.out, norm)
	if len(w.out) > MaxEntries {
		return fmt.Errorf("more than %d entries", MaxEntries)
	}
	return nil
}

// matchFields is the groups an object's plain fields satisfy.
func (w *walker) matchFields(fields []fact) uint64 {
	var done uint64
	for i, g := range w.groups {
		if g.field == "" {
			continue
		}
		for _, f := range fields {
			if strings.EqualFold(f.name, g.field) && g.match(f.value, f.lvalue) {
				done |= 1 << i
				break
			}
		}
	}
	return done
}

// matchKey is the groups a key satisfies, which only the bare names can.
func (w *walker) matchKey(k, lower string) uint64 {
	var done uint64
	for i, g := range w.groups {
		if g.field == "" && g.match(k, lower) {
			done |= 1 << i
		}
	}
	return done
}

func plainFields(m map[string]any) []fact {
	var out []fact
	add := func(name string, v any) {
		if s, ok := scalar(v); ok && len(out) < maxFactFields {
			out = append(out, newFact(name, s))
		}
	}
	for k, x := range m {
		if list, ok := x.([]any); ok {
			if len(list) <= maxFactArray {
				for _, e := range list {
					add(k, e)
				}
			}
			continue
		}
		add(k, x)
	}
	return out
}

// scalar is the text of a string, a number or a boolean that is not an
// address.
func scalar(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "", false
		}
		if _, err := model.ParseAddress(s); err == nil {
			return "", false
		}
		return s, true
	case json.Number:
		return v.String(), true
	case bool:
		return strconv.FormatBool(v), true
	}
	return "", false
}

func newFact(name, value string) fact {
	name = strings.TrimSpace(name)
	return fact{
		name: name, value: value,
		lname: strings.ToLower(name), lvalue: strings.ToLower(value),
		pick: pickableName(name) && pickableValue(value),
	}
}

// pickableName reports whether a field or key name can be offered: short
// and printable, with no = or * that a condition would read as syntax.
func pickableName(s string) bool {
	return s != "" && len(s) <= maxChoiceLen && !strings.ContainsAny(s, "=*") &&
		!strings.ContainsFunc(s, unicode.IsControl)
}

// pickableValue reports whether a value can be offered: short and
// printable, and with no * that a condition would read as a pattern.
func pickableValue(s string) bool {
	return s != "" && len(s) <= maxChoiceLen && !strings.Contains(s, "*") &&
		!strings.ContainsFunc(s, unicode.IsControl)
}

// survey counts, for every field value and key in a document, the addresses
// of each family under it, to find the ones worth selecting on.
type survey struct {
	total  [2]int
	fields map[string]*fieldTally // by lower-case name
	keys   map[string]*tally      // by lower-case key
	// tooManyKeys drops the keys of a document keyed by something nobody
	// picks from, like one key per host.
	tooManyKeys bool
}

type fieldTally struct {
	name    string
	values  map[string]*tally // by lower-case value
	tooMany bool
}

// tally is how many addresses of each family sit under one value or key.
type tally struct {
	name string
	n    [2]int
	last int
}

func (t *tally) add(family, id int) {
	if t.last == id {
		return
	}
	t.last = id
	t.n[family]++
}

func newSurvey() *survey {
	return &survey{fields: map[string]*fieldTally{}, keys: map[string]*tally{}}
}

func (s *survey) count(addr string, at *scope, id int) {
	family := 0
	if strings.Contains(addr, ":") {
		family = 1
	}
	s.total[family]++
	for sc := at; sc != nil; sc = sc.up {
		if sc.pick {
			s.key(sc, family, id)
		}
		for i := range sc.fields {
			if sc.fields[i].pick {
				s.field(&sc.fields[i], family, id)
			}
		}
	}
}

func (s *survey) key(sc *scope, family, id int) {
	if s.tooManyKeys {
		return
	}
	t := s.keys[sc.lkey]
	if t == nil {
		if len(s.keys) == maxChoiceValues {
			s.tooManyKeys, s.keys = true, nil
			return
		}
		t = &tally{name: sc.key}
		s.keys[sc.lkey] = t
	}
	t.add(family, id)
}

func (s *survey) field(f *fact, family, id int) {
	ft := s.fields[f.lname]
	if ft == nil {
		if len(s.fields) == maxSurveyFields {
			return
		}
		ft = &fieldTally{name: f.name, values: map[string]*tally{}}
		s.fields[f.lname] = ft
	}
	if ft.tooMany {
		return
	}
	t := ft.values[f.lvalue]
	if t == nil {
		if len(ft.values) == maxChoiceValues {
			ft.tooMany, ft.values = true, nil
			return
		}
		t = &tally{name: f.value}
		ft.values[f.lvalue] = t
	}
	t.add(family, id)
}

// choices is every field and key that narrows the document. One that every
// address sits under narrows nothing, and one that only parts the IPv4
// addresses from the IPv6 ones does what an alias's two sets already do.
func (s *survey) choices() []Choice {
	var out []Choice
	for _, lname := range slices.Sorted(maps.Keys(s.fields)) {
		ft := s.fields[lname]
		if ft.tooMany || !slices.ContainsFunc(slices.Collect(maps.Values(ft.values)), s.splits) {
			continue
		}
		out = append(out, Choice{Field: ft.name, Values: names(slices.Collect(maps.Values(ft.values)))})
		if len(out) == maxChoiceFields {
			break
		}
	}
	if !s.tooManyKeys {
		keys := slices.DeleteFunc(slices.Collect(maps.Values(s.keys)), func(t *tally) bool { return !s.splits(t) })
		if len(keys) > 0 {
			out = append(out, Choice{Values: names(keys)})
		}
	}
	return out
}

// splits reports whether keeping only what sits under a value leaves some
// addresses out, and more than one whole family.
func (s *survey) splits(t *tally) bool {
	n := t.n[0] + t.n[1]
	switch {
	case n == 0 || n == s.total[0]+s.total[1]:
		return false
	case t.n[0] == s.total[0] && t.n[1] == 0, t.n[1] == s.total[1] && t.n[0] == 0:
		return false
	}
	return true
}

// names is the tallies' names in the order a picker lists them.
func names(ts []*tally) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.name)
	}
	slices.SortFunc(out, func(a, b string) int {
		if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return out
}
