package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHandlerFS(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"index.html":       {Data: []byte("<html>index</html>")},
		"assets/app.1.js":  {Data: []byte("js")},
		"assets/nested/.x": {Data: []byte("")},
	}
	srv := httptest.NewServer(HandlerFS(fsys))
	defer srv.Close()

	cases := []struct {
		path   string
		status int
		body   string
		cache  string
	}{
		{"/", 200, "<html>index</html>", "no-cache"},
		{"/firewall/rules", 200, "<html>index</html>", "no-cache"},
		{"/assets/app.1.js", 200, "js", "public, max-age=31536000, immutable"},
		{"/assets/", 200, "<html>index</html>", "no-cache"},
		{"/assets/missing.js", 200, "<html>index</html>", "no-cache"},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, resp.StatusCode, tc.status)
		}
		if string(body) != tc.body {
			t.Errorf("%s: body %q, want %q", tc.path, body, tc.body)
		}
		if got := resp.Header.Get("Cache-Control"); got != tc.cache {
			t.Errorf("%s: cache-control %q, want %q", tc.path, got, tc.cache)
		}
	}
}

func TestHandlerFSUnbuilt(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(HandlerFS(fstest.MapFS{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", resp.StatusCode)
	}
}
