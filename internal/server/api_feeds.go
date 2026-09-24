package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/rforced/ostiole/internal/feeds"
)

// FeedRefresher fetches the aliases whose contents come from elsewhere.
type FeedRefresher interface {
	RefreshOne(ctx context.Context, alias string) (int, error)
	Tick(ctx context.Context, force bool)
	// Inspect reads a list no alias names yet.
	Inspect(ctx context.Context, url string) (feeds.Part, error)
}

func (a *api) registerFeeds(mux *router) {
	mux.HandleFunc("GET /api/v1/aliases/feeds", a.read(a.feedStatus))
	mux.HandleFunc("POST /api/v1/aliases/feeds/refresh", a.write(a.refreshFeeds))
	mux.HandleFunc("POST /api/v1/aliases/inspect", a.write(a.inspectFeed))
	mux.HandleFunc("POST /api/v1/aliases/{name}/refresh", a.write(a.refreshFeed))
}

// feedStatus reports when each fetched alias was last updated, how many
// entries it holds, and why the last attempt failed if it did.
func (a *api) feedStatus(w http.ResponseWriter, _ *http.Request) error {
	out := []feeds.Status{}
	if a.feedCache == nil {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	cfg := a.engine.Effective()
	if cfg == nil {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	writeJSON(w, http.StatusOK, a.feedCache.Statuses(cfg))
	return nil
}

func (a *api) refreshFeeds(w http.ResponseWriter, r *http.Request) error {
	if a.feeds == nil {
		return &unavailable{errors.New("nothing is refreshing aliases on this router")}
	}
	a.feeds.Tick(r.Context(), true)
	return a.feedStatus(w, r)
}

func (a *api) refreshFeed(w http.ResponseWriter, r *http.Request) error {
	if a.feeds == nil {
		return &unavailable{errors.New("nothing is refreshing aliases on this router")}
	}
	name := r.PathValue("name")
	count, err := a.feeds.RefreshOne(r.Context(), name)
	if err != nil {
		return &badRequest{fmt.Errorf("refresh %s: %w", name, err)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alias": name, "entries": count})
	return nil
}

// inspectFeed reads a list before an alias names it, so the alias dialog
// can offer what the list can be narrowed by as soon as a URL is typed. It
// fetches the one URL it is given, the way a refresh would.
func (a *api) inspectFeed(w http.ResponseWriter, r *http.Request) error {
	if a.feeds == nil {
		return &unavailable{errors.New("nothing is refreshing aliases on this router")}
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if u, err := url.Parse(req.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return &badRequest{fmt.Errorf("%q must be an http or https URL", req.URL)}
	}
	part, err := a.feeds.Inspect(r.Context(), req.URL)
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, part)
	return nil
}
