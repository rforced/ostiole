package requestlog

import (
	"testing"

	"github.com/rforced/ostiole/internal/logsearch"
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

func TestASearchReadsWhatTheTabShows(t *testing.T) {
	t.Parallel()
	r, _ := Parse(line)
	for q, want := range map[string]bool{
		"vault hub": true, "203.0.113": true, "101": true, "bitwarden": true, "POST": false,
	} {
		if got := Matcher(logsearch.Parse(q))(&r); got != want {
			t.Errorf("%q: %v", q, got)
		}
	}
}
