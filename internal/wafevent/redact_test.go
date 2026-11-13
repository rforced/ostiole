package wafevent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// token is a made-up JSON web token: {"alg":"none"}, {"sub":"x"} and a
// signature.
const token = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"

// A token in a query, under a credential's name or none, in a path, or
// cut short in a rule's evidence, is replaced; what a request or a rule
// says otherwise is left as it was.
func TestCredentialsAreRedacted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"/notifications/hub?access_token=" + token, "/notifications/hub?access_token=REDACTED"},
		{"/?q=%3Cscript%3E&api_key=abc123&page=2", "/?q=%3Cscript%3E&api_key=REDACTED&page=2"},
		{"/?X-Amz-Signature=f00d&Password=hunter2&sessionId=7", "/?X-Amz-Signature=REDACTED&Password=REDACTED&sessionId=REDACTED"},
		{"/?access%5Ftoken=abc", "/?access%5Ftoken=REDACTED"},
		{"/cb?state=s&x=" + token, "/cb?state=s&x=REDACTED"},
		{"/t/" + token + "/go", "/t/REDACTED/go"},
		{"/?token=&q=1", "/?token=&q=1"},
		{"/turkeyJerkyRecipes?author", "/turkeyJerkyRecipes?author"},
	} {
		if got := redactURI(c.in); got != c.want {
			t.Errorf("URI %s became %s, want %s", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"Matched Data: -UCNif found within ARGS:access_token: " + token,
			"Matched Data: REDACTED found within ARGS:access_token: REDACTED"},
		{"Matched Data: x found within ARGS_GET:access_token: eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiJ9.eyJuYmYiOjE3…",
			"Matched Data: REDACTED found within ARGS_GET:access_token: REDACTED"},
		{"Matched Data: Bearer found within REQUEST_HEADERS:Authorization: Bearer abc.def",
			"Matched Data: REDACTED found within REQUEST_HEADERS:Authorization: REDACTED"},
		{"Matched Data: ' found within REQUEST_COOKIES:theme: dark'",
			"Matched Data: REDACTED found within REQUEST_COOKIES:theme: REDACTED"},
		{"Matched Data: eyJhbGciOiJub25lIn0.eyJzdW… found within ARGS:x: eyJhbGciOiJub25lIn0.eyJzdW…",
			"Matched Data: REDACTED… found within ARGS:x: REDACTED…"},
		{"Matched Data: ' found within REQUEST_URI: /hub?access_token=abc'def&q=1",
			"Matched Data: REDACTED found within REQUEST_URI: /hub?access_token=REDACTED&q=1"},
		{"Matched Data: ' found within REQUEST_URI: /search?q=it's&access_token=abc",
			"Matched Data: ' found within REQUEST_URI: /search?q=it's&access_token=REDACTED"},
		// Jellyfin signs in with {"Username": ..., "Pw": ...}.
		{"Matched Data: s&1c found within ARGS:json.Pw: hunter2' or 1=1--",
			"Matched Data: REDACTED found within ARGS:json.Pw: REDACTED"},
		{"Matched Data: ' or 1=1 found within ARGS_POST:json.NewPw: x' or 1=1",
			"Matched Data: REDACTED found within ARGS_POST:json.NewPw: REDACTED"},
		{`Matched Data: ' or 1=1 found within REQUEST_BODY: {"Username":"alice","Pw":"hunter2' or 1=1--"}`,
			`Matched Data: REDACTED found within REQUEST_BODY: {"Username":"alice","Pw":"REDACTED"}`},
		{`Matched Data: <x found within REQUEST_BODY: {"Comment":"<x onload>", "password": "cut off he`,
			`Matched Data: <x found within REQUEST_BODY: {"Comment":"<x onload>", "password": "REDACTED`},
		{"Matched Data: pa'ss found within XML:/*: pa'ss",
			"Matched Data: REDACTED found within XML:/*: REDACTED"},
		{"Matched Data: <script> found within ARGS:q: <script>alert(1)</script>",
			"Matched Data: <script> found within ARGS:q: <script>alert(1)</script>"},
		{"Found 3 byte(s) outside the range", "Found 3 byte(s) outside the range"},
	} {
		if got := redactData(c.in); got != c.want {
			t.Errorf("data %s became %s, want %s", c.in, got, c.want)
		}
	}
}

