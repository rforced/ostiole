package requestlog

import (
	"testing"

	"ostiole/internal/logsearch"
)

// line is a request as the proxy writes it at Info, cut down.
const line = `{"level":"info","ts":1790480447.622,"logger":"http.log.access.log0","msg":"handled request",` +
	`"request":{"remote_ip":"10.0.0.2","remote_port":"51234","client_ip":"203.0.113.9","proto":"HTTP/2.0",` +
	`"method":"GET","host":"vault.example.com","uri":"/notifications/hub"},"bytes_read":0,"user_id":"",` +
	`"duration":0.0123,"size":512,"status":101,"site":"vault","user_agent":"Bitwarden_Mobile/2026.9"}`

func TestParseReadsARequestLine(t *testing.T) {
	t.Parallel()
	r, ok := Parse(line)
	want := Request{Site: "vault", Client: "203.0.113.9", Method: "GET", Host: "vault.example.com",
		Path: "/notifications/hub", Proto: "HTTP/2.0", Status: 101, Bytes: 512, Duration: 0.0123, Agent: "Bitwarden_Mobile/2026.9"}
	if !ok || r != want {
		t.Errorf("parsed %+v, %v", r, ok)
	}
}

// Only a request's line is one; a query string an older proxy left on is
// taken off; the address the connection came from stands in for a client
// address the proxy did not work out.
func TestParseKeepsOnlyRequests(t *testing.T) {
	t.Parallel()
	for _, other := range []string{
		`{"level":"info","logger":"http","msg":"server running"}`,
		`ostiole-waf {"site":"vault"}`,
		`{"logger":"http.log.access.log0",`,
	} {
		if _, ok := Parse(other); ok {
			t.Errorf("%s was read", other)
		}
	}
	old := `{"logger":"http.log.access.log0","request":{"remote_ip":"10.0.0.2","method":"GET","host":"a","uri":"/x?token=secret"},"status":200}`
	if r, ok := Parse(old); !ok || r.Path != "/x" || r.Client != "10.0.0.2" {
		t.Errorf("parsed %+v, %v", r, ok)
	}
}

// A line says who answered: the site's server unless the WAF noted its
// error, or the client got other than the server sent, or no server
// answered at all. An older proxy's line says nothing.
func TestParseReadsWhoAnswered(t *testing.T) {
	t.Parallel()
	head := `{"logger":"http.log.access.log0","request":{"remote_ip":"10.0.0.2","method":"GET","host":"a","uri":"/"},`
	for tail, want := range map[string]string{
		`"status":403,"upstream_status":403}`:                  BySite,
		`"status":403,"upstream_status":null}`:                 ByProxy,
		`"status":403,"upstream_status":null,"waf":"refused"}`: ByWAF,
		`"status":403,"upstream_status":200}`:                  ByWAF,
		`"status":403}`:                                        "",
	} {
		if r, ok := Parse(head + tail); !ok || r.By != want {
			t.Errorf("%s: by %q, want %q", tail, r.By, want)
		}
	}
}

func TestASearchReadsWhatTheTabShows(t *testing.T) {
	t.Parallel()
	r, _ := Parse(line)
	for q, want := range map[string]bool{
		"vault hub": true, "203.0.113": true, "101": true, "bitwarden": true, "POST": false, "waf": false,
	} {
		if got := Matcher(logsearch.Parse(q))(&r); got != want {
			t.Errorf("%q: %v", q, got)
		}
	}
	r.By = ByWAF
	if !Matcher(logsearch.Parse("waf 101"))(&r) {
		t.Error("a request the WAF refused is not found by WAF")
	}
}
