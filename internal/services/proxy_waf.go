package services

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/wafevent"
)

// The Core Rule Set application exclusion plugins, pinned in
// crs/VERSIONS and written to disk at apply time. They are static, so
// they travel with the release rather than through a render.
//
//go:embed crs/plugins/*.conf
var crsPlugins embed.FS

const crsDirName = "crs"

// crsFiles names the embedded plugin files, by their name on disk.
func crsFiles() map[string]string {
	out := map[string]string{}
	entries, err := fs.ReadDir(crsPlugins, "crs/plugins")
	if err != nil {
		return out
	}
	for _, e := range entries {
		raw, err := crsPlugins.ReadFile("crs/plugins/" + e.Name())
		if err != nil {
			continue
		}
		out[filepath.Join(crsDirName, e.Name())] = string(raw)
	}
	return out
}

// crsInclude includes an application's set file, if it ships that part.
// coraza-caddy keeps a WAF across reloads while its directives read the
// same, whatever the files they include hold, so the file's hash goes in
// first: a set that changed changes caddy.json, and the reload an apply
// then does builds the WAF again.
func crsInclude(b *strings.Builder, dir, app, part string) {
	raw, err := crsPlugins.ReadFile(fmt.Sprintf("crs/plugins/%s-%s.conf", app, part))
	if err != nil {
		return
	}
	fmt.Fprintf(b, "# sha256 %x\n", sha256.Sum256(raw))
	fmt.Fprintf(b, "Include %s\n", filepath.Join(dir, crsDirName, app+"-"+part+".conf"))
}

// cloudflareCookies matches the cookies Cloudflare's documentation lists
// it setting on a site it serves.
const cloudflareCookies = `^(?:__cf_bm|__cflb|__cfruid|__cfseq.*|__cfwaitingroom|_cfuvid|cf_clearance|cf_chl_rc_(?:i|ni|m)|cf_ob_info|cf_use_ob)$`

// The first rule ID Ostiole writes. CRS reserves 10000-99999 for local
// rules, and everything below is a counter from here. Ostiole's own
// exclusion sets take a thousand each from 20000: vaultwarden 20000-20999,
// jellyfin 21000-21999.
const wafRuleBase = 10000