// Nothing the proxy writes, nor anything the daemon reads back, holds the
// token, whichever wrote it: an older proxy's line still has it.
func TestLinesAndParsedEventsHoldNoCredentials(t *testing.T) {
	t.Parallel()
	ev := Event{
		Time: time.Now().UTC(), ID: "1", Site: "vault", Method: "GET", Verdict: VerdictMatched,
		URI: "/notifications/hub?access_token=" + token,
		Rules: []Hit{{ID: 942432, Message: "Restricted SQL Character Anomaly Detection (args)",
			Data: "Matched Data: -UCNif found within ARGS:access_token: " + token}},
	}
	line, err := Line(ev)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), "eyJ") || strings.Contains(string(line), "UCNif") {
		t.Errorf("the proxy writes %s", line)
	}
	if ev.Rules[0].Data == Redacted || !strings.Contains(ev.URI, token) {
		t.Error("redacting changed the event it was given")
	}
	old, err := jsonLine(ev)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Parse(old)
	if !ok || got.URI != "/notifications/hub?access_token=REDACTED" ||
		got.Rules[0].Data != "Matched Data: REDACTED found within ARGS:access_token: REDACTED" {
		t.Errorf("an older proxy's line reads back as %+v", got)
	}
	if again := got.Redacted(); again.URI != got.URI || again.Rules[0].Data != got.Rules[0].Data {
		t.Errorf("redacting twice gave %+v", again)
	}
}

// jsonLine is ev as a proxy from before redaction wrote it.
func jsonLine(ev Event) (string, error) {
	b, err := json.Marshal(ev)
	return Prefix + string(b), err
}

var hiddenPrefixes = []string{"/feed/", "/s/"}

// The tail of a path under a listed prefix is hidden in the URI, the query
// stays, and a bare prefix, a near miss or a prefix not at the start of
// the path is left alone. The shortest prefix the path starts with wins.
func TestHidingPathsInTheURI(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		prefixes []string
		in, want string
	}{
		{hiddenPrefixes, "/s/tok42", "/s/REDACTED"},
		{hiddenPrefixes, "/s/tok42?a=1&b=2", "/s/REDACTED?a=1&b=2"},
		{hiddenPrefixes, "/s/tok42?", "/s/REDACTED?"},
		{hiddenPrefixes, "/s/", "/s/"},
		{hiddenPrefixes, "/s", "/s"},
		{hiddenPrefixes, "/s/?a=1", "/s/?a=1"},
		{hiddenPrefixes, "/shared/tok42", "/shared/tok42"},
		{hiddenPrefixes, "/static/app.js", "/static/app.js"},
		{hiddenPrefixes, "/t/s/tok42", "/t/s/tok42"},
		{hiddenPrefixes, "/feed/tok42.ics", "/feed/REDACTED"},
		{hiddenPrefixes, "/s/a/b/c", "/s/REDACTED"},
		{hiddenPrefixes, "/", "/"},
		{hiddenPrefixes, "", ""},
		{[]string{"/s/x/", "/s/"}, "/s/x/y", "/s/REDACTED"},
		{[]string{"/s/x/", "/s/"}, "/s/x/", "/s/REDACTED"},
		{[]string{"/s/x/"}, "/s/x/", "/s/x/"},
		{[]string{"", "/s/"}, "/other", "/other"},
		{nil, "/s/tok42", "/s/tok42"},
	} {
		if got := (Event{URI: c.in}).HidingPaths(c.prefixes).URI; got != c.want {
			t.Errorf("%q under %q became %q, want %q", c.in, c.prefixes, got, c.want)
		}
	}
}

