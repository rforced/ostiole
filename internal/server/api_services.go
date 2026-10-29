package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/install"
	"ostiole/internal/model"
	"ostiole/internal/services"
	"ostiole/internal/store"
)

// Actions on System › Services.
const (
	unitRestart = "restart"
	unitStart   = "start"
)

// selfRestartDelay lets the reply to a restart of Ostiole itself leave
// before systemd stops the process that is sending it.
const selfRestartDelay = 500 * time.Millisecond

// ServiceUnit is a row of System › Services: the dashboard's account of a
// unit, what systemd says about its running, and what may be done to it.
type ServiceUnit struct {
	Name   string `json:"name"`
	Unit   string `json:"unit"`
	State  string `json:"state"`
	Want   bool   `json:"want"`
	Detail string `json:"detail,omitempty"`
	// Since is when it last started, while it runs.
	Since *time.Time `json:"since,omitempty"`
	// Restarts counts systemd's own restarts since it was last started
	// by hand.
	Restarts int `json:"restarts"`
	// Failure says how it last stopped, while it is down after a failure.
	Failure string `json:"failure,omitempty"`
	// Effect is what a restart interrupts, for the confirm.
	Effect  string   `json:"effect,omitempty"`
	Actions []string `json:"actions"`
}

// ServicesReply lists the units and says whether this daemon can restart
// them at all.
type ServicesReply struct {
	Units   []ServiceUnit `json:"units"`
	Control bool          `json:"control"`
}

