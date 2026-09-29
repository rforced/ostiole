package dnsblock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// A list is fetched when it has never been, and again once its period
// has passed, not on every tick in between: the tick is frequent, the
// fetch is what costs the publisher.
func TestDueFollowsTheFetchTime(t *testing.T) {
	t.Parallel()
	r := &Refresher{Cache: NewCache(t.TempDir())}
	l := model.BlockList{Name: "ads", URL: "https://example.com/ads", RefreshHours: 6}
	if !r.due(l) {
		t.Error("a list never fetched is not due")
	}
	save := func(at time.Time) {
		t.Helper()
		if err := r.Cache.Save(Meta{Name: "ads", URL: l.URL, FetchedAt: at}, []string{"ads.example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	save(time.Now())
	if r.due(l) {
		t.Error("a list fetched just now is due")
	}
	save(time.Now().Add(-5 * time.Hour))
	if r.due(l) {
		t.Error("a list an hour short of its period is due")
	}
	save(time.Now().Add(-7 * time.Hour))
	if !r.due(l) {
		t.Error("a list past its period is not due")
	}
}

// A publisher that is down keeps yesterday's names in force: the tick
// reports the failure, the cache keeps what it had, and the loader is
// not handed an emptier list.
func TestAFailedFetchKeepsYesterdaysList(t *testing.T) {
	t.Parallel()
	var down bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if down {
			http.Error(w, "gone fishing", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("0.0.0.0 ads.example.com\n0.0.0.0 tracker.example.org\n"))
	}))
	defer srv.Close()
	cfg := &model.Config{}
	cfg.Services.DNS.Enabled = true
	cfg.Blocking = model.Blocking{Enabled: true, Lists: []model.BlockList{{Name: "ads", Enabled: true, URL: srv.URL}}}
	loader := &recordingLoader{}
	r := &Refresher{Cache: NewCache(t.TempDir()), Fetcher: NewFetcher("test"), Source: func() *model.Config { return cfg }, Loader: loader}

	if rep := r.Tick(context.Background(), true); len(rep.Fetched) != 1 || rep.Failed != nil {
		t.Fatalf("first tick = %+v", rep)
	}
	if !strings.Contains(loader.last, "ads.example.com") {
		t.Fatalf("the list did not reach the loader:\n%s", loader.last)
	}

	down = true
	rep := r.Tick(context.Background(), true)
	if rep.Failed["ads"] == "" {
		t.Errorf("the failure was not reported: %+v", rep)
	}
	if m, ok := r.Cache.Meta("ads"); !ok || m.Domains != 2 {
		t.Errorf("cache after a failed fetch = %+v, %v", m, ok)
	}
	if loader.loads != 1 || !strings.Contains(loader.last, "ads.example.com") {
		t.Errorf("loader after a failed fetch: loads = %d, last =\n%s", loader.loads, loader.last)
	}
}
