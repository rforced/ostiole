package dnsprovider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A read the provider fails goes again, unless it said how long to wait,
// which is the caller's to honour. A write does not go again.
func TestAPIAsksAgainOnlyToRead(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		method, retryAfter string
		ok                 bool
		want               int32
	}{
		"a read it failed":            {method: http.MethodGet, ok: true, want: 2},
		"a read it asked to wait for": {method: http.MethodGet, retryAfter: "30", want: 1},
		"a write it failed":           {method: http.MethodPost, want: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var asked atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if asked.Add(1) == 1 {
					if tc.retryAfter != "" {
						w.Header().Set("Retry-After", tc.retryAfter)
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(srv.Close)
			a := &api{name: "Example", base: srv.URL, http: srv.Client(), retryWait: time.Millisecond}
			_, err := a.do(context.Background(), tc.method, "/records", nil, nil)
			if n := asked.Load(); n != tc.want || (err == nil) != tc.ok {
				t.Errorf("asked %d times, err = %v; want %d times", n, err, tc.want)
			}
			if e := (*Error)(nil); tc.retryAfter != "" && (!errors.As(err, &e) || e.RetryAfter != 30*time.Second) {
				t.Errorf("err = %v, want the wait handed back", err)
			}
		})
	}
}