type serviceActionReply struct {
	Unit string `json:"unit"`
	// Restarting is set for Ostiole itself, which restarts after this
	// reply.
	Restarting bool   `json:"restarting,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (a *api) registerServices(mux *router) {
	mux.HandleFunc("GET /api/v1/system/services", a.read(a.serviceList))
	mux.HandleFunc("POST /api/v1/system/services/{unit}/restart", a.write(a.serviceAction(unitRestart)))
	mux.HandleFunc("POST /api/v1/system/services/{unit}/start", a.write(a.serviceAction(unitStart)))
}

func (a *api) serviceList(w http.ResponseWriter, r *http.Request) error {
	units, err := a.serviceUnits(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, ServicesReply{Units: units, Control: a.control != nil})
	return nil
}

// serviceUnits is the dashboard's list without the firewall log, which is
// no unit, with Ostiole itself first and the PPPoE sessions, which the
// dashboard has no tile for, before the network.
func (a *api) serviceUnits(ctx context.Context) ([]ServiceUnit, error) {
	cfg, err := a.engine.Store().Load()
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	units := a.readUnits(ctx, cfg)
	out := []ServiceUnit{{Name: "Ostiole", Unit: install.DaemonUnit, State: stateActive, Want: true}}
	for _, s := range a.serviceStates(ctx, cfg, units) {
		switch s.Unit {
		case "":
			continue
		case install.NetworkdUnit:
			out = append(out, pppoeUnits(cfg, units)...)
		}
		out = append(out, ServiceUnit{Name: s.Name, Unit: s.Unit, State: s.State, Want: s.Want, Detail: s.Detail})
	}
	a.unitDetails(ctx, out)
	for i := range out {
		u := &out[i]
		u.Effect = restartEffect(u.Unit)
		u.Actions = a.unitActions(*u)
	}
	return out, nil
}

// pppoeUnits is a row per dialled session.
func pppoeUnits(cfg *model.Config, units install.UnitStates) []ServiceUnit {
	var out []ServiceUnit
	for _, n := range pppoeNames(cfg) {
		in, _ := cfg.Interface(n)
		s := ServiceState{Name: "PPPoE " + n, Unit: services.PPPoEUnitFor(n), Want: in.Enabled}
		fillTile(&s, units, services.PPPoEUnit, "PPPoE is not set up on this router. Run `ostiole repair` as root.")
		out = append(out, ServiceUnit{Name: s.Name, Unit: s.Unit, State: s.State, Want: s.Want, Detail: s.Detail})
	}
	return out
}

// pppoeNames lists the interfaces that dial a session.
func pppoeNames(cfg *model.Config) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for _, in := range cfg.Interfaces {
		if in.PPPoE != nil {
			out = append(out, in.Name)
		}
	}
	return out
}

// unitDetails asks systemd when each known unit started, how often it
// restarted itself and how it last stopped, in one call. systemctl show
// answers in the order asked, under the name an alias resolves to.
func (a *api) unitDetails(ctx context.Context, units []ServiceUnit) {
	if a.units == nil {
		return
	}
	var asked []*ServiceUnit
	for i := range units {
		if units[i].State == stateActive || units[i].State == stateInactive {
			asked = append(asked, &units[i])
		}
	}
	if len(asked) == 0 {
		return
	}
	args := []string{"show", "--timestamp=unix", "-p", "ActiveEnterTimestamp,NRestarts,Result", "--"}
	for _, u := range asked {
		args = append(args, u.Unit)
	}
	out, err := a.units.Run(ctx, args...)
	if err != nil {
		return
	}
	blocks := strings.Split(strings.TrimSpace(out), "\n\n")
	if len(blocks) != len(asked) {
		return
	}
	for i, b := range blocks {
		u := asked[i]
		for line := range strings.SplitSeq(b, "\n") {
			key, value, _ := strings.Cut(line, "=")
			switch key {
			case "ActiveEnterTimestamp":
				if sec, err := strconv.ParseInt(strings.TrimPrefix(value, "@"), 10, 64); err == nil && u.State == stateActive {
					t := time.Unix(sec, 0).UTC()
					u.Since = &t
				}
			case "NRestarts":
				u.Restarts, _ = strconv.Atoi(value)
			case "Result":
				// A unit running again has restarted past its failure; the
				// count says how often.
				if u.State != stateActive {
					u.Failure = failureWords(value)
				}
			}
		}
	}
}

// failureWords says how a unit stopped, for a Result that is a failure.
func failureWords(result string) string {
	switch result {
	case "", "success", "condition", "exec-condition":
		return ""
	case "exit-code":
		return "Exited with an error."
	case "signal":
		return "Was killed."
	case "core-dump":
		return "Crashed."
	case "timeout":
		return "Timed out."
	case "watchdog":
		return "Stopped answering its watchdog."
	case "start-limit-hit":
		return "Failed too often to be started again."
	case "oom-kill":
		return "Ran out of memory."
	case "resources":
		return "Could not get what it needs to start."
	}
	return "Stopped: " + result + "."
}

// restartEffect is what a restart of the unit interrupts.
func restartEffect(unit string) string {
	switch {
	case unit == install.DaemonUnit:
		return "The page reconnects once it is back. Logs kept only in memory start over."
	case unit == services.Unit:
		return "DNS stops answering for a few seconds. Leases are kept."
	case unit == services.UnboundUnit:
		return "Lookups wait for a few seconds, and its cache empties."
	case unit == services.UPnPUnit:
		return "Devices lose their port mappings until they ask again."
	case unit == services.TailscaleUnit:
		return "The tailnet drops for a few seconds."
	case unit == services.ProxyUnit:
		return "Its sites drop open connections and are back in a second or two."
	case unit == services.NTPUnit:
		return "The clock keeps running. LAN clients asking for the time wait a few seconds."
	case instanceOf(unit, services.OpenVPNUnit):
		return "The tunnel drops and connects again."
	case instanceOf(unit, services.WirelessUnit):
		return "Clients on this radio disconnect and join again."
	case instanceOf(unit, services.PPPoEUnit):
		return "Internet access over this line drops until it dials again, often for several seconds, and its address may change. Every connection through it breaks, this page too if you reach it over this line."
	}
	return ""
}

// instanceOf reports whether unit is an instance of a template such as
// ostiole-hostapd@.service.
func instanceOf(unit, template string) bool {
	prefix, suffix, ok := strings.Cut(template, "@")
	return ok && strings.HasPrefix(unit, prefix+"@") && strings.HasSuffix(unit, strings.TrimPrefix(suffix, "."))
}

// unitActions is what may be done to a unit now: a restart while it runs,
// a start while it is down and the configuration wants it. Nothing stops
// one, as the configuration decides what runs, and the network's own
// service is left alone, since a restart can drop the router's addresses.
func (a *api) unitActions(u ServiceUnit) []string {
	if a.control == nil || restartEffect(u.Unit) == "" {
		return []string{}
	}
	switch {
	case u.State == stateActive:
		return []string{unitRestart}
	case u.State == stateInactive && u.Want:
		return []string{unitStart}
	}
	return []string{}
}

// unitRefusal says why an action is not offered on a unit.
func unitRefusal(u ServiceUnit, action string) error {
	switch {
	case restartEffect(u.Unit) == "":
		return fmt.Errorf("%s cannot be %sed from here", u.Name, action)
	case action == unitStart && u.State == stateActive:
		return fmt.Errorf("%s is already running", u.Name)
	case action == unitStart && !u.Want:
		return fmt.Errorf("the configuration leaves %s off", u.Name)
	case action == unitRestart && u.State == stateInactive:
		return fmt.Errorf("%s is not running, so start it instead", u.Name)
	}
	return fmt.Errorf("%s is %s, so it cannot be %sed", u.Name, u.State, action)
}

func (a *api) serviceAction(action string) func(w http.ResponseWriter, r *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		if a.control == nil {
			return &unavailable{errors.New("this router cannot restart services from here")}
		}
		name := r.PathValue("unit")
		units, err := a.serviceUnits(r.Context())
		if err != nil {
			return err
		}
		i := slices.IndexFunc(units, func(u ServiceUnit) bool { return u.Unit == name })
		if i < 0 {
			return &notFound{fmt.Errorf("%q is not a service this router runs", name)}
		}
		u := units[i]
		if !slices.Contains(u.Actions, action) {
			return &badRequest{unitRefusal(u, action)}
		}
		auditAction := audit.ServiceRestart
		if action == unitStart {
			auditAction = audit.ServiceStart
		}
		// A blocking restart of this daemon from inside it would die with
		// the process it stops, and the reply with it.
		if u.Unit == install.DaemonUnit {
			a.record(r, auditAction, u.Name, "")
			writeJSON(w, http.StatusAccepted, serviceActionReply{Unit: u.Unit, Restarting: true})
			go a.restartSelf(context.WithoutCancel(r.Context()))
			return nil
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		out, err := a.control.Run(ctx, "--no-ask-password", action, u.Unit)
		if err != nil {
			a.record(r, auditAction, u.Name, "failed")
			writeJSON(w, http.StatusOK, serviceActionReply{Unit: u.Unit, Error: firstLine(out, err)})
			return nil
		}
		a.record(r, auditAction, u.Name, "")
		writeJSON(w, http.StatusOK, serviceActionReply{Unit: u.Unit})
		return nil
	}
}

// restartSelf restarts this daemon once the reply has gone out.
func (a *api) restartSelf(ctx context.Context) {
	time.Sleep(selfRestartDelay)
	if _, err := a.control.Run(ctx, "--no-ask-password", "--no-block", unitRestart, install.DaemonUnit); err != nil {
		slog.Warn("could not restart Ostiole", "err", err)
	}
}

// firstLine is what systemctl said about a failure, its first line, which
// names the unit and the reason; the rest points at commands to run.
func firstLine(out string, err error) string {
	if line, _, _ := strings.Cut(strings.TrimSpace(out), "\n"); line != "" {
		return line
	}
	return err.Error()
}
