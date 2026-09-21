package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/dnslog"
	"github.com/rforced/ostiole/internal/model"
)

// BlocklistRefresher fetches the DNS blocklists and installs the result.
type BlocklistRefresher interface {
	RefreshOne(ctx context.Context, name string) (int, error)
	Store(ctx context.Context, name string, domains []string, skipped int, format model.ListFormat) (int, error)
	Tick(ctx context.Context, force bool) dnsblock.Report
}

func (a *api) registerBlocking(mux *router) {
	mux.HandleFunc("GET /api/v1/blocking", a.read(a.blockingStatus))
	mux.HandleFunc("GET /api/v1/blocking/catalog", a.read(a.blockingCatalog))
	mux.HandleFunc("GET /api/v1/blocking/lookup", a.read(a.blockingLookup))
	mux.HandleFunc("POST /api/v1/blocking/refresh", a.write(a.refreshBlocklists))
	mux.HandleFunc("POST /api/v1/blocking/lists/{name}/refresh", a.write(a.refreshBlocklist))
	mux.HandleFunc("POST /api/v1/blocking/lists/{name}/import", a.write(a.importBlocklist))
}

// blockingState is what the UI shows above the lists.
type blockingState struct {
	// Enabled is whether the subscribed lists are switched on.
	Enabled bool `json:"enabled"`
	// Active is whether their names are really being refused: the lists are
	// on and so is the DNS server that would do it.
	Active bool               `json:"active"`
	Mode   model.BlockMode    `json:"mode"`
	Lists  []dnsblock.Status  `json:"lists"`
	Totals blockingTotals     `json:"totals"`
	Limits blockingLimitsInfo `json:"limits"`
	// Report is filled in by a refresh, so the caller can be told what the
	// pass did rather than being left to guess from the table.
	Report *dnsblock.Report `json:"report,omitempty"`
	// Counts is how much each list has actually refused, present only
	// while the query log is on: nothing else on this router counts.
	Counts      map[string]dnslog.ListCount `json:"counts,omitempty"`
	CountsSince *time.Time                  `json:"countsSince,omitempty"`
}

type blockingTotals struct {
	// Domains is what the enabled lists come to added together, before
	// merging. Overlapping lists merge down a long way; lists curated not
	// to overlap, like HaGeZi's, barely move.
	Domains int `json:"domains"`
	// Blocked is what the last installed merge actually came to, which is
	// what costs memory and what the ceiling applies to. It is zero until
	// a merge has been installed.
	Blocked int `json:"blocked"`
	// MergedAt is when that merge happened.
	MergedAt *time.Time `json:"mergedAt,omitempty"`
	// EstimatedMemoryMB is roughly what dnsmasq holds for Blocked names.
	EstimatedMemoryMB int `json:"estimatedMemoryMb"`
	Lists             int `json:"lists"`
	Allow             int `json:"allow"`
	Deny              int `json:"deny"`
}

type blockingLimitsInfo struct {
	// MaxDomains is the ceiling in force: what the configuration asks for,
	// or the default.
	MaxDomains int `json:"maxDomains"`
	// HardMax is the most it can be raised to.
	HardMax int `json:"hardMax"`
	// DefaultMax is what it is when the configuration names none.
	DefaultMax int `json:"defaultMax"`
	// BytesPerName is what one blocked name costs dnsmasq, so the UI can
	// price a change without asking the server.
	BytesPerName int `json:"bytesPerName"`
}

func (a *api) blockingStatus(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, a.blockingState())
	return nil
}

