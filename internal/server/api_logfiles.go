package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"ostiole/internal/dhcplog"
	"ostiole/internal/dnslog"
	"ostiole/internal/fwlog"
	"ostiole/internal/gateway"
	"ostiole/internal/logfile"
	"ostiole/internal/logging"
	"ostiole/internal/model"
	"ostiole/internal/peerlog"
	"ostiole/internal/requestlog"
	"ostiole/internal/smart"
	"ostiole/internal/traffic"
	"ostiole/internal/waflog"
	"ostiole/internal/wirelesslog"
)

func (a *api) registerLogFiles(mux *router) {
	mux.HandleFunc("GET /api/v1/system/log-files", a.readNoEngine(a.logFilesStatus))
	mux.HandleFunc("DELETE /api/v1/system/logs", a.admin(a.logsClear))
}

// logFilesStatus says whether the logs are written to files and how that
// goes. Whether they are on is the configuration the router runs, which
// the writer takes up a few seconds later.
func (a *api) logFilesStatus(w http.ResponseWriter, _ *http.Request) error {
	st := logfile.Status{Dir: logfile.Dir, Logs: []logfile.LogStatus{}}
	if a.logFiles != nil {
		st = a.logFiles.Status()
	}
	st.Enabled = false
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			st.Enabled = cfg.System.Logging.Files.Enabled
		}
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

// clearLog empties a log and deletes its files together, so nothing the
// log held before is written after. An error is about the files alone:
// the memory is empty either way.
func (a *api) clearLog(name string, empty func()) error {
	if a.logFiles == nil {
		empty()
		return nil
	}
	return a.logFiles.Clear(name, empty)
}

// errFilesStay is what a Clear says when a log is empty but its files are
// still there.
func errFilesStay(err error) error {
	return fmt.Errorf("the log is empty, but its files could not be deleted: %w", err)
}

// clearable is a log a Clear takes: its directory among the log files,
// and what empties it in memory.
type clearable struct {
	name  string
	empty func()
}

// clearables are the logs this daemon keeps that a Clear takes. The
// journal is not among them.
func (a *api) clearables() []clearable {
	var out []clearable
	add := func(name string, empty func()) { out = append(out, clearable{name, empty}) }
	if a.fwlog != nil {
		add(fwlog.FileName, a.fwlog.Clear)
	}
	if a.querylog != nil {
		add(dnslog.FileName, a.querylog.Clear)
	}
	if a.waflog != nil {
		add(waflog.FileName, a.waflog.Clear)
	}
	if a.requests != nil {
		add(requestlog.FileName, a.requests.Clear)
	}
	if a.dhcplog != nil {
		add(dhcplog.FileName, a.dhcplog.Clear)
	}
	if a.wirelesslog != nil {
		add(wirelesslog.FileName, a.wirelesslog.Clear)
	}
	if a.wireguardLog != nil {
		add(peerlog.WireGuard.Name, a.wireguardLog.Clear)
	}
	if a.tailscaleLog != nil {
		add(peerlog.Tailscale.Name, a.tailscaleLog.Clear)
	}
	if a.driveHistory != nil {
		add(smart.HistoryFileName, a.driveHistory.Clear)
	}
	if a.traffic != nil {
		add(traffic.LinksFile, a.traffic.ClearLinks)
		add(traffic.DevicesFile, a.traffic.Clear)
		add(traffic.DestinationsFile, a.traffic.ClearDestinations)
	}
	if a.gatewayHistory != nil {
		add(gateway.HistoryFileName, a.gatewayHistory.ClearHistory)
		add(gateway.EventsFileName, a.gatewayHistory.Events.Clear)
	}
	return out
}

// clearOne empties the log kept under name, and deletes its files.
func (a *api) clearOne(name string) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		for _, c := range a.clearables() {
			if c.name != name {
				continue
			}
			err := a.clearLog(c.name, c.empty)
			a.noteCleared(r, "cleared a log", "log", name)
			if err != nil {
				return errFilesStay(err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
			return nil
		}
		return &unavailable{fmt.Errorf("the %s is not kept by this daemon", logFileNames[name])}
	}
}

// logsClear empties every log a Clear takes and deletes their files. A log
// whose files stay does not stop the rest.
func (a *api) logsClear(w http.ResponseWriter, r *http.Request) error {
	var names []string
	var errs []error
	for _, c := range a.clearables() {
		if err := a.clearLog(c.name, c.empty); err != nil {
			names = append(names, logFileNames[c.name])
			errs = append(errs, err)
		}
	}
	a.noteCleared(r, "cleared every log")
	if len(errs) > 0 {
		return fmt.Errorf("every log is empty, but the files of the %s could not be deleted: %w",
			strings.Join(names, ", "), errors.Join(errs...))
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
	return nil
}

// noteCleared writes a Clear to the journal whatever the level, with who
// asked for it and from where: nothing brings back what it took.
func (a *api) noteCleared(r *http.Request, msg string, args ...any) {
	user := ""
	if p, ok := a.authenticate(r); ok {
		user = p.Name
	}
	slog.InfoContext(logging.Always(r.Context()), msg, append(args, "user", user, "address", remoteIP(r))...)
}

// logFileNames are what a sentence calls each log kept in files.
var logFileNames = map[string]string{
	fwlog.FileName:           "firewall log",
	dnslog.FileName:          "query log",
	waflog.FileName:          "WAF event log",
	requestlog.FileName:      "proxy request log",
	dhcplog.FileName:         "DHCP log",
	wirelesslog.FileName:     "wireless log",
	peerlog.WireGuard.Name:   "WireGuard log",
	peerlog.Tailscale.Name:   "Tailscale log",
	smart.HistoryFileName:    "drive history",
	traffic.LinksFile:        "traffic per link",
	traffic.DevicesFile:      "traffic per device",
	traffic.DestinationsFile: "list of destinations",
	gateway.HistoryFileName:  "gateway history",
	gateway.EventsFileName:   "gateway events",
}

// logFileWarnings are the dashboard's say on the files: writing waits for
// room, or a log cannot be written. Either is found out only after the
// restart that needed the files, unless it is said now.
func (a *api) logFileWarnings(cfg *model.Config) []Warning {
	if a.logFiles == nil || cfg == nil || !cfg.System.Logging.Files.Enabled {
		return nil
	}
	st := a.logFiles.Status()
	var out []Warning
	if st.Paused {
		out = append(out, Warning{
			Kind: "log-files", Level: "warn", Key: "room",
			Title: "Logs are waiting in memory",
			Detail: "The disk holding " + st.Dir + " has less than 5% free, so nothing is written to the log files. " +
				"Details under System › General.",
		})
	}
	for _, l := range st.Logs {
		if l.Error == "" {
			continue
		}
		name := logFileNames[l.Name]
		if name == "" {
			name = l.Name + " log"
		}
		out = append(out, Warning{
			Kind: "log-files", Level: "warn", Key: l.Name,
			Title:  "The " + name + " is not being written to its files",
			Detail: l.Error + ". Details under System › General.",
		})
	}
	return out
}
