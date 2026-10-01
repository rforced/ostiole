package feeds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rforced/ostiole/internal/fetch"
)

// A URL on the router itself is refused before anything connects to it,
// however the address is written and whoever typed it.
func TestInspectRefusesTheRouter(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("192.0.2.0/24\n"))
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	secure := strings.Replace(srv.URL, "http://", "https://", 1)

	f := NewFetcher(nil)
	for _, c := range []struct {
		url        string
		publicOnly bool
		want       error
	}{
		{srv.URL + "/list.txt", false, fetch.ErrNotAllowed},
		{"http://[::ffff:127.0.0.1]" + port + "/list.txt", false, fetch.ErrNotAllowed},
		{secure + "/list.txt", false, fetch.ErrNotAllowed},
		{secure + "/list.txt", true, fetch.ErrNotAllowed},
		{srv.URL + "/list.txt", true, fetch.ErrNotAllowed},
	} {
		if _, err := f.Inspect(context.Background(), c.url, c.publicOnly); !errors.Is(err, c.want) || err.Error() != c.want.Error() {
			t.Errorf("%s, publicOnly %v: err = %v, want %v alone", c.url, c.publicOnly, err, c.want)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the server was reached %d times", n)
	}
}

// testFetcher reads from servers on the loopback as if they were lists
// inside the network.
func testFetcher() *Fetcher { return NewFetcher(fetch.Inside()) }
