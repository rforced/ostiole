// Package diff compares two configurations and reports what changed in
// terms a person can act on: paths that name rules and interfaces rather
// than array positions.
package diff

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Kind says what happened at a path.
type Kind string

// Kinds of change.
const (
	Added   Kind = "added"
	Removed Kind = "removed"
	Changed Kind = "changed"
)

// Change is one difference. Before and After hold the values as they
// appear in the configuration; the unused side of an addition or removal
// is nil.
type Change struct {
	Path   string `json:"path"`
	Kind   Kind   `json:"kind"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// String renders a change the way the CLI prints it.
func (c Change) String() string {
	switch c.Kind {
	case Added:
		return "+ " + c.Path + " = " + render(c.After)
	case Removed:
		return "- " + c.Path + " was " + render(c.Before)
	default:
		return "~ " + c.Path + ": " + render(c.Before) + " -> " + render(c.After)
	}
}

// Compare reports how b differs from a. Both are marshalled to JSON first,
// so what is compared is exactly what gets stored.
func Compare(a, b any) ([]Change, error) {
	left, err := normalise(a)
	if err != nil {
		return nil, err
	}
	right, err := normalise(b)
	if err != nil {
		return nil, err
	}
	var out []Change
	walk("", left, right, &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// normalise turns a value into the generic maps and slices JSON gives us.
func normalise(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func walk(path string, a, b any, out *[]Change) {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, Change{Path: path, Kind: Changed, Before: a, After: b})
			return
		}
		walkMap(path, left, right, out)
	case []any:
		right, ok := b.([]any)
		if !ok {
			*out = append(*out, Change{Path: path, Kind: Changed, Before: a, After: b})
			return
		}
		walkSlice(path, left, right, out)
	default:
		if !equal(a, b) {
			*out = append(*out, change(path, a, b))
		}
	}
}

func walkMap(path string, a, b map[string]any, out *[]Change) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		left, hasLeft := a[k]
		right, hasRight := b[k]
		child := join(path, k)
		switch {
		case !hasLeft:
			switch {
			case empty(right):
			case named(right):
				// Name the new entries rather than dumping the whole list.
				walkSlice(child, nil, right.([]any), out)
			default:
				*out = append(*out, Change{Path: child, Kind: Added, After: right})
			}
		case !hasRight:
			switch {
			case empty(left):
			case named(left):
				walkSlice(child, left.([]any), nil, out)
			default:
				*out = append(*out, Change{Path: child, Kind: Removed, Before: left})
			}
		default:
			walk(child, left, right, out)
		}
	}
}

// walkSlice pairs elements by their identity (an id, name, or other
// natural key) so inserting one rule does not report every rule after it
// as changed. Lists without an identity fall back to position.
func walkSlice(path string, a, b []any, out *[]Change) {
	leftKeys, leftOK := identities(a)
	rightKeys, rightOK := identities(b)
	if !leftOK || !rightOK {
		walkByPosition(path, a, b, out)
		return
	}

	left := map[string]any{}
	for i, k := range leftKeys {
		left[k] = a[i]
	}
	right := map[string]any{}
	for i, k := range rightKeys {
		right[k] = b[i]
	}
	seen := map[string]bool{}
	order := append(append([]string{}, leftKeys...), rightKeys...)
	for _, k := range order {
		if seen[k] {
			continue
		}
		seen[k] = true
		child := fmt.Sprintf("%s[%s]", path, k)
		l, hasLeft := left[k]
		r, hasRight := right[k]
		switch {
		case !hasLeft:
			*out = append(*out, Change{Path: child, Kind: Added, After: r})
		case !hasRight:
			*out = append(*out, Change{Path: child, Kind: Removed, Before: l})
		default:
			walk(child, l, r, out)
		}
	}
	// Order matters for firewall rules, so say so when it changes without
	// the contents changing.
	if len(leftKeys) == len(rightKeys) && strings.Join(leftKeys, ",") != strings.Join(rightKeys, ",") &&
		sameSet(leftKeys, rightKeys) {
		*out = append(*out, Change{
			Path:   path + " (order)",
			Kind:   Changed,
			Before: strings.Join(leftKeys, ", "),
			After:  strings.Join(rightKeys, ", "),
		})
	}
}

func walkByPosition(path string, a, b []any, out *[]Change) {
	for i := 0; i < len(a) && i < len(b); i++ {
		walk(fmt.Sprintf("%s[%d]", path, i), a[i], b[i], out)
	}
	for i := len(b); i < len(a); i++ {
		*out = append(*out, Change{Path: fmt.Sprintf("%s[%d]", path, i), Kind: Removed, Before: a[i]})
	}
	for i := len(a); i < len(b); i++ {
		*out = append(*out, Change{Path: fmt.Sprintf("%s[%d]", path, i), Kind: Added, After: b[i]})
	}
}

// identityKeys are the fields that name an element, in the order they are
// tried. Every list in the configuration uses one of them.
var identityKeys = []string{"id", "name", "interface", "mac", "hostname", "gateway", "address"}

// identities returns one key per element, and false when the list has no
// usable identity or two elements share one.
func identities(list []any) ([]string, bool) {
	keys := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		key := ""
		for _, field := range identityKeys {
			if v, ok := obj[field].(string); ok && v != "" {
				key = v
				break
			}
		}
		if key == "" || seen[key] {
			return nil, false
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys, true
}

// named reports whether a value is a list whose entries name themselves,
// like rules or interfaces. A list that appears or disappears wholesale is
// worth reporting entry by entry; a list of bare addresses is not.
func named(v any) bool {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	_, ok = identities(list)
	return ok
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, k := range a {
		seen[k]++
	}
	for _, k := range b {
		seen[k]--
		if seen[k] < 0 {
			return false
		}
	}
	return true
}

func change(path string, a, b any) Change {
	switch {
	case empty(a):
		return Change{Path: path, Kind: Added, After: b}
	case empty(b):
		return Change{Path: path, Kind: Removed, Before: a}
	}
	return Change{Path: path, Kind: Changed, Before: a, After: b}
}

// empty reports values that mean "not set", so a field appearing as false
// or "" does not read as a change.
func empty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case float64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

func equal(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func render(v any) string {
	switch t := v.(type) {
	case nil:
		return "nothing"
	case string:
		return fmt.Sprintf("%q", t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprint(int64(t))
		}
		return fmt.Sprint(t)
	case map[string]any, []any:
		raw, err := json.Marshal(t)
		if err == nil {
			return string(raw)
		}
	}
	return fmt.Sprint(v)
}
