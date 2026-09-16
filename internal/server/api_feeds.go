package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/rforced/ostiole/internal/feeds"
)

// FeedRefresher fetches the aliases whose contents come from elsewhere.
type FeedRefresher interface {
	RefreshOne(ctx context.Context, alias string) (int, error)
	Tick(ctx context.Context, force bool)
}

func (a *api) registerFeeds(mux *router) {
	mux.HandleFunc("GET /api/v1/aliases/feeds", a.read(a.feedStatus))
	mux.HandleFunc("POST /api/v1/aliases/feeds/refresh", a.write(a.refreshFeeds))
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
		return &unavailable{errors.New("nothing is refreshing aliases on this box")}
	}
	a.feeds.Tick(r.Context(), true)
	return a.feedStatus(w, r)
}

func (a *api) refreshFeed(w http.ResponseWriter, r *http.Request) error {
	if a.feeds == nil {
		return &unavailable{errors.New("nothing is refreshing aliases on this box")}
	}
	name := r.PathValue("name")
	count, err := a.feeds.RefreshOne(r.Context(), name)
	if err != nil {
		return &badRequest{fmt.Errorf("refresh %s: %w", name, err)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alias": name, "entries": count})
	return nil
}