// What a rule quotes of the path is cut the same way, and a match that
// holds, lies in or runs into the tail is hidden whole.
func TestHidingPathsInARulesEvidence(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ uri, in, want string }{
		{"/s/tok42?a=1",
			"Matched Data: ? found within REQUEST_URI: /s/tok42?a=1",
			"Matched Data: ? found within REQUEST_URI: /s/REDACTED?a=1"},
		{"/feed/tok42.ics",
			"Matched Data: /feed found within REQUEST_FILENAME: /feed/tok42.ics",
			"Matched Data: /feed found within REQUEST_FILENAME: /feed/REDACTED"},
		{"/s/tok42",
			"Matched Data: GET found within REQUEST_LINE: GET /s/tok42 HTTP/1.1",
			"Matched Data: GET found within REQUEST_LINE: GET /s/REDACTED HTTP/1.1"},
		// The match is in the tail, or runs into it from either end.
		{"/s/tok42",
			"Matched Data: ok4 found within REQUEST_FILENAME: /s/tok42",
			"Matched Data: REDACTED found within REQUEST_FILENAME: /s/REDACTED"},
		{"/s/tok42?a=1",
			"Matched Data: 42?a found within REQUEST_URI: /s/tok42?a=1",
			"Matched Data: REDACTED found within REQUEST_URI: /s/REDACTED?a=1"},
		{"/s/tok42",
			"Matched Data: s/to found within REQUEST_URI: /s/tok42",
			"Matched Data: REDACTED found within REQUEST_URI: /s/REDACTED"},
		{"/s/tok42",
			"Matched Data: /s/tok42 found within REQUEST_URI: /s/tok42",
			"Matched Data: REDACTED found within REQUEST_URI: /s/REDACTED"},
		// REQUEST_FILENAME is decoded.
		{"/s/a%3Db",
			"Matched Data: x found within REQUEST_FILENAME: /s/a=b",
			"Matched Data: x found within REQUEST_FILENAME: /s/REDACTED"},
		// Evidence cut short, as a long value is.
		{"/s/tok42abcdef",
			"Matched Data: x found within REQUEST_URI: /s/tok42ab…",
			"Matched Data: x found within REQUEST_URI: /s/REDACTED…"},
		// The last segment of the path is the end of the tail alone.
		{"/feed/tok42.ics",
			"Matched Data: .ics found within REQUEST_BASENAME: tok42.ics",
			"Matched Data: REDACTED found within REQUEST_BASENAME: REDACTED"},
		{"/s/tok42",
			"Matched Data: x found within REQUEST_HEADERS:Referer: https://example.com/s/tok42",
			"Matched Data: x found within REQUEST_HEADERS:Referer: https://example.com/s/REDACTED"},
		{"/s/tok42", "Request /s/tok42 refused", "Request /s/REDACTED refused"},
		{"/s/tok42", "Found 3 byte(s) outside the range", "Found 3 byte(s) outside the range"},
		{"/s/tok42",
			"Matched Data: <script> found within ARGS:q: <script>alert(1)</script>",
			"Matched Data: <script> found within ARGS:q: <script>alert(1)</script>"},
		{"/static/tok42",
			"Matched Data: x found within REQUEST_URI: /static/tok42",
			"Matched Data: x found within REQUEST_URI: /static/tok42"},
	} {
		ev := Event{URI: c.uri, Rules: []Hit{{ID: 1, Data: c.in}}}.HidingPaths(hiddenPrefixes)
		if got := ev.Rules[0].Data; got != c.want {
			t.Errorf("%s: %s became %s, want %s", c.uri, c.in, got, c.want)
		}
	}
}

// Hiding gives back a new event, the line holds no tail, and an event with
// nothing to hide comes back as it was.
func TestHidingPathsLeavesTheGivenEventAndTheLineHoldsNoTail(t *testing.T) {
	t.Parallel()
	ev := Event{
		Time: time.Now().UTC(), ID: "1", Site: "share", Method: "GET", Verdict: VerdictMatched,
		URI: "/feed/tok42.ics?x=1",
		Rules: []Hit{
			{ID: 920440, Data: "Matched Data: .ics found within REQUEST_URI: /feed/tok42.ics?x=1"},
			{ID: 920441, Data: "Matched Data: tok42.ics found within REQUEST_BASENAME: tok42.ics"},
		},
	}
	hidden := ev.HidingPaths(hiddenPrefixes)
	if !strings.Contains(ev.URI, "tok42") || !strings.Contains(ev.Rules[0].Data, "tok42") {
		t.Error("hiding changed the event it was given")
	}
	line, err := Line(hidden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), "tok42") || !strings.Contains(string(line), "/feed/REDACTED?x=1") {
		t.Errorf("the proxy writes %s", line)
	}
	got, ok := Parse(string(line))
	if !ok || got.URI != "/feed/REDACTED?x=1" || got.Rules[1].Data != "Matched Data: REDACTED found within REQUEST_BASENAME: REDACTED" {
		t.Errorf("the line reads back as %+v", got)
	}
	if !reflect.DeepEqual(ev.HidingPaths(nil), ev) || !reflect.DeepEqual(ev.HidingPaths([]string{"/other/"}), ev) {
		t.Error("an event with nothing to hide changed")
	}
}
