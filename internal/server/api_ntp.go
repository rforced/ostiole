package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/chrony"
	"github.com/rforced/ostiole/internal/model"
)

// Reading the clock is a viewer's business, like every other status.
func (a *api) registerNTP(mux *router) {
	mux.HandleFunc("GET /api/v1/ntp/status", a.readNoEngine(a.ntpStatus))
}

// ntpStatus is what the time service is doing.
type ntpStatus struct {
	// SetUp says the unit exists, which `ostiole repair` writes. Until it
	// does, the distribution keeps the clock.
	SetUp   bool `json:"setUp"`
	Running bool `json:"running"`
	// HostClock says the router cannot set its own clock, as in a
	// container, and the host keeps it.
	HostClock bool   `json:"hostClock,omitempty"`
	Version   string `json:"version,omitempty"`
	// Read says chronyd answered, so what follows is its account.
	Read bool `json:"read"`
	// Synchronised says a source still sets the clock.
	Synchronised bool `json:"synchronised"`
	// Reference is the source the clock follows, by the name it was
	// configured with.
	Reference string `json:"reference,omitempty"`
	Stratum   int    `json:"stratum,omitempty"`
	// OffsetSeconds is how far the clock is from true time. Positive is
	// ahead.
	OffsetSeconds float64 `json:"offsetSeconds"`
	// LastUpdate is when the clock was last set from a source.
	LastUpdate *time.Time  `json:"lastUpdate,omitempty"`
	Sources    []ntpSource `json:"sources"`
	// Served is what the LAN asked since the service started; absent where
	// the build cannot say.
	Served *ntpServed `json:"served,omitempty"`
	// Defaults are the servers asked when the configuration names none,
	// so the page shows the list the router would use.
	Defaults []model.NTPServer `json:"defaults"`
}

type ntpSource struct {
	// Name is the one configured: every server of a pool carries the
	// pool's name.
	Name    string `json:"name"`
	Address string `json:"address"`
	// State is selected, combined, excluded, unusable, falseticker or
	// jittery, or for a source that answers and goes unused, why, where
	// chronyd says: untrusted, waiting, sampling, unsynchronised, distant
	// or noselect.
	State       string `json:"state"`
	Stratum     int    `json:"stratum"`
	PollSeconds int64  `json:"pollSeconds"`
	// Reach has a bit for each of the last eight polls answered.
	Reach int `json:"reach"`
	// LastSeconds is how long ago it last answered; -1 for never.
	LastSeconds   int64   `json:"lastSeconds"`
	OffsetSeconds float64 `json:"offsetSeconds"`
	ErrorSeconds  float64 `json:"errorSeconds"`
	// NTS is signed or failing for a source asked to sign its answers,
	// and empty for one that is not, or where the build cannot say.
	NTS string `json:"nts,omitempty"`
}

type ntpServed struct {
	Packets int64 `json:"packets"`
	Dropped int64 `json:"dropped"`
}

