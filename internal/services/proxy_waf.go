package services

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/model"
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

// crsHas reports whether an application ships the given part.
func crsHas(app, part string) bool {
	_, err := crsPlugins.Open(fmt.Sprintf("crs/plugins/%s-%s.conf", app, part))
	return err == nil
}

// The first rule ID Ostiole writes. CRS reserves 10000-99999 for local
// rules, and everything below is a counter from here.
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
	// A, H, K and Z: the request line and client, the producer — which is
	// where the site signature and the engine mode are — the rules that
	// matched with their data, and the terminator. No headers, no bodies.
	// Without K a message carries only the error line and no rule at all,
	// which is an event the page cannot say anything about.
	b.WriteString("SecAuditLogParts AHKZ\n")
	b.WriteString("SecAuditLogFormat json\n")
	b.WriteString("SecAuditLogType Serial\n")
	b.WriteString("SecAuditLog /dev/stderr\n")
	fmt.Fprintf(&b, "SecComponentSignature %q\n", "ostiole-site:"+siteID)
	b.WriteString("Include @crs-setup.conf.example\n")

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

	for _, app := range w.Applications {
		for _, part := range []string{"config", "before"} {
			if crsHas(app, part) {
				fmt.Fprintf(&b, "Include %s\n", filepath.Join(dir, crsDirName, app+"-"+part+".conf"))
			}
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
	for _, app := range w.Applications {
		if crsHas(app, "after") {
			fmt.Fprintf(&b, "Include %s\n", filepath.Join(dir, crsDirName, app+"-after.conf"))
		}
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
