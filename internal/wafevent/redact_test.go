package wafevent

import (
	"encoding/json"
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
