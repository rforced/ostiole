package wafevent

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Redacted stands in for a credential a visitor sent.
const Redacted = "REDACTED"

// Redacted is ev without the credentials a visitor sent: the values of the
// query parameters named like one, anything shaped like a JSON web token,
// and what a rule quotes of a parameter, cookie or header named like one.
// An exclusion is made from a rule and a name, never a value, so nothing
// the Events tab is for goes with them. A cut token is still a token's
// start, so it goes too.
func (ev Event) Redacted() Event {
	ev.URI = redactURI(ev.URI)
	if ev.Rules != nil {
		rules := make([]Hit, len(ev.Rules))
		for i, h := range ev.Rules {
			h.Data = redactData(h.Data)
			rules[i] = h
		}
		ev.Rules = rules
	}
	return ev
}

// HidingPaths is ev with the tail of its path under one of prefixes, a share
// link's token say, replaced by Redacted, in the URI and wherever a rule
// quotes it. A bare prefix has no tail. Of the prefixes the path starts
// with the shortest counts, as in the request log's filter. A match that
// holds the tail, lies in it or runs into it is hidden whole. Only this
// request's own tail is known, so a rule's quote of another path stays.
func (ev Event) HidingPaths(prefixes []string) Event {
	path, query, hasQuery := strings.Cut(ev.URI, "?")
	prefix := ""
	for _, p := range prefixes {
		if p != "" && len(path) > len(p) && strings.HasPrefix(path, p) && (prefix == "" || len(p) < len(prefix)) {
			prefix = p
		}
	}
	if prefix == "" {
		return ev
	}
	// The WAF reads some variables decoded, REQUEST_FILENAME among them.
	tails := []string{path[len(prefix):]}
	if d, err := url.PathUnescape(tails[0]); err == nil && d != tails[0] {
		tails = append(tails, d)
	}
	ev.URI = prefix + Redacted
	if hasQuery {
		ev.URI += "?" + query
	}
	if ev.Rules != nil {
		rules := slices.Clone(ev.Rules)
		for i := range rules {
			rules[i].Data = hideData(rules[i].Data, prefix, tails)
		}
		ev.Rules = rules
	}
	return ev
}

// hideData hides the tails in what a rule quotes. A quote of the whole
// path, as REQUEST_URI, REQUEST_FILENAME and REQUEST_LINE give it, keeps
// the prefix; REQUEST_BASENAME is the end of the tail alone.
func hideData(data, prefix string, tails []string) string {
	m := evidence.FindStringSubmatch(data)
	if m == nil {
		return hideTails(data, prefix, tails)
	}
	head, match, within, variable, name, value := m[1], m[2], m[3], m[4], m[5], m[6]
	if name != "" {
		variable += ":" + name
	}
	if match != "" && overlaps(match, tails) {
		match = Redacted
	} else {
		match = hideTails(match, prefix, tails)
	}
	if m[4] == "REQUEST_BASENAME" && value != "" && overlaps(value, tails) {
		value = Redacted
	} else {
		value = hideTails(value, prefix, tails)
	}
	return head + match + within + variable + ": " + value
}

// hideTails replaces what follows each prefix in s as far as it reads like
// a tail, since a rule's quote may be cut short.
func hideTails(s, prefix string, tails []string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			break
		}
		i += len(prefix)
		b.WriteString(s[:i])
		s = s[i:]
		if n := hiddenLen(s, tails); n > 0 {
			b.WriteString(Redacted)
			s = s[n:]
		}
	}
	b.WriteString(s)
	return b.String()
}

// hiddenLen is how much of the start of s a tail has in common with it.
func hiddenLen(s string, tails []string) int {
	n := 0
	for _, t := range tails {
		i := 0
		for i < len(s) && i < len(t) && s[i] == t[i] {
			i++
		}
		n = max(n, i)
	}
	return n
}

// overlaps says whether s holds a tail, lies in one, starts like one or
// shares an end with one. A match that only shares a character is hidden
// too: hiding what was not a secret costs little.
func overlaps(s string, tails []string) bool {
	return hiddenLen(s, tails) > 0 || slices.ContainsFunc(tails, func(t string) bool {
		return strings.Contains(t, s) || strings.Contains(s, t) || endsWithStartOf(s, t) || endsWithStartOf(t, s)
	})
}

// endsWithStartOf says whether a ends with some start of b.
func endsWithStartOf(a, b string) bool {
	for n := min(len(a), len(b)); n > 0; n-- {
		if strings.HasSuffix(a, b[:n]) {
			return true
		}
	}
	return false
}