func (a *api) blockingState() blockingState {
	st := blockingState{
		Lists: []dnsblock.Status{},
		Limits: blockingLimitsInfo{
			MaxDomains:   dnsblock.DefaultMaxDomains,
			HardMax:      dnsblock.MaxDomains,
			DefaultMax:   dnsblock.DefaultMaxDomains,
			BytesPerName: dnsblock.BytesPerName,
		},
	}
	cfg := a.engine.Effective()
	if cfg == nil {
		return st
	}
	st.Enabled = cfg.Blocking.Enabled
	st.Active = cfg.ListsActive()
	st.Mode = cfg.Blocking.BlockMode()
	st.Totals = blockingTotals{
		Lists: len(cfg.Blocking.EnabledLists()),
		Allow: len(cfg.Blocking.Allow),
		Deny:  len(cfg.Blocking.Deny),
	}
	if cfg.Blocking.MaxDomains > 0 {
		st.Limits.MaxDomains = cfg.Blocking.MaxDomains
	}
	if a.blockCache != nil {
		st.Lists = a.blockCache.Statuses(cfg)
		st.Totals.Domains = a.blockCache.Total(cfg)
		if merged, ok := a.blockCache.Merged(); ok {
			st.Totals.Blocked = merged.Domains
			st.Totals.EstimatedMemoryMB = int(merged.EstimatedBytes() / (1 << 20))
			if !merged.At.IsZero() {
				at := merged.At
				st.Totals.MergedAt = &at
			}
		}
	}
	if a.querylog != nil && a.querylog.Enabled() {
		counts, since := a.querylog.ListCounts()
		st.Counts = counts
		if !since.IsZero() {
			st.CountsSince = &since
		}
	}
	return st
}

func (a *api) blockingCatalog(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, dnsblock.Catalog())
	return nil
}

// blockingLookup answers "why is this name blocked?", which is the question
// every support conversation about DNS blocking starts with.
func (a *api) blockingLookup(w http.ResponseWriter, r *http.Request) error {
	name := r.URL.Query().Get("name")
	if name == "" {
		return &badRequest{errors.New("name is required")}
	}
	cfg := a.engine.Effective()
	if cfg == nil || a.blockCache == nil {
		return &unavailable{errors.New("nothing is configured yet")}
	}
	writeJSON(w, http.StatusOK, dnsblock.Lookup(dnsblock.OptionsFor(cfg), a.blockCache, name))
	return nil
}

// refreshContext detaches the work from the browser's request. Fetching
// several published lists takes minutes, and a tab closed halfway through
// should not leave the router with half a blocklist.
func refreshContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Minute)
}

func (a *api) refreshBlocklists(w http.ResponseWriter, r *http.Request) error {
	if a.blocklists == nil {
		return &unavailable{errors.New("nothing is refreshing blocklists on this router")}
	}
	ctx, cancel := refreshContext(r)
	defer cancel()
	report := a.blocklists.Tick(ctx, true)
	st := a.blockingState()
	st.Report = &report
	writeJSON(w, http.StatusOK, st)
	return nil
}

func (a *api) refreshBlocklist(w http.ResponseWriter, r *http.Request) error {
	if a.blocklists == nil {
		return &unavailable{errors.New("nothing is refreshing blocklists on this router")}
	}
	ctx, cancel := refreshContext(r)
	defer cancel()
	name := r.PathValue("name")
	count, err := a.blocklists.RefreshOne(ctx, name)
	if err != nil {
		return &badRequest{fmt.Errorf("refresh %s: %w", name, err)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": name, "domains": count})
	return nil
}

// importBlocklist takes a list as the request body, which is how a router with
// no way out to the internet gets one, and how a hand-written list is kept.
func (a *api) importBlocklist(w http.ResponseWriter, r *http.Request) error {
	if a.blocklists == nil {
		return &unavailable{errors.New("nothing is holding blocklists on this router")}
	}
	name := r.PathValue("name")
	cfg := a.engine.Effective()
	if cfg == nil {
		return &unavailable{errors.New("nothing is configured yet")}
	}
	l, ok := cfg.Blocking.List(name)
	if !ok {
		return &badRequest{fmt.Errorf("there is no list called %q", name)}
	}
	defer r.Body.Close()
	domains, skipped, format, err := dnsblock.ParseStream(io.LimitReader(r.Body, dnsblock.MaxBytes+1), l.FormatOrAuto())
	if err != nil {
		return &badRequest{fmt.Errorf("read the list: %w", err)}
	}
	count, err := a.blocklists.Store(r.Context(), name, domains, skipped, format)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": name, "domains": count, "format": format, "skipped": skipped})
	return nil
}
