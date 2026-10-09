package server

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"ostiole/internal/model"
)

func (a *api) registerLogLimits(mux *router) {
	mux.HandleFunc("GET /api/v1/logs/limits", a.read(a.logLimits))
}

type logName struct {
	key    string
	name   string
	plural bool
}

var logNames = map[string]logName{
	"system.management.firewallLog.entries": {key: "firewall", name: "firewall log"},
	"services.dns.queryLog.entries":         {key: "queries", name: "query log"},
	"services.proxy.events.entries":         {key: "events", name: "WAF events", plural: true},
	"traffic.destinations.entries":          {key: "destinations", name: "destinations", plural: true},
	"services.proxy.requests.entries":       {key: "requests", name: "proxy requests", plural: true},
	"services.dhcp.log.entries":             {key: "dhcp", name: "DHCP log"},
	"wireless.log.entries":                  {key: "wireless", name: "wireless log"},
	"vpn.wireguardLog.entries":              {key: "wireguard", name: "WireGuard log"},
	"vpn.tailscaleLog.entries":              {key: "tailscale", name: "Tailscale log"},
}

type logLimit struct {
	model.MemoryLog
	logName
}

func logLimits(c *model.Config, b model.MemoryBudget) []logLimit {
	var out []logLimit
	for _, l := range b.MemoryLogs(c) {
		if n, ok := logNames[l.Path]; ok {
			out = append(out, logLimit{l, n})
		}
	}
	return out
}

type logLimitsInfo struct {
	MemTotal   int64          `json:"memTotal"`
	Reserve    int64          `json:"reserve"`
	Budget     int64          `json:"budget"`
	PeakFactor float64        `json:"peakFactor"`
	Ceilings   map[string]int `json:"ceilings"`
}

func (a *api) logLimits(w http.ResponseWriter, _ *http.Request) error {
	cfg := a.engine.Effective()
	if cfg == nil {
		cfg = &model.Config{}
	}
	b := a.budget()
	out := logLimitsInfo{PeakFactor: model.PeakFactor, Ceilings: map[string]int{}}
	if b.Total > 0 {
		out.MemTotal, out.Reserve, out.Budget = b.Total, b.Reserve(cfg), b.Logs(cfg)
	}
	for _, l := range logLimits(cfg, b) {
		out.Ceilings[l.key] = l.Ceiling
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *api) budget() model.MemoryBudget {
	n := a.memory()
	if n > math.MaxInt64 {
		n = math.MaxInt64
	}
	return model.MemoryBudget{Total: int64(n)}
}

func logsMemoryWarnings(cfg *model.Config, b model.MemoryBudget) []Warning {
	var out []Warning
	peak, room := float64(cfg.LogsFullBytes())*model.PeakFactor, b.Logs(cfg)
	if b.Total > 0 && peak > float64(room) {
		out = append(out, Warning{
			Kind: "logs", Level: "warn", Key: "logs-memory",
			Title: "The logs could take more memory than this router has for them",
			Detail: "Full, the logs in memory take " + formatBytes(peak) + "; this router has " +
				formatBytes(float64(room)) + " for them. Set fewer entries under Firewall › Log, " +
				"Services › DNS › Queries, Services › Reverse proxy › Events and Requests, Services › DHCP › Log, " +
				"Traffic › Destinations, Wireless › Log or VPN › Logs.",
		})
	}
	var clamped []string
	for _, l := range logLimits(cfg, b) {
		if !l.On || l.Size <= l.Ceiling {
			continue
		}
		verb := "keeps"
		if l.plural {
			verb = "keep"
		}
		clamped = append(clamped, fmt.Sprintf("the %s %s %s of the %s set", l.name, verb, groupDigits(l.Ceiling), groupDigits(l.Size)))
	}
	if len(clamped) > 0 {
		detail := strings.Join(clamped, "; ")
		out = append(out, Warning{
			Kind: "logs", Level: "warn", Key: "logs-clamped",
			Title:  "A log keeps fewer entries than set",
			Detail: strings.ToUpper(detail[:1]) + detail[1:] + ". This router has memory for that many.",
		})
	}
	return out
}

func groupDigits(n int) string {
	if n < 0 {
		return "-" + groupDigits(-n)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
