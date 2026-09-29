package wafevent

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func sample() Event {
	return Event{
		Time:    time.Date(2026, 9, 27, 14, 2, 11, 123456789, time.UTC),
		ID:      "Ze77",
		Site:    "shop",
		Client:  "2001:db8::7",
		Method:  "GET",
		URI:     "/?q=<script>",
		Verdict: VerdictBlocked,
		Engine:  "On",
		Rules: []Hit{
			{ID: 941100, Message: "XSS Attack Detected via libinjection", Data: "Matched Data: <script>", Severity: "critical"},
			{ID: 949110, Message: "Inbound Anomaly Score Exceeded (Total Score: 5)", Severity: "emergency"},
		},
	}
}

func TestALineReadsBackAsWritten(t *testing.T) {
	t.Parallel()
	line, err := Line(sample())
	if err != nil {
		t.Fatal(err)
	}
	s := string(line)
	if !strings.HasPrefix(s, Prefix+"{") || strings.Contains(s, "\n") {
		t.Fatalf("line = %q", s)
	}
	// The journal shows the request as it came.
	if !strings.Contains(s, `"uri":"/?q=<script>"`) {
		t.Errorf("escaped: %s", s)
	}
	got, ok := Parse(s)
	if !ok || !reflect.DeepEqual(got, sample()) {
		t.Errorf("read back %+v, %v", got, ok)
	}
}

// Parse reads the proxy's line and nothing else in its journal.
func TestParseTakesOnlyAnEventLine(t *testing.T) {
	t.Parallel()
	line, _ := Line(sample())
	for name, msg := range map[string]string{
		"caddy":        `{"level":"info","logger":"http.log.access","msg":"handled request"}`,
		"coraza json":  `{"transaction":{"id":"Ze77"},"messages":[]}`,
		"cut short":    string(line[:len(line)/2]),
		"no time":      Prefix + `{"id":"a","verdict":"blocked","rules":[]}`,
		"odd verdict":  Prefix + `{"time":"2026-09-27T14:02:11Z","id":"a","verdict":"maybe","rules":[]}`,
		"prefix alone": Prefix,
	} {
		if ev, ok := Parse(msg); ok {
			t.Errorf("%s read as %+v", name, ev)
		}
	}
	ev, ok := Parse("  " + Prefix + `{"time":"2026-09-27T14:02:11Z","id":"a","verdict":"matched"}` + "\n")
	if !ok || ev.Rules == nil || len(ev.Rules) != 0 {
		t.Errorf("an event with no rules: %+v, %v", ev, ok)
	}
}

func TestVerdict(t *testing.T) {
	t.Parallel()
	xss := []Hit{{ID: 941100}}
	for _, tc := range []struct {
		interrupted bool
		rules       []Hit
		want        string
	}{
		{true, xss, VerdictBlocked},
		{false, append(xss, Hit{ID: 949110}), VerdictWouldBlock},
		{false, []Hit{{ID: 959100}}, VerdictWouldBlock},
		{false, xss, VerdictMatched},
		{false, nil, VerdictMatched},
	} {
		if got := Verdict(tc.interrupted, tc.rules); got != tc.want {
			t.Errorf("Verdict(%v, %v) = %s, want %s", tc.interrupted, tc.rules, got, tc.want)
		}
	}
}

// What a client sent is cut to what the page quotes, on a character
// boundary, and the caller's event is left as it was.
func TestLongTextsAreCut(t *testing.T) {
	t.Parallel()
	ev := sample()
	ev.URI = "/" + strings.Repeat("a", MaxURI)
	ev.Rules[0].Data = strings.Repeat("x", MaxText-1) + "é"
	ev.Rules[1].Message = strings.Repeat("m", MaxText+1)
	got := ev.trimmed()
	if len(got.URI) != MaxURI+len("…") || !strings.HasSuffix(got.URI, "…") {
		t.Errorf("uri is %d bytes", len(got.URI))
	}
	if want := strings.Repeat("x", MaxText-1) + "…"; got.Rules[0].Data != want {
		t.Errorf("data cut inside a character: %q", got.Rules[0].Data[MaxText-4:])
	}
	if len(got.Rules[1].Message) != MaxText+len("…") {
		t.Errorf("message is %d bytes", len(got.Rules[1].Message))
	}
	if len(ev.Rules[0].Data) != MaxText+1 {
		t.Error("trimming changed the caller's rules")
	}
}

// A request that sends a restricted header a thousand times matches the
// rule a thousand times. The line keeps a hundred, counts the rest, and
// still names every rule that matched.
func TestMatchesAreCappedAndEveryRuleStays(t *testing.T) {
	t.Parallel()
	ev := Event{Time: time.Now(), ID: "a", Verdict: VerdictBlocked}
	for range 1000 {
		ev.Rules = append(ev.Rules, Hit{ID: 920450, Data: "x-middleware-subrequest"})
	}
	ev.Rules = append(ev.Rules, Hit{ID: 949110, Message: "Inbound Anomaly Score Exceeded"})
	line, err := Line(ev)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Parse(string(line))
	if !ok || len(got.Rules) != MaxMatches || got.MoreMatches != 901 {
		t.Fatalf("kept %d, %d more, %v", len(got.Rules), got.MoreMatches, ok)
	}
	if got.Rules[0].ID != 920450 || got.Rules[MaxMatches-1].ID != 949110 {
		t.Errorf("first %d, last %d", got.Rules[0].ID, got.Rules[MaxMatches-1].ID)
	}
	if len(line) > 16<<10 {
		t.Errorf("the line is %d bytes", len(line))
	}
}