func (a *api) ntpStatus(w http.ResponseWriter, r *http.Request) error {
	st := ntpStatus{Sources: []ntpSource{}, Defaults: model.DefaultNTPServers}
	if a.ntp != nil {
		ctx := r.Context()
		st.SetUp = a.ntp.Installed(ctx)
		st.Running = st.SetUp && a.ntp.Active(ctx)
		st.HostClock = st.SetUp && !st.Running && a.ntp.Skipped(ctx)
		st.Version = a.ntp.Version().String()
	}
	// Only a running unit is asked: with no unit of ours, the daemon on
	// the command port is the distribution's.
	if st.Running && a.chrony != nil {
		ctx, cancel := contextWithTimeout(r, 5*time.Second)
		defer cancel()
		a.readChrony(ctx, &st)
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

// readChrony fills in chronyd's account. A command a build will not
// answer leaves its part out rather than failing the rest.
func (a *api) readChrony(ctx context.Context, st *ntpStatus) {
	tr, err := a.chrony.Tracking(ctx)
	if err != nil {
		return
	}
	st.Read = true
	st.Synchronised = following(tr, time.Now())
	st.Stratum = tr.Stratum
	st.OffsetSeconds = tr.Offset
	// On its own clock chronyd gives the time of asking, not of a source.
	if !tr.RefTime.IsZero() && !tr.Local {
		t := tr.RefTime
		st.LastUpdate = &t
	}
	sources, err := a.chrony.Sources(ctx)
	if err != nil {
		return
	}
	signed := map[string]string{}
	if auth, err := a.chrony.Auth(ctx); err == nil {
		for _, au := range auth {
			if au.Mode == "NTS" {
				signed[au.Address] = ntsState(au)
			}
		}
	}
	why := map[string]string{}
	if selection, err := a.chrony.Selection(ctx); err == nil {
		for _, s := range selection {
			why[s.Address] = s.State
		}
	}
	for _, s := range sources {
		if st.Synchronised && s.Address == tr.Address && tr.Address != "" {
			st.Reference = s.Name
		}
		last := int64(-1)
		if s.LastRx >= 0 {
			last = int64(s.LastRx / time.Second)
		}
		st.Sources = append(st.Sources, ntpSource{
			Name: s.Name, Address: s.Address, State: sourceState(s, why[s.Address]), Stratum: s.Stratum,
			PollSeconds: int64(s.Poll / time.Second), Reach: int(s.Reach), LastSeconds: last,
			OffsetSeconds: s.Offset, ErrorSeconds: s.Error, NTS: signed[s.Address],
		})
	}
	if served, err := a.chrony.ServerStats(ctx); err == nil {
		st.Served = &ntpServed{Packets: served.NTPReceived, Dropped: served.NTPDropped}
	}
}

// sourceState is what a source is doing: chronyd's state for it, unless
// it answers and goes unused and chronyd says why. One that stopped
// answering is just that, whatever became of its earlier answers.
func sourceState(s chrony.Source, why string) string {
	switch {
	case s.State == "selected" || s.State == "combined":
		return s.State
	case s.Reach == 0:
		return "unusable"
	case why == "":
		return s.State
	}
	return why
}

// ntsState says whether a source's signed answers are working: it holds
// cookies, the last one was not refused, and the key exchange is not
// being retried.
func ntsState(au chrony.Auth) string {
	if au.Cookies > 0 && !au.NAK && au.Attempts <= 1 {
		return "signed"
	}
	return "failing"
}

// lostAfter is how long the clock can go uncorrected before its sources
// count as lost. chronyd asks a source at least every 1024 s and gives up
// on one after eight polls unanswered, so while any source answers the
// last correction is never this old.
const lostAfter = 3 * time.Hour

// following says whether a source still sets the clock. chronyd's word is
// not enough: it goes on calling the clock synchronised after every
// source stops answering.
func following(tr chrony.Tracking, now time.Time) bool {
	return tr.Synchronised() && now.Sub(tr.RefTime) < lostAfter
}

// clockWarning speaks up when the time service has run for a while and
// no server sets the clock: certificates, DNSSEC and schedules all go
// wrong on a clock that drifts. It says so too, more quietly, when only
// unsigned servers set it while signed ones are listed. A service that
// just started is given ten minutes, which is far longer than a first
// sync takes. running is whether systemd said the service runs.
func (a *api) clockWarning(ctx context.Context, running bool) (Warning, bool) {
	if a.ntp == nil || a.chrony == nil || !running {
		return Warning{}, false
	}
	since := a.ntp.ActiveSince(ctx)
	if since.IsZero() || time.Since(since) < 10*time.Minute {
		return Warning{}, false
	}
	tr, err := a.chrony.Tracking(ctx)
	if err != nil {
		return Warning{}, false
	}
	// A build that cannot open authdata cannot tell signed servers apart.
	auth, err := a.chrony.Auth(ctx)
	if err != nil {
		auth = nil
	}
	sources, err := a.chrony.Sources(ctx)
	if err != nil {
		sources = nil
	}
	if following(tr, time.Now()) {
		return unsignedWarning(auth, sources)
	}
	detail := "No time server answers. Check that the router reaches the internet and that udp/123 is not blocked on the way out."
	switch failing := failingNTS(auth); {
	case failing != "":
		detail = "No server gave a signed answer, " + failing + ". Where tcp/4460 or large UDP packets are blocked on the way out NTS cannot work; list servers without it on the NTP page."
	case slices.ContainsFunc(sources, func(s chrony.Source) bool { return s.Reach != 0 }):
		detail = "Servers answer, but not enough of them agree to set the clock. The NTP page says what each one is doing."
	}
	if tr.Local {
		detail += " Meanwhile the router answers the LAN from its own clock."
	}
	return Warning{
		Kind: "clock-unsynchronised", Level: "warn",
		Title:  "The clock is not synchronised",
		Detail: detail,
	}, true
}

// unsignedWarning says when the clock follows unsigned servers alone
// while signed ones are listed. The time is right, so it is news rather
// than a fault, and no notice is sent for it.
func unsignedWarning(auth []chrony.Auth, sources []chrony.Source) (Warning, bool) {
	nts := map[string]bool{}
	for _, au := range auth {
		if au.Mode == "NTS" {
			nts[au.Address] = true
		}
	}
	if len(nts) == 0 || len(sources) == 0 {
		return Warning{}, false
	}
	for _, s := range sources {
		// A signed server chronyd can select bounds the unsigned ones,
		// whether or not the clock follows it.
		if nts[s.Address] && (s.State == "selected" || s.State == "combined" || s.State == "excluded") {
			return Warning{}, false
		}
	}
	detail := "No signed server can be used, so the unsigned ones set the clock until one can. The NTP page says what each one is doing."
	if failing := failingNTS(auth); failing != "" {
		detail = "No server gave a signed answer, " + failing + ", so the unsigned ones set the clock. Where tcp/4460 or large UDP packets are blocked on the way out NTS cannot work."
	}
	return Warning{
		Kind: "clock-unsigned", Level: "info",
		Title:  "The clock follows unsigned servers",
		Detail: detail,
	}, true
}

// failingNTS names the sources whose signed answers are failing, when
// every source asked to sign is failing.
func failingNTS(auth []chrony.Auth) string {
	var failing []string
	for _, au := range auth {
		if au.Mode != "NTS" {
			continue
		}
		if ntsState(au) == "signed" {
			return ""
		}
		failing = append(failing, au.Name)
	}
	if len(failing) == 0 {
		return ""
	}
	return "from " + strings.Join(failing, ", ")
}
