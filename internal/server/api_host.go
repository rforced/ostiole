package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/sysupdate"
)

func (a *api) registerHost(mux *router) {
	mux.HandleFunc("GET /api/v1/host", a.readNoEngine(a.hostStatus))
	mux.HandleFunc("POST /api/v1/host/setup", a.admin(a.hostSetup))
	mux.HandleFunc("POST /api/v1/host/takeover", a.admin(a.hostTakeover))
	mux.HandleFunc("POST /api/v1/host/packages/remove", a.admin(a.hostRemovePackages))
	mux.HandleFunc("POST /api/v1/host/legacy/flush", a.admin(a.hostFlushLegacy))
	mux.HandleFunc("POST /api/v1/host/network", a.admin(a.hostNetwork))
	mux.HandleFunc("POST /api/v1/host/steps/{step}", a.admin(a.hostSkipStep))
}

// hostResult is what every action returns: what the command said, and the
// state of the router afterwards, so a page never has to ask twice.
type hostResult struct {
	Output string      `json:"output,omitempty"`
	Status host.Report `json:"status"`
}

// setupTimeout bounds the steps that fetch packages over somebody's
// internet connection. It is longer than any of the package manager's own
// timeouts, because the thing to do with a slow mirror is wait for it.
const setupTimeout = 15 * time.Minute

func (a *api) hostStatus(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, host.Status(r.Context(), a.host))
	return nil
}

// result answers an action with its output and a fresh report.
func (a *api) result(ctx context.Context, w http.ResponseWriter, out string, err error) error {
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, hostResult{Output: out, Status: host.Status(ctx, a.host)})
	return nil
}

// hostSetup installs components and writes their units. It blocks: a
// package manager fetching dnsmasq is not something to report progress
// on, it is something to wait for, and the page says so while it waits.
func (a *api) hostSetup(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Components []string `json:"components"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	if len(body.Components) == 0 {
		return &badRequest{errors.New("name at least one component to set up")}
	}
	// Checked here as well as in the command, because they become argv.
	for _, key := range body.Components {
		if _, ok := sysupdate.ComponentByKey(key); !ok {
			return &badRequest{fmt.Errorf("%q is not a component Ostiole installs", key)}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), setupTimeout)
	defer cancel()
	// Driven through the command line, because writing a unit file is
	// exactly what the daemon's sandbox forbids. The command is told not
	// to restart this daemon: it would cut off the answer, so the restart
	// is scheduled below, once the report has been gathered.
	argv := append([]string{"host", "setup", "--no-restart"}, body.Components...)
	out, err := host.Drive(ctx, a.host, argv...)
	if err != nil {
		return &badRequest{err}
	}
	st := host.Status(r.Context(), a.host)
	if host.RestartsDaemon(body.Components) {
		// The unit was written and its directory created, and the daemon
		// only sees a new directory after a restart. Sessions are on disk,
		// so nobody is signed out by it.
		if err := host.RestartDaemonSoon(r.Context(), a.host); err != nil {
			out += "\n" + err.Error() + "; restart it before applying"
		} else {
			out += "\n" + install.DaemonUnit + " restarts in a moment so it can write the directories this added"
		}
	}
	writeJSON(w, http.StatusOK, hostResult{Output: strings.TrimSpace(out), Status: st})
	return nil
}

// hostTakeover stops, disables and masks every competing firewall.
func (a *api) hostTakeover(w http.ResponseWriter, r *http.Request) error {
	out, err := host.TakeoverFirewall(r.Context(), a.host)
	return a.result(r.Context(), w, out, err)
}

// hostRemovePackages takes a retired competitor off the router. With
// preview nothing is removed and the package manager's own account of
// what would go is returned, which is what the operator agrees to.
func (a *api) hostRemovePackages(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Units   []string `json:"units"`
		Preview bool     `json:"preview"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), setupTimeout)
	defer cancel()
	out, err := host.RemovePackages(ctx, a.host, body.Units, body.Preview)
	return a.result(r.Context(), w, out, err)
}

// hostFlushLegacy clears what an older firewall left in the kernel.
func (a *api) hostFlushLegacy(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Tables []string `json:"tables"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			return err
		}
	}
	out, err := host.FlushLegacy(r.Context(), a.host, body.Tables)
	return a.result(r.Context(), w, out, err)
}

// hostNetwork hands addressing to systemd-networkd, or settles a handover
// that is already waiting. Taking it over arms a revert timer and answers
// as soon as the switch is running in its own unit: the session this
// request arrived on is one of the things that may not survive it.
func (a *api) hostNetwork(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Action string `json:"action"`
		Window string `json:"window,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), setupTimeout)
	defer cancel()
	var out string
	var err error
	switch body.Action {
	case "take":
		window := host.NetworkTakeoverWindow
		if body.Window != "" {
			window, err = time.ParseDuration(body.Window)
			if err != nil {
				return &badRequest{err}
			}
		}
		out, err = host.TakeNetwork(ctx, a.host, window)
	case "confirm":
		out, err = host.ConfirmNetwork(ctx, a.host)
	case "revert":
		out, err = host.RevertNetwork(ctx, a.host)
	default:
		return &badRequest{errors.New(`action must be "take", "confirm" or "revert"`)}
	}
	return a.result(r.Context(), w, out, err)
}

// hostSkipStep records that a step is being left alone, or takes that
// back. A skipped step stops the browser being sent here; it stays on the
// page, with who left it and when, because "we decided not to" is worth
// keeping and "it is fine" is not.
func (a *api) hostSkipStep(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Skip bool `json:"skip"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	by := ""
	if p, ok := a.authenticate(r); ok {
		by = p.Name
	}
	if err := host.SetSkip(a.host.Dir, r.PathValue("step"), by, body.Skip); err != nil {
		return &badRequest{err}
	}
	return a.result(r.Context(), w, "", nil)
}
