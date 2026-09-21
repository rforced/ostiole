package services

import (
	"encoding/json"
	"strings"
	"time"
)

// ProxyEvent is one transaction the WAF had something to say about.
// Headers and bodies are not logged, so nothing here carries what a
// visitor sent beyond the rule's own evidence.
type ProxyEvent struct {
	Time    time.Time  `json:"time"`
	ID      string     `json:"id"`
	Site    string     `json:"site,omitempty"`
	Client  string     `json:"client,omitempty"`
	Method  string     `json:"method,omitempty"`
	URI     string     `json:"uri,omitempty"`
	Status  int        `json:"status,omitempty"`
	Verdict string     `json:"verdict"`
	Engine  string     `json:"engine,omitempty"`
	Rules   []ProxyHit `json:"rules"`
}

// ProxyHit is one rule that matched.
type ProxyHit struct {
	ID       int    `json:"id"`
	Message  string `json:"message,omitempty"`
	Data     string `json:"data,omitempty"`
	Severity string `json:"severity,omitempty"`
}

// Verdicts, worst first.
const (
	VerdictBlocked     = "blocked"
	VerdictWouldBlock  = "would-block"
	VerdictMatched     = "matched"
	proxyEventPrefix   = `{"transaction"`
	proxySiteSignature = "ostiole-site:"
)

// The anomaly-score rules that stop a request and a response. A match on
// either under detection-only is what would have been blocked.
var wafBlockingRules = []int{949110, 959100}

// auditLine is Coraza's JSON audit entry, as much of it as the events
// tab shows.
type auditLine struct {
	Transaction struct {
		Timestamp     string `json:"timestamp"`
		UnixTimestamp int64  `json:"unix_timestamp"`
		ID            string `json:"id"`
		ClientIP      string `json:"client_ip"`
		Request       *struct {
			Method string `json:"method"`
			URI    string `json:"uri"`
		} `json:"request"`
		Response *struct {
			Status int `json:"status"`
		} `json:"response"`
		Producer *struct {
			RuleEngine string   `json:"rule_engine"`
			Rulesets   []string `json:"rulesets"`
		} `json:"producer"`
		IsInterrupted bool `json:"is_interrupted"`
	} `json:"transaction"`
	Messages []struct {
		Message string `json:"message"`
		Data    *struct {
			ID       int    `json:"id"`
			Msg      string `json:"msg"`
			Data     string `json:"data"`
			Severity int    `json:"severity"`
		} `json:"data"`
	} `json:"messages"`
}

// severityNames are Coraza's, by the number it writes.
var severityNames = []string{
	"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug",
}

// ParseProxyEvent reads one journal message. A line that is not an audit
// entry — Caddy's own logging, mostly — is not one.
func ParseProxyEvent(message string, fallback time.Time) (ProxyEvent, bool) {
	message = strings.TrimSpace(message)
	if !strings.HasPrefix(message, proxyEventPrefix) {
		return ProxyEvent{}, false
	}
	var line auditLine
	if err := json.Unmarshal([]byte(message), &line); err != nil {
		return ProxyEvent{}, false
	}
	tx := line.Transaction
	ev := ProxyEvent{
		Time:   auditTime(tx.Timestamp, tx.UnixTimestamp, fallback),
		ID:     tx.ID,
		Client: tx.ClientIP,
		Rules:  []ProxyHit{},
	}
	if tx.Request != nil {
		ev.Method, ev.URI = tx.Request.Method, tx.Request.URI
	}
	if tx.Response != nil {
		ev.Status = tx.Response.Status
	}
	scored := false
	for _, m := range line.Messages {
		if m.Data == nil {
			continue
		}
		hit := ProxyHit{ID: m.Data.ID, Message: m.Data.Msg, Data: m.Data.Data}
		if hit.Message == "" {
			hit.Message = m.Message
		}
		if s := m.Data.Severity; s >= 0 && s < len(severityNames) {
			hit.Severity = severityNames[s]
		}
		for _, id := range wafBlockingRules {
			if hit.ID == id {
				scored = true
			}
		}
		ev.Rules = append(ev.Rules, hit)
	}
	if tx.Producer != nil {
		ev.Engine = tx.Producer.RuleEngine
		for _, name := range tx.Producer.Rulesets {
			if site, ok := strings.CutPrefix(name, proxySiteSignature); ok {
				ev.Site = site
			}
		}
	}
	switch {
	case tx.IsInterrupted:
		ev.Verdict = VerdictBlocked
	case scored:
		ev.Verdict = VerdictWouldBlock
	default:
		ev.Verdict = VerdictMatched
	}
	return ev, true
}

// auditLayouts are what a connector may have written the time as. Coraza
// itself uses the Apache form; coraza-caddy writes the first of these.
var auditLayouts = []string{
	"2006/01/02 15:04:05",
	"02/Jan/2006:15:04:05.999999 -0700",
	"02/Jan/2006:15:04:05 -0700",
}

// auditTime is when the transaction ran. The unix field is the reliable
// one, but its unit is whatever the connector used — coraza-caddy writes
// nanoseconds — so it is read by magnitude. The written form and then the
// journal's own timestamp are the fallbacks.
func auditTime(written string, unix int64, fallback time.Time) time.Time {
	switch {
	case unix > 1e17:
		return time.Unix(0, unix).UTC()
	case unix > 1e14:
		return time.UnixMicro(unix).UTC()
	case unix > 1e11:
		return time.UnixMilli(unix).UTC()
	case unix > 0:
		return time.Unix(unix, 0).UTC()
	}
	for _, layout := range auditLayouts {
		if t, err := time.Parse(layout, written); err == nil {
			return t.UTC()
		}
	}
	return fallback
}
