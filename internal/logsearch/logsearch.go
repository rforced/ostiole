// Package logsearch is how a log kept in memory is searched and paged: the
// words of a query, each somewhere in what a row shows with case ignored,
// and a walk from the newest entry back that stops at a page, at the start
// of the log, or at its budget. The matching is matches() in
// web/src/lib/search.js written again in Go; both read one table of cases,
// so the two cannot drift apart.
package logsearch

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// macWords are the ways a MAC, or the start of one, is written in the wild:
// aa-bb-cc as Windows writes it, aabb.ccdd as a switch does, and bare hex.
var macWords = []*regexp.Regexp{
	regexp.MustCompile(`^[0-9a-f]{2}([:-][0-9a-f]{2})*[:-][0-9a-f]{1,2}$`),
	regexp.MustCompile(`^[0-9a-f]{4}(\.[0-9a-f]{1,4})+$`),
	regexp.MustCompile(`^[0-9a-f]{6,12}$`),
}

// maxWords is how many words of a query are matched. Found words are kept
// as bits.
const maxWords = 64

// Query is a search split into the words every match must hold.
type Query struct {
	words []string
	// hex is a word written as part of a MAC with its separators taken out,
	// or empty for any other word.
	hex []string
}

// Parse splits a query into its words, lower case.
func Parse(q string) Query {
	words := strings.Fields(strings.ToLower(q))
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	out := Query{words: words, hex: make([]string, len(words))}
	for i, w := range words {
		for _, re := range macWords {
			if re.MatchString(w) {
				out.hex[i] = strings.NewReplacer(":", "", ".", "", "-", "").Replace(w)
				break
			}
		}
	}
	return out
}

// Empty reports whether the query matches everything.
func (q Query) Empty() bool { return len(q.words) == 0 }

// Adder takes the values a row shows, one by one. Row is the one that
// matters; a test records them.
type Adder interface {
	Add(v string)
	AddBytes(v []byte)
}

// Row is one row being matched: it is given the values the row shows and
// then says whether every word was among them. Nothing is allocated on the
// way for a value in ASCII.
type Row struct {
	q     *Query
	found uint64
	all   uint64
}

// Row starts matching one row.
func (q *Query) Row() Row {
	all := uint64(1)<<uint(len(q.words)) - 1
	if len(q.words) == maxWords {
		all = ^uint64(0)
	}
	return Row{q: q, all: all}
}

// Add looks for the words not yet found in one value.
func (r *Row) Add(v string) {
	if r.found == r.all || v == "" {
		return
	}
	for i, w := range r.q.words {
		if r.found&(1<<uint(i)) == 0 && contains(v, w) {
			r.found |= 1 << uint(i)
		}
	}
}

// AddBytes is Add for a value written into a buffer.
func (r *Row) AddBytes(v []byte) {
	if r.found == r.all || len(v) == 0 {
		return
	}
	for i, w := range r.q.words {
		if r.found&(1<<uint(i)) == 0 && contains(v, w) {
			r.found |= 1 << uint(i)
		}
	}
}

// AddMAC looks in a MAC, which a word written as part of one also finds
// with the separators on both sides ignored.
func (r *Row) AddMAC(v string) {
	r.Add(v)
	if r.found == r.all || v == "" {
		return
	}
	for i, h := range r.q.hex {
		if h != "" && r.found&(1<<uint(i)) == 0 && containsHex(v, h) {
			r.found |= 1 << uint(i)
		}
	}
}

// Match reports whether every word was found.
func (r *Row) Match() bool { return r.found == r.all }

// Reset starts the next row, so one Row serves a whole walk.
func (r *Row) Reset() { r.found = 0 }

// Matches is the whole of matches() for values at hand: every word of the
// query somewhere in the values or the MACs.
func Matches(query string, values, macs []string) bool {
	q := Parse(query)
	row := q.Row()
	for _, v := range values {
		row.Add(v)
	}
	for _, m := range macs {
		row.AddMAC(m)
	}
	return row.Match()
}

// contains reports whether s holds word, a lower-case word, case ignored.
// An ASCII value is compared a byte at a time; anything else is lowered
// whole, as the page lowers it.
func contains[S string | []byte](s S, word string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return strings.Contains(strings.ToLower(string(s)), word)
		}
	}
	n := len(word)
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for j < n && lower(s[i+j]) == word[j] {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

// containsHex reports whether a MAC holds hex, the separators of both left
// out and case ignored.
func containsHex(mac, hex string) bool {
	for start := 0; start < len(mac); start++ {
		if isSeparator(mac[start]) {
			continue
		}
		i, j := start, 0
		for i < len(mac) && j < len(hex) {
			if isSeparator(mac[i]) {
				i++
				continue
			}
			if lower(mac[i]) != hex[j] {
				break
			}
			i++
			j++
		}
		if j == len(hex) {
			return true
		}
	}
	return false
}

func isSeparator(b byte) bool { return b == ':' || b == '.' || b == '-' }

func lower(b byte) byte {
	if 'A' <= b && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}
