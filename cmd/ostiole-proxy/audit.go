package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/corazawaf/coraza/v3/experimental/plugins"
	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
	"github.com/corazawaf/coraza/v3/types"

	"ostiole/internal/wafevent"
)

// The WAF writes its audit log as the lines the daemon reads back for the
// Events tab: SecAuditLogFormat ostiole. Coraza's JSON repeats each rule's
// text and error line for every match, tens of kilobytes an event, which
// journald cuts into pieces.
func init() {
	plugins.RegisterAuditLogFormatter(wafevent.Format, auditFormat{})
}

type auditFormat struct{}

func (auditFormat) MIME() string { return "text/plain" }

// Format writes one audit log entry as a line. A part the configuration
// leaves out comes back from its accessor as a nil pointer rather than a
// nil value, so a panic here is an error, not the end of the request.
func (auditFormat) Format(al plugintypes.AuditLog) (line []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			line, err = nil, fmt.Errorf("audit log entry: %v", r)
		}
	}()
	return wafevent.Line(event(al))
}

// event is what the Events tab shows of an audit log entry: the producer,
// which names the site and the engine's mode, comes with part H, the
// response status with part F, and the rules that matched with part K.
func event(al plugintypes.AuditLog) wafevent.Event {
	tx := al.Transaction()
	ev := wafevent.Event{
		Time:   time.Unix(0, tx.UnixTimestamp()).UTC(),
		ID:     tx.ID(),
		Client: tx.ClientIP(),
		Rules:  []wafevent.Hit{},
	}
	if tx.HasRequest() {
		ev.Method, ev.URI = tx.Request().Method(), tx.Request().URI()
	}
	// Coraza records what the site answered, which a blocked client never
	// got. The anomaly-score rules deny without a status, and coraza-caddy
	// answers that with 403.
	switch {
	case tx.IsInterrupted():
		ev.Status = http.StatusForbidden
	case tx.HasResponse():
		ev.Status = tx.Response().Status()
	}
	parts := al.Parts()
	if slices.Contains(parts, types.AuditLogPartAuditLogTrailer) {
		p := tx.Producer()
		ev.Engine = p.RuleEngine()
		for _, name := range p.Rulesets() {
			if site, ok := strings.CutPrefix(name, wafevent.SiteSignature); ok {
				ev.Site = site
			}
		}
	}
	if slices.Contains(parts, types.AuditLogPartRulesMatched) {
		for _, m := range al.Messages() {
			d := m.Data()
			hit := wafevent.Hit{ID: d.ID(), Message: d.Msg(), Data: d.Data()}
			if s := d.Severity(); s != types.RuleSeverityUnset {
				hit.Severity = s.String()
			}
			ev.Rules = append(ev.Rules, hit)
		}
	}
	ev.Verdict = wafevent.Verdict(tx.IsInterrupted(), ev.Rules)
	return ev
}
