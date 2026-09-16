package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
)

// BlocklistRefresher fetches the DNS blocklists and installs the result.
type BlocklistRefresher interface {
	RefreshOne(ctx context.Context, name string) (int, error)
	Store(ctx context.Context, name string, domains []string, skipped int, format model.ListFormat) (int, error)
	Tick(ctx context.Context, force bool)
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
	Enabled bool `json:"enabled"`
	// Active is whether names are really being refused: blocking is on and
	// so is the DNS server that would do it.
	Active bool               `json:"active"`
	Mode   model.BlockMode    `json:"mode"`
	Lists  []dnsblock.Status  `json:"lists"`
	Totals blockingTotals     `json:"totals"`
	Limits blockingLimitsInfo `json:"limits"`
}

type blockingTotals struct {
	// Domains is what the enabled lists come to before merging. The number
	// actually written is smaller, because lists overlap heavily.
	Domains int `json:"domains"`
	Lists   int `json:"lists"`
	Allow   int `json:"allow"`
	Deny    int `json:"deny"`
}

type blockingLimitsInfo struct {
	MaxDomains int `json:"maxDomains"`
}

func (a *api) blockingStatus(w http.ResponseWriter, _ *http.Request) error {
	st := blockingState{Lists: []dnsblock.Status{}, Limits: blockingLimitsInfo{MaxDomains: dnsblock.DefaultMaxDomains}}
	cfg := a.engine.Effective()
	if cfg == nil {
		writeJSON(w, http.StatusOK, st)
		return nil
	}
	st.Enabled = cfg.Blocking.Enabled
	st.Active = cfg.BlockingActive()
	st.Mode = cfg.Blocking.BlockMode()
	st.Totals = blockingTotals{
		Lists: len(cfg.Blocking.EnabledLists()),
		Allow: len(cfg.Blocking.Allow),
		Deny:  len(cfg.Blocking.Deny),
	}
	if a.blockCache != nil {
		st.Lists = a.blockCache.Statuses(cfg)
		st.Totals.Domains = a.blockCache.Total(cfg)
	}
	writeJSON(w, http.StatusOK, st)
	return nil
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

func (a *api) refreshBlocklists(w http.ResponseWriter, r *http.Request) error {
	if a.blocklists == nil {
		return &unavailable{errors.New("nothing is refreshing blocklists on this box")}
	}
	a.blocklists.Tick(r.Context(), true)
	return a.blockingStatus(w, r)
}

func (a *api) refreshBlocklist(w http.ResponseWriter, r *http.Request) error {
	if a.blocklists == nil {
		return &unavailable{errors.New("nothing is refreshing blocklists on this box")}
	}
	name := r.PathValue("name")
	count, err := a.blocklists.RefreshOne(r.Context(), name)
	if err != nil {
		return &badRequest{fmt.Errorf("refresh %s: %w", name, err)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": name, "domains": count})
	return nil
}

// importBlocklist takes a list as the request body, which is how a box with
// no way out to the internet gets one, and how a hand-written list is kept.
func (a *api) importBlocklist(w http.ResponseWriter, r *http.Request) error {
	if a.blocklists == nil {
		return &unavailable{errors.New("nothing is holding blocklists on this box")}
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
