package main

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/corazawaf/coraza/v3"

	"ostiole/internal/wafevent"
)

// wafWriting is a WAF that writes its audit log to a file, set up the way
// the daemon sets up the proxy's.
func wafWriting(t *testing.T, engine, parts string, rules ...string) (coraza.WAF, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.log")
	directives := append([]string{
		"SecRuleEngine " + engine,
		"SecAuditEngine RelevantOnly",
		"SecAuditLogParts " + parts,
		"SecAuditLogFormat " + wafevent.Format,
		"SecAuditLogType Serial",
		"SecAuditLog " + path,
		`SecComponentSignature "` + wafevent.SiteSignature + `shop"`,
	}, rules...)
	waf, err := coraza.NewWAF(coraza.NewWAFConfig().WithDirectives(strings.Join(directives, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return waf, path
}

// get runs one GET through waf, which the site answers with status when
// the WAF lets it through, and hands back the lines its audit log holds.
func get(t *testing.T, waf coraza.WAF, path, uri string, status int, headers ...string) []string {
	t.Helper()
	tx := waf.NewTransaction()
	tx.ProcessConnection("2001:db8::7", 51000, "192.0.2.1", 443)
	tx.ProcessURI(uri, "GET", "HTTP/1.1")
	for _, h := range headers {
		tx.AddRequestHeader(h, "1")
	}
	if tx.ProcessRequestHeaders() == nil {
		it, err := tx.ProcessRequestBody()
		if err != nil {
			t.Fatal(err)
		}
		if it == nil {
			tx.ProcessResponseHeaders(status, "HTTP/1.1")
		}
	}
	tx.ProcessLogging()
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func TestABlockedRequestIsOneShortLine(t *testing.T) {
	t.Parallel()
	waf, path := wafWriting(t, "On", "AHFKZ",
		`SecRule ARGS:q "@contains evil" "id:1001,phase:1,deny,log,msg:'Evil query',logdata:'Matched %{MATCHED_VAR}',severity:CRITICAL"`)
	lines := get(t, waf, path, "/search?q=evil", http.StatusOK)
	if len(lines) != 1 || len(lines[0]) > 1024 {
		t.Fatalf("audit log = %q", lines)
	}
	ev, ok := wafevent.Parse(lines[0])
	if !ok {
		t.Fatalf("not an event line: %s", lines[0])
	}
	if time.Since(ev.Time) > time.Minute || ev.ID == "" {
		t.Errorf("time %v, id %q", ev.Time, ev.ID)
	}
	want := wafevent.Hit{ID: 1001, Message: "Evil query", Data: "Matched evil", Severity: "critical"}
	if ev.Site != "shop" || ev.Client != "2001:db8::7" || ev.Method != "GET" || ev.URI != "/search?q=evil" ||
		ev.Status != http.StatusForbidden || ev.Verdict != wafevent.VerdictBlocked || ev.Engine != "On" ||
		len(ev.Rules) != 1 || ev.Rules[0] != want {
		t.Errorf("event = %+v", ev)
	}
}

// Stopped on its response, a request still reads as what the client got,
// not what the site had answered.
func TestABlockOnTheResponseReadsAsTheWAFsAnswer(t *testing.T) {
	t.Parallel()
	waf, path := wafWriting(t, "On", "AHFKZ",
		`SecRule RESPONSE_STATUS "@streq 200" "id:1002,phase:3,deny,log,msg:'Leak'"`)
	ev, ok := wafevent.Parse(get(t, waf, path, "/", http.StatusOK)[0])
	if !ok || ev.Verdict != wafevent.VerdictBlocked || ev.Status != http.StatusForbidden {
		t.Errorf("event = %+v, %v", ev, ok)
	}
}

// Under detection only, the anomaly score is what would have blocked, and
// a rule that checks each header matches once for every one.
func TestDetectionOnlyReadsAsWouldBlock(t *testing.T) {
	t.Parallel()
	waf, path := wafWriting(t, "DetectionOnly", "AHFKZ",
		`SecRule REQUEST_HEADERS_NAMES "@beginsWith x-bad" "id:920450,phase:1,pass,t:lowercase,log,msg:'Restricted header',logdata:'%{MATCHED_VAR}'"`,
		`SecRule REQUEST_HEADERS_NAMES "@beginsWith x-bad" "id:949110,phase:1,pass,t:lowercase,log,msg:'Inbound Anomaly Score Exceeded'"`)
	lines := get(t, waf, path, "/", http.StatusNotFound, "X-Bad-One", "X-Bad-Two")
	ev, ok := wafevent.Parse(lines[0])
	if !ok || ev.Verdict != wafevent.VerdictWouldBlock || ev.Engine != "DetectionOnly" || ev.Status != http.StatusNotFound {
		t.Fatalf("event = %+v, %v", ev, ok)
	}
	var ids []int
	for _, h := range ev.Rules {
		ids = append(ids, h.ID)
	}
	if len(ids) != 4 || ids[0] != 920450 || ids[1] != 920450 || ids[3] != 949110 {
		t.Errorf("rules %v", ids)
	}
}

// A configuration without H, F or K leaves out the site, the site's
// answer and the rules, and the line is written without them rather than
// failing.
func TestAPartLeftOutIsLeftOut(t *testing.T) {
	t.Parallel()
	waf, path := wafWriting(t, "DetectionOnly", "AZ",
		`SecRule ARGS:q "@contains evil" "id:1001,phase:1,deny,log,msg:'Evil query'"`)
	lines := get(t, waf, path, "/?q=evil", http.StatusOK)
	ev, ok := wafevent.Parse(lines[0])
	if !ok || ev.Site != "" || ev.Engine != "" || ev.Status != 0 || len(ev.Rules) != 0 || ev.Verdict != wafevent.VerdictMatched {
		t.Errorf("event = %+v, %v", ev, ok)
	}
}

// A path under a prefix the configuration names in a signature is written
// without its tail, in the URI and in what each rule quotes of it, while
// the same request without the signature shows the token, and a path
// outside the prefix is left as it is.
func TestAPathUnderAHiddenPrefixIsWrittenWithoutItsTail(t *testing.T) {
	t.Parallel()
	rule := `SecRule REQUEST_URI|REQUEST_FILENAME|REQUEST_BASENAME|REQUEST_LINE "@rx tok[0-9]+" ` +
		`"id:1003,phase:1,pass,log,capture,msg:'Token',severity:CRITICAL,` +
		`logdata:'Matched Data: %{TX.0} found within %{MATCHED_VAR_NAME}: %{MATCHED_VAR}'"`
	sign := `SecComponentSignature "` + wafevent.RedactSignature + `/s/"`

	waf, path := wafWriting(t, "DetectionOnly", "AHFKZ", sign, `SecComponentSignature "`+wafevent.RedactSignature+`/feed/"`, rule)
	line := get(t, waf, path, "/s/tok42?a=1", http.StatusOK)[0]
	if strings.Contains(line, "tok42") {
		t.Errorf("the line holds the token: %s", line)
	}
	ev, ok := wafevent.Parse(line)
	if !ok || ev.Site != "shop" || ev.URI != "/s/REDACTED?a=1" {
		t.Fatalf("event = %+v, %v", ev, ok)
	}
	var within []string
	for _, h := range ev.Rules {
		_, w, _ := strings.Cut(h.Data, " found within ")
		within = append(within, w)
	}
	for _, want := range []string{
		"REQUEST_URI: /s/REDACTED?a=1",
		"REQUEST_FILENAME: /s/REDACTED",
		"REQUEST_BASENAME: REDACTED",
		"REQUEST_LINE: GET /s/REDACTED?a=1 HTTP/1.1",
	} {
		if !slices.Contains(within, want) {
			t.Errorf("no rule quotes %q, they quote %q", want, within)
		}
	}

	waf, path = wafWriting(t, "DetectionOnly", "AHFKZ", rule)
	if line := get(t, waf, path, "/s/tok42?a=1", http.StatusOK)[0]; !strings.Contains(line, "/s/tok42?a=1") {
		t.Errorf("without the signature the line is %s", line)
	}

	waf, path = wafWriting(t, "DetectionOnly", "AHFKZ", sign, rule)
	if ev, _ := wafevent.Parse(get(t, waf, path, "/static/tok42", http.StatusOK)[0]); ev.URI != "/static/tok42" {
		t.Errorf("a path outside the prefix reads %q", ev.URI)
	}
	lines := get(t, waf, path, "/s/tok42", http.StatusOK)
	if ev, _ := wafevent.Parse(lines[len(lines)-1]); ev.URI != "/s/REDACTED" {
		t.Errorf("a second request reads %q", ev.URI)
	}
}
