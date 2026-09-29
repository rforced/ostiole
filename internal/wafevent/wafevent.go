// Package wafevent is the line ostiole-proxy writes to its journal for
// each transaction the WAF had something to say about, and the daemon
// reads back for the Events tab. Both import it, so the two cannot drift,
// and it imports nothing of Coraza's, so the daemon links none of it.
package wafevent

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Format is the name the proxy registers its audit log formatter under,
// which the WAF configuration asks for with SecAuditLogFormat.
const Format = "ostiole"

// Prefix starts every line, which tells one from Caddy's own logging in
// the same journal.
const Prefix = "ostiole-waf "

// SiteSignature marks the site a WAF configuration inspects: the proxy's
// SecComponentSignature is SiteSignature and the site's ID, and the audit
// log reports it among the rulesets.
const SiteSignature = "ostiole-site:"

// Event is one transaction the WAF had something to say about. Headers
// and bodies are not logged, so nothing here carries what a visitor sent
// beyond the request line and each rule's own evidence.
type Event struct {
	Time    time.Time `json:"time"`
	ID      string    `json:"id"`
	Site    string    `json:"site,omitempty"`
	Client  string    `json:"client,omitempty"`
	Method  string    `json:"method,omitempty"`
	URI     string    `json:"uri,omitempty"`
	Status  int       `json:"status,omitempty"`
	Verdict string    `json:"verdict"`
	Engine  string    `json:"engine,omitempty"`
	Rules   []Hit     `json:"rules"`
	// MoreMatches counts the matches past MaxMatches, which are not kept.
	MoreMatches int `json:"moreMatches,omitempty"`
}

// Hit is one match of a rule.
type Hit struct {
	ID       int    `json:"id"`
	Message  string `json:"message,omitempty"`
	Data     string `json:"data,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// Verdicts, worst first.
const (
	VerdictBlocked    = "blocked"
	VerdictWouldBlock = "would-block"
	VerdictMatched    = "matched"
)

// blockingRules are the anomaly-score rules that stop a request and a
// response. A match on either under detection-only is what would have
// been blocked.
var blockingRules = []int{949110, 959100}

// Verdict is what came of a transaction: stopped, what would have been
// stopped with the engine on, or only matched.
func Verdict(interrupted bool, rules []Hit) string {
	switch {
	case interrupted:
		return VerdictBlocked
	case slices.ContainsFunc(rules, func(h Hit) bool { return slices.Contains(blockingRules, h.ID) }):
		return VerdictWouldBlock
	default:
		return VerdictMatched
	}
}

// The longest texts kept and the most matches. A rule's evidence is the
// value it matched, which can be a whole request body, and a rule that
// checks each header matches once for every header a request sends.
const (
	MaxURI     = 2048
	MaxText    = 512
	MaxMatches = 100
)

// Line is ev as the proxy writes it: Prefix and then ev in JSON, redacted
// and trimmed to what the page shows.
func Line(ev Event) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(Prefix)
	enc := json.NewEncoder(&b)
	// A request reads in the journal as it was sent: <script>, not
	// <script>.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ev.Redacted().trimmed()); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// Parse reads one journal message: false when it is not a line Line
// wrote. It redacts and trims what it reads too, since the journal is not
// the proxy's alone and holds what older proxies wrote.
func Parse(message string) (Event, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(message), Prefix)
	if !ok {
		return Event{}, false
	}
	var ev Event
	if err := json.Unmarshal([]byte(rest), &ev); err != nil || ev.Time.IsZero() {
		return Event{}, false
	}
	switch ev.Verdict {
	case VerdictBlocked, VerdictWouldBlock, VerdictMatched:
	default:
		return Event{}, false
	}
	if ev.Rules == nil {
		ev.Rules = []Hit{}
	}
	return ev.Redacted().trimmed(), true
}

// trimmed is ev with its texts cut and its matches capped, so an event
// costs about what the page shows rather than whatever a client sent. The
// first match of every rule is kept whatever the cap, since the anomaly
// score, which says why a request was stopped, comes last.
func (ev Event) trimmed() Event {
	ev.URI = cut(ev.URI, MaxURI)
	rules := ev.Rules
	if len(rules) > MaxMatches {
		keep := make([]bool, len(rules))
		n := 0
		seen := map[int]bool{}
		for i, h := range rules {
			if !seen[h.ID] {
				seen[h.ID], keep[i] = true, true
				n++
			}
		}
		for i := range rules {
			if n >= MaxMatches {
				break
			}
			if !keep[i] {
				keep[i] = true
				n++
			}
		}
		kept := make([]Hit, 0, n)
		for i, h := range rules {
			if keep[i] {
				kept = append(kept, h)
			}
		}
		ev.MoreMatches += len(rules) - len(kept)
		rules = kept
	}
	if slices.ContainsFunc(rules, func(h Hit) bool { return len(h.Message) > MaxText || len(h.Data) > MaxText }) {
		rules = slices.Clone(rules)
		for i := range rules {
			rules[i].Message = cut(rules[i].Message, MaxText)
			rules[i].Data = cut(rules[i].Data, MaxText)
		}
	}
	ev.Rules = rules
	return ev
}

// cut shortens s to at most n bytes on a character boundary, marking it.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.Clone(s) + "…"
}
