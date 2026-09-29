package server

import (
	"fmt"
	"net/http"

	"github.com/rforced/ostiole/internal/dhcplog"
	"github.com/rforced/ostiole/internal/dnslog"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/peerlog"
	"github.com/rforced/ostiole/internal/requestlog"
	"github.com/rforced/ostiole/internal/smart"
	"github.com/rforced/ostiole/internal/traffic"
	"github.com/rforced/ostiole/internal/waflog"
	"github.com/rforced/ostiole/internal/wirelesslog"
)

func (a *api) registerLogFiles(mux *router) {
	mux.HandleFunc("GET /api/v1/system/log-files", a.readNoEngine(a.logFilesStatus))
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
// log held before is written after.
func (a *api) clearLog(name string, empty func()) error {
	if a.logFiles == nil {
		empty()
		return nil
	}
	if err := a.logFiles.Clear(name, empty); err != nil {
		return fmt.Errorf("the log is empty, but its files could not be deleted: %w", err)
	}
	return nil
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
				"Details under System, General.",
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
			Detail: l.Error + ". Details under System, General.",
		})
	}
	return out
}