// jwts is a JSON web token or the start of one: its header is JSON, so
// base64url of `{"`.
var jwts = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]*){0,2}`)

func redactURI(uri string) string {
	path, query, ok := strings.Cut(uri, "?")
	path = jwts.ReplaceAllString(path, Redacted)
	if !ok {
		return path
	}
	params := strings.Split(query, "&")
	for i, p := range params {
		params[i] = redactParam(p)
	}
	return path + "?" + strings.Join(params, "&")
}

// redactParam is one name=value of a query.
func redactParam(p string) string {
	name, value, ok := strings.Cut(p, "=")
	if !ok {
		return jwts.ReplaceAllString(p, Redacted)
	}
	if value != "" && secretName(name) {
		return name + "=" + Redacted
	}
	return name + "=" + jwts.ReplaceAllString(value, Redacted)
}

// evidence is how CRS writes what a rule matched: "Matched Data: <match>
// found within ARGS:access_token: <value>", or of a variable without
// names, "... found within REQUEST_URI: <value>".
var evidence = regexp.MustCompile(`(?s)^(Matched Data: )(.*?)( found within )([A-Z_]+)(?::([^:\s][^:]*))?: (.*)$`)

// pairs are the name=value pairs of a URI or body a rule quotes.
var pairs = regexp.MustCompile(`([^=&?;\s]+)=([^&;\s]*)`)

// jsonPairs are the "name": "value" pairs of a JSON body a rule quotes,
// the value perhaps cut off at the end.
var jsonPairs = regexp.MustCompile(`"((?:[^"\\]|\\.){1,64})"(\s*:\s*)"((?:[^"\\]|\\.)*)("?)`)

func redactData(data string) string {
	m := evidence.FindStringSubmatch(data)
	if m == nil {
		text, _ := redactText(data)
		return text
	}
	prefix, match, within, variable, name, value := m[1], m[2], m[3], m[4], m[5], m[6]
	if name != "" {
		variable += ":" + name
	}
	if secretVariable(m[4], name) {
		return prefix + Redacted + within + variable + ": " + Redacted
	}
	value, secrets := redactText(value)
	// What matched may be a piece of a value just hidden, or hold it.
	if match != "" && slices.ContainsFunc(secrets, func(s string) bool {
		return strings.Contains(s, match) || strings.Contains(match, s)
	}) {
		match = Redacted
	} else {
		match, _ = redactText(match)
	}
	return prefix + match + within + variable + ": " + value
}

// redactText hides the credentials in what a rule quotes, and returns
// the values it hid under a credential's name.
func redactText(s string) (string, []string) {
	var secrets []string
	s = pairs.ReplaceAllStringFunc(s, func(p string) string {
		name, value, _ := strings.Cut(p, "=")
		if value == "" || !secretName(name) {
			return p
		}
		secrets = append(secrets, value)
		return name + "=" + Redacted
	})
	s = jsonPairs.ReplaceAllStringFunc(s, func(p string) string {
		g := jsonPairs.FindStringSubmatch(p)
		if g[3] == "" || !secretName(g[1]) {
			return p
		}
		secrets = append(secrets, g[3])
		return `"` + g[1] + `"` + g[2] + `"` + Redacted + g[4]
	})
	return jwts.ReplaceAllString(s, Redacted), secrets
}

// secretVariable says whether a rule's variable holds a credential: every
// cookie, the parameters and headers named like one, and the text of an
// XML body, which comes without the names of what it was.
func secretVariable(variable, name string) bool {
	switch variable {
	case "REQUEST_COOKIES", "XML":
		return true
	case "ARGS", "ARGS_GET", "ARGS_POST", "REQUEST_HEADERS", "MULTIPART_PART_HEADERS":
		return secretName(name)
	}
	return false
}

// A name is a credential's when, in lower case and without its separators,
// it holds one of secretParts or ends in one of secretEnds. Hiding a value
// that was not a secret costs little; showing one that was costs more.
var (
	secretParts = []string{
		"token", "secret", "pass", "apikey", "signature", "credential",
		"session", "sessid", "auth", "jwt", "bearer", "cookie",
	}
	secretEnds = []string{"key", "sig", "pw", "pwd", "pswd", "pin", "otp", "code", "sid", "ticket", "hash"}
)

func secretName(name string) bool {
	if n, err := url.QueryUnescape(name); err == nil {
		name = n
	}
	n := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return -1
	}, name)
	return slices.ContainsFunc(secretParts, func(p string) bool { return strings.Contains(n, p) }) ||
		slices.ContainsFunc(secretEnds, func(e string) bool { return strings.HasSuffix(n, e) })
}
