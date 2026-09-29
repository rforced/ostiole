package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/corazawaf/coraza/v3"
)

// XML-RPC's <param> and RSS's <link> are HTML's empty elements, which
// Coraza's own reader closes as they open, so their end tags did not
// parse. A document is read for its attribute values and its text, a
// DTD's entities are left as they are, and a document cut off is read up
// to the cut.
func TestAnXMLDocumentIsReadAsXML(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, body  string
		attrs, text []string
	}{
		{"xml-rpc", `<?xml version="1.0"?><methodCall><methodName>wp.getUsersBlogs</methodName>` +
			`<params><param><value><string>admin</string></value></param></params></methodCall>`,
			nil, []string{"wp.getUsersBlogs", "admin"}},
		{"rss", `<rss version="2.0"><channel><link>https://example.com/</link><title>News</title></channel></rss>`,
			[]string{"2.0"}, []string{"https://example.com/", "News"}},
		{"cdata", `<a b="x &amp; y"><![CDATA[<script>]]></a>`, []string{"x & y"}, []string{"<script>"}},
		{"dtd", `<!DOCTYPE x [<!ENTITY e "boom">]><x>&e;</x>`, nil, []string{"&e;"}},
		{"cut off", `<order id="7"><item>Tea</item><note>Leave`, []string{"7"}, []string{"Tea", "Leave"}},
		{"empty", ``, nil, nil},
	} {
		attrs, text, err := readXML(strings.NewReader(c.body))
		if err != nil || !slices.Equal(attrs, c.attrs) || !slices.Equal(text, c.text) {
			t.Errorf("%s: attrs %q, text %q, %v", c.name, attrs, text, err)
		}
	}
	for _, body := range []string{`<<order>`, `<a></b>`, "<a>\x00</a>"} {
		if _, _, err := readXML(strings.NewReader(body)); err == nil {
			t.Errorf("%q read", body)
		}
	}
}

// The WAF reads an XML body through it: a value in an XML-RPC <param> is
// in XML:/*, where a rule finds it.
func TestTheWAFReadsXMLThroughIt(t *testing.T) {
	t.Parallel()
	waf, err := coraza.NewWAF(coraza.NewWAFConfig().WithDirectives(strings.Join([]string{
		"SecRuleEngine On",
		"SecRequestBodyAccess On",
		`SecRule REQUEST_HEADERS:Content-Type "@rx xml" "id:1001,phase:1,pass,nolog,ctl:requestBodyProcessor=XML"`,
		`SecRule REQBODY_ERROR "!@eq 0" "id:1002,phase:2,deny,status:400"`,
		`SecRule XML:/* "@streq evil" "id:1003,phase:2,deny,status:403"`,
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		value string
		rule  int
	}{{"evil", 1003}, {"fine", 0}} {
		tx := waf.NewTransaction()
		tx.ProcessURI("/xmlrpc.php", http.MethodPost, "HTTP/1.1")
		tx.AddRequestHeader("Content-Type", "text/xml")
		if it := tx.ProcessRequestHeaders(); it != nil {
			t.Fatalf("headers: %+v", it)
		}
		body := `<methodCall><methodName>demo.echo</methodName><params><param><value><string>` + c.value +
			`</string></value></param></params></methodCall>`
		if it, _, err := tx.WriteRequestBody([]byte(body)); it != nil || err != nil {
			t.Fatalf("%s: body: %+v, %v", c.value, it, err)
		}
		it, err := tx.ProcessRequestBody()
		if err != nil {
			t.Fatal(err)
		}
		rule := 0
		if it != nil {
			rule = it.RuleID
		}
		if rule != c.rule {
			t.Errorf("%s: stopped by %d, want %d", c.value, rule, c.rule)
		}
		if err := tx.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