// wafDirectives is one site's rule configuration, as Coraza reads it.
// The signature names the site, which is how an audit line is tied back
// to it without the rules knowing anything about Ostiole.
func wafDirectives(dir string, w model.WAFProfile, siteID string) string {
	var b strings.Builder
	engine := "DetectionOnly"
	if w.Mode == "block" {
		engine = "On"
	}
	limit := w.BodyLimitMB
	if limit == 0 {
		limit = model.DefaultBodyLimitMB
	}
	fmt.Fprintf(&b, "SecRuleEngine %s\n", engine)
	b.WriteString("SecRequestBodyAccess On\n")
	fmt.Fprintf(&b, "SecRequestBodyLimit %d\n", limit*1048576)
	b.WriteString("SecRequestBodyLimitAction ProcessPartial\n")
	if w.InspectResponses {
		b.WriteString("SecResponseBodyAccess On\n")
		b.WriteString("SecResponseBodyMimeType text/plain text/html text/xml application/json\n")
	} else {
		b.WriteString("SecResponseBodyAccess Off\n")
	}
	b.WriteString("SecResponseBodyLimit 524288\n")
	b.WriteString("SecResponseBodyLimitAction ProcessPartial\n")
	b.WriteString("SecArgumentsLimit 1000\n")
	// Not @coraza.conf-recommended: it sets SecAuditLogRelevantStatus, and
	// with that unset an entry is written exactly when a rule that logs
	// matched, which is every request that scored, in both modes.
	b.WriteString("SecAuditEngine RelevantOnly\n")
	// A, H, F, K and Z: the request line and client, the producer (the
	// site signature and the engine mode), the response status, the rules
	// that matched with their data, and the terminator. F brings the
	// response headers too, which ostiole-proxy does not write. No bodies.
	// Without K a message carries only the error line and no rule at all,
	// which is an event the page cannot say anything about.
	b.WriteString("SecAuditLogParts AHFKZ\n")
	// ostiole-proxy's own format: one short line of what the Events tab
	// shows. Coraza's JSON repeats each rule's text and error line for
	// every match, and journald cuts a line that long into pieces.
	b.WriteString("SecAuditLogFormat " + wafevent.Format + "\n")
	b.WriteString("SecAuditLogType Serial\n")
	b.WriteString("SecAuditLog /dev/stderr\n")
	// Coraza's debug log goes to the WAF's own logger, which logConfig
	// leaves out, so it is off. coraza-caddy keeps a WAF, and the logger
	// it was built with, across reloads while these lines stay the same:
	// this one also rebuilds the WAFs a proxy built before that logger was
	// left out, which would go on writing through it.
	b.WriteString("SecDebugLogLevel 0\n")
	fmt.Fprintf(&b, "SecComponentSignature %q\n", wafevent.SiteSignature+siteID)
	b.WriteString("Include @crs-setup.conf.example\n")
	// Coraza gives a rule only its own phase's default actions, and its
	// built-in log,auditlog only to phase 2. crs-setup covers phases 1, 2
	// and 5, so without these the response rules would neither log nor
	// audit, and a blocked response would be an empty 403 with no event.
	// CRS 4.29's crs-setup sets 3 and 4 as well, and Coraza refuses a
	// second default for a phase: scripts/update-crs.sh takes those out,
	// so a proxy still loads what an older daemon wrote.
	b.WriteString("SecDefaultAction \"phase:3,log,auditlog,pass\"\n")
	b.WriteString("SecDefaultAction \"phase:4,log,auditlog,pass\"\n")

	paranoia := w.Paranoia
	if paranoia == 0 {
		paranoia = model.DefaultParanoia
	}
	inbound, outbound := w.InboundThreshold, w.OutboundThreshold
	if inbound == 0 {
		inbound = model.DefaultInboundThreshold
	}
	if outbound == 0 {
		outbound = model.DefaultOutboundThreshold
	}
	id := wafRuleBase
	id++
	fmt.Fprintf(&b, "SecAction \"id:%d,phase:1,pass,nolog,setvar:tx.blocking_paranoia_level=%d,setvar:tx.detection_paranoia_level=%d\"\n",
		id, paranoia, paranoia)
	id++
	fmt.Fprintf(&b, "SecAction \"id:%d,phase:1,pass,nolog,setvar:tx.inbound_anomaly_score_threshold=%d,setvar:tx.outbound_anomaly_score_threshold=%d\"\n",
		id, inbound, outbound)
	// A JSON body is read as JSON, each value an argument of its own
	// (ARGS:json.user.email), and an XML one as XML, its text in XML:/* and
	// its attribute values in XML://@*. Otherwise CRS reads either as a
	// form, the whole document one argument name, whose quotes, braces and
	// angle brackets trip the SQL rules at paranoia 2 and up. These rules are
	// @coraza.conf-recommended's, which is left out above: a body that does
	// not parse as what it says it is gives the rules nothing to read, so it
	// is refused, or in detect mode logged. An empty body is not read at
	// all. The refusal is a 403 like any other, not the recommended 400: the
	// audit line cannot see a rule's own status, and an event says what the
	// client got.
	id++
	fmt.Fprintf(&b, "SecRule REQUEST_HEADERS:Content-Type \"@rx ^application/(?:[a-z0-9.-]+[+])?json\" \"id:%d,phase:1,t:none,t:lowercase,pass,nolog,ctl:requestBodyProcessor=JSON\"\n", id)
	id++
	fmt.Fprintf(&b, "SecRule REQUEST_HEADERS:Content-Type \"@rx ^(?:application/(?:[a-z0-9.-]+[+])?|text/)xml\" \"id:%d,phase:1,t:none,t:lowercase,pass,nolog,ctl:requestBodyProcessor=XML\"\n", id)
	id++
	fmt.Fprintf(&b, "SecRule REQBODY_ERROR \"!@eq 0\" \"id:%d,phase:2,t:none,log,deny,msg:'Failed to parse request body',logdata:'%%{reqbody_error_msg}',severity:2\"\n", id)
	// Coraza reads the first 1,000 arguments, from the query string, the
	// path and the body together, and drops the rest, where no rule sees
	// them: a thousand harmless ones in front of an attack hid it. So a
	// request with more is refused, from the query in phase 1 and from the
	// body in phase 2, as the recommended rules 200004 and 200005 do. The
	// vaultwarden set lifts the phase 2 one by its ID where a whole vault
	// is sent, so these two stay where they are.
	id++
	fmt.Fprintf(&b, "SecRule ARGUMENTS_LIMIT_REACHED \"@eq 1\" \"id:%d,phase:1,t:none,log,deny,msg:'Argument limit reached (GET/PATH args)',severity:2\"\n", id)
	id++
	fmt.Fprintf(&b, "SecRule ARGUMENTS_LIMIT_REACHED \"@eq 1\" \"id:%d,phase:2,t:none,log,deny,msg:'Argument limit reached (POST args)',severity:2\"\n", id)
	// A multipart body that repeats a part's header or a parameter, two
	// filenames say, which backends settle differently, or that breaks the
	// format in a way they might read past, is refused too, as the
	// recommended rule 200003 does. Except when it was cut at the body
	// limit, where it breaks off mid-part: the recommended config refuses
	// a body that long outright, but here the part up to the limit is read
	// and the rest let through unread, so an upload that size still works.
	id++
	fmt.Fprintf(&b, "SecRule MULTIPART_STRICT_ERROR \"!@eq 0\" \"id:%d,phase:2,t:none,log,deny,msg:'Multipart request body failed strict validation',logdata:'MULTIPART_DUPLICATE_PART_HEADER=%%{MULTIPART_DUPLICATE_PART_HEADER}, MULTIPART_INVALID_QUOTING=%%{MULTIPART_INVALID_QUOTING}',severity:2,chain\"\n", id)
	b.WriteString("SecRule INBOUND_DATA_ERROR \"@eq 0\" \"t:none\"\n")

	for _, app := range w.Applications {
		for _, part := range []string{"config", "before"} {
			crsInclude(&b, dir, app, part)
		}
	}
	for _, e := range w.Exclusions {
		if e.Path == "" {
			continue
		}
		id++
		ctl := fmt.Sprintf("ctl:ruleRemoveById=%s", e.Rule)
		if e.Target != "" {
			ctl = fmt.Sprintf("ctl:ruleRemoveTargetById=%s;%s", e.Rule, e.Target)
		}
		fmt.Fprintf(&b, "SecRule REQUEST_URI \"@beginsWith %s\" \"id:%d,phase:1,pass,nolog,%s\"\n", e.Path, id, ctl)
	}
	b.WriteString("Include @owasp_crs/*.conf\n")
	// Cloudflare's cookies are for Cloudflare, and a browser sends them to
	// every name under a zone once one name in it has been through
	// Cloudflare. Their random base64url trips the SQL comment, hex and
	// character counting rules now and then, for as long as the cookie
	// lasts. CRS leaves Google's analytics and ad cookies out the same way,
	// by name.
	b.WriteString("SecRuleUpdateTargetByTag OWASP_CRS \"!REQUEST_COOKIES:/" + cloudflareCookies + "/\"\n")
	for _, app := range w.Applications {
		crsInclude(&b, dir, app, "after")
	}
	for _, e := range w.Exclusions {
		if e.Path != "" {
			continue
		}
		if e.Target != "" {
			fmt.Fprintf(&b, "SecRuleUpdateTargetById %s %q\n", e.Rule, "!"+e.Target)
			continue
		}
		fmt.Fprintf(&b, "SecRuleRemoveById %s\n", e.Rule)
	}
	return b.String()
}
