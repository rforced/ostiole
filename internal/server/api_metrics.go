package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/version"
)

func (a *api) registerMetrics(mux *router) {
	mux.HandleFunc("GET /metrics", a.readNoEngine(a.metrics))
}

// metrics answers in the Prometheus text format. It is deliberately
// hand-rolled: the numbers come from places Ostiole already reads, and a
// client library would be a dependency for a few dozen lines of text.
//
// It needs a viewer, which an API token can be, so scraping does not mean
// handing a monitoring system the keys to the firewall.
func (a *api) metrics(w http.ResponseWriter, r *http.Request) error {
	var b strings.Builder
	m := &metricWriter{b: &b}

	m.gauge("ostiole_build_info", "The running version, always 1.", 1,
		"version", version.Version, "commit", version.Commit)

	if a.engine != nil {
		st, err := a.engine.Status(r.Context())
		if err == nil {
			m.gauge("ostiole_configured", "1 when a configuration has been saved.", boolValue(st.Configured))
			m.gauge("ostiole_ruleset_loaded", "1 when the Ostiole nftables table is in the kernel.", boolValue(st.TableLoaded))
			m.gauge("ostiole_apply_pending", "1 while an apply is waiting to be confirmed.", boolValue(st.Pending != nil))
		}
		if counters, err := a.engine.Counters(r.Context()); err == nil {
			m.help("ostiole_rule_packets_total", "Packets matched by a firewall rule.", "counter")
			m.help("ostiole_rule_bytes_total", "Bytes matched by a firewall rule.", "counter")
			ids := make([]string, 0, len(counters))
			for id := range counters {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				m.sample("ostiole_rule_packets_total", float64(counters[id].Packets), "rule", id)
				m.sample("ostiole_rule_bytes_total", float64(counters[id].Bytes), "rule", id)
			}
		}
	}

	if links, err := network.Discover(); err == nil {
		m.help("ostiole_interface_up", "1 when the interface has carrier.", "gauge")
		m.help("ostiole_interface_receive_bytes_total", "Bytes received since the link came up.", "counter")
		m.help("ostiole_interface_transmit_bytes_total", "Bytes sent since the link came up.", "counter")
		m.help("ostiole_interface_receive_packets_total", "Packets received since the link came up.", "counter")
		m.help("ostiole_interface_transmit_packets_total", "Packets sent since the link came up.", "counter")
		for _, l := range links {
			if l.Kind == "loopback" {
				continue
			}
			m.sample("ostiole_interface_up", boolValue(l.Carrier), "interface", l.Name)
			m.sample("ostiole_interface_receive_bytes_total", float64(l.RXBytes), "interface", l.Name)
			m.sample("ostiole_interface_transmit_bytes_total", float64(l.TXBytes), "interface", l.Name)
			m.sample("ostiole_interface_receive_packets_total", float64(l.RXPackets), "interface", l.Name)
			m.sample("ostiole_interface_transmit_packets_total", float64(l.TXPackets), "interface", l.Name)
		}
	}

	if a.gateways != nil {
		statuses := a.gateways.Statuses()
		if len(statuses) > 0 {
			m.help("ostiole_gateway_up", "1 when the gateway answers its monitor.", "gauge")
			m.help("ostiole_gateway_latency_seconds", "Round trip time to the monitored address.", "gauge")
			m.help("ostiole_gateway_loss_ratio", "Share of recent probes that went unanswered.", "gauge")
			m.help("ostiole_gateway_active", "1 for the gateway currently carrying the default route.", "gauge")
			for _, g := range statuses {
				m.sample("ostiole_gateway_up", boolValue(g.Online), "gateway", g.Name, "interface", g.Interface)
				m.sample("ostiole_gateway_latency_seconds", g.LatencyMS/1000, "gateway", g.Name, "interface", g.Interface)
				m.sample("ostiole_gateway_loss_ratio", g.LossPercent/100, "gateway", g.Name, "interface", g.Interface)
				m.sample("ostiole_gateway_active", boolValue(g.Active), "gateway", g.Name, "interface", g.Interface)
			}
		}
	}

	if a.services != nil {
		if leases, err := a.services.ReadLeases(); err == nil {
			m.gauge("ostiole_dhcp_leases", "Addresses currently leased.", float64(len(leases)))
		}
		m.gauge("ostiole_service_running", "1 when the service unit is active.",
			boolValue(a.services.Active(r.Context())), "service", "dnsmasq")
	}
	if a.resolver != nil {
		m.gauge("ostiole_service_running", "", boolValue(a.resolver.Active(r.Context())), "service", "unbound")
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
	return nil
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// metricWriter emits the Prometheus text format, keeping each HELP and
// TYPE line to one per metric name however many samples follow.
type metricWriter struct {
	b     *strings.Builder
	named map[string]bool
}

func (m *metricWriter) help(name, help, typ string) {
	if m.named == nil {
		m.named = map[string]bool{}
	}
	if m.named[name] {
		return
	}
	m.named[name] = true
	if help != "" {
		fmt.Fprintf(m.b, "# HELP %s %s\n", name, help)
	}
	fmt.Fprintf(m.b, "# TYPE %s %s\n", name, typ)
}

// gauge writes a single-sample gauge, with its documentation.
func (m *metricWriter) gauge(name, help string, value float64, labels ...string) {
	m.help(name, help, "gauge")
	m.sample(name, value, labels...)
}

func (m *metricWriter) sample(name string, value float64, labels ...string) {
	m.b.WriteString(name)
	if len(labels) > 1 {
		m.b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				m.b.WriteByte(',')
			}
			fmt.Fprintf(m.b, "%s=%q", labels[i], escapeLabel(labels[i+1]))
		}
		m.b.WriteByte('}')
	}
	fmt.Fprintf(m.b, " %g\n", value)
}

// escapeLabel protects a label value that contains a quote, a backslash,
// or a newline, any of which would otherwise break the line.
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}
