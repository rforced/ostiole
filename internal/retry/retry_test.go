package retry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A service that errs or did not answer in time is worth asking again. One
// that answered no, asked to be left alone or could not be reached, and a
// body still arriving when the time ran out, are not.
func TestTransient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stall" {
			_, _ = w.Write([]byte("192.0.2.0/24\n"))
			w.(http.Flusher).Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	read := func(url string, timeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		return err
	}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"an error":          {StatusError(http.StatusInternalServerError), true},
		"busy":              {StatusError(http.StatusServiceUnavailable), true},
		"too many requests": {StatusError(http.StatusTooManyRequests), false},
		"not found":         {StatusError(http.StatusNotFound), false},
		"no answer in time": {read(srv.URL+"/hang", 100*time.Millisecond), true},
		"still arriving":    {read(srv.URL+"/stall", time.Second), false},
		"refused":           {read(closed.URL, time.Second), false},
		"called off":        {context.Canceled, false},
	} {
		if got := Transient(tc.err); got != tc.want {
			t.Errorf("%s (%v): %v, want %v", name, tc.err, got, tc.want)
		}
	}
	if got := StatusError(http.StatusServiceUnavailable).Error() + ", " + StatusError(520).Error(); got != "HTTP 503 Service Unavailable, HTTP 520" {
		t.Errorf("status errors read %q", got)
	}
}

// Do asks again while the failure may pass, waiting longer each time, and
// stops at once at one that will not or when it is called off.
func TestDo(t *testing.T) {
	t.Parallel()
	busy := StatusError(http.StatusServiceUnavailable)
	for name, tc := range map[string]struct {
		errs []error
		want int
	}{
		"busy once":       {errs: []error{busy}, want: 2},
		"busy every time": {errs: []error{busy, busy, busy, busy}, want: Attempts},
		"not found":       {errs: []error{StatusError(http.StatusNotFound)}, want: 1},
	} {
		tries := 0
		err := Do(context.Background(), time.Millisecond, Transient, func() error {
			tries++
			if tries <= len(tc.errs) {
				return tc.errs[tries-1]
			}
			return nil
		})
		if tries != tc.want || (err == nil) != (tc.want > len(tc.errs)) {
			t.Errorf("%s: tried %d times, err = %v; want %d times", name, tries, err, tc.want)
		}
	}

	start := time.Now()
	_ = Do(context.Background(), 20*time.Millisecond, Transient, func() error { return busy })
	if waited := time.Since(start); waited < 60*time.Millisecond {
		t.Errorf("waited %s over %d attempts, want 20ms and then 40ms", waited, Attempts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		tries := 0
		_ = Do(ctx, time.Hour, Transient, func() error {
			tries++
			cancel()
			return busy
		})
		done <- tries
	}()
	select {
	case tries := <-done:
		if tries != 1 {
			t.Errorf("tried %d times after being called off", tries)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still waiting to ask again after being called off")
	}
}
