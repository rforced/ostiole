package services

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ostiole/internal/model"
	"ostiole/internal/wafevent"
)

// Each prefix to hide is one more signature, written right after the
// site's, and with none the directives are what they were: the same bytes
// with no mention of a prefix.
func TestWAFDirectivesNameThePrefixesToHide(t *testing.T) {
	t.Parallel()
	const dir = "/etc/ostiole/proxy"
	profile := model.WAFProfile{Mode: "block"}
	none := wafDirectives(dir, profile, "shop")
	if strings.Contains(none, wafevent.RedactSignature) || wafDirectives(dir, profile, "shop", nil...) != none {
		t.Errorf("with no prefix:\n%s", none)
	}
	site := `SecComponentSignature "` + wafevent.SiteSignature + "shop\"\n"
	if !strings.Contains(none, site) {
		t.Fatalf("no site signature in\n%s", none)
	}
	feed := `SecComponentSignature "` + wafevent.RedactSignature + "/feed/\"\n"
	share := `SecComponentSignature "` + wafevent.RedactSignature + "/s/\"\n"
	with := wafDirectives(dir, profile, "shop", "/feed/", "/s/")
	if !strings.Contains(with, site+feed+share+"Include @crs-setup.conf.example\n") {
		t.Errorf("with two prefixes:\n%s", with)
	}
	if strings.Replace(strings.Replace(with, feed, "", 1), share, "", 1) != none {
		t.Error("the prefixes changed more than their own lines")
	}
}

// The prefixes are the proxy's, not a site's: a WAF site's directives name
// those of every enabled site, the site without a WAF included. With none
// the rendered files are the golden's.
func TestRenderedWAFNamesThePrefixesOfEveryEnabledSite(t *testing.T) {
	t.Parallel()
	p := &Proxy{
		Dir:      "/etc/ostiole/proxy",
		SelfCert: "/etc/ostiole/tls/cert.pem",
		SelfKey:  "/etc/ostiole/tls/key.pem",
	}
	cfg := loadConfig(t, "testdata/proxy.json")
	files, err := p.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/proxy.proxy")
	if err != nil {
		t.Fatal(err)
	}
	if files.String() != string(golden) || strings.Contains(files.String(), wafevent.RedactSignature) {
		t.Error("with no prefix the rendered files are not the golden")
	}

	sites := cfg.Services.Proxy.Sites
	for i := range sites {
		switch sites[i].ID {
		case "shop":
			sites[i].RedactPaths = []string{"/s/"}
		case "status":
			sites[i].RedactPaths = []string{"/feed/"}
		}
	}
	files, err = p.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(`SecComponentSignature "` + wafevent.SiteSignature + "shop\"\n" +
		`SecComponentSignature "` + wafevent.RedactSignature + "/feed/\"\n" +
		`SecComponentSignature "` + wafevent.RedactSignature + "/s/\"\n" +
		"Include @crs-setup.conf.example\n")
	if err != nil {
		t.Fatal(err)
	}
	raw := files[proxyConfName]
	if !strings.Contains(raw, strings.Trim(string(want), `"`)) || strings.Count(raw, wafevent.RedactSignature) != 2 {
		t.Errorf("the WAF directives do not name the prefixes:\n%s", raw)
	}
}
