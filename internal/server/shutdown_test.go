package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// A stream open when the daemon stops ends with it. Shutdown waits for
// every request, so a page left open held a restart on the router for the
// whole timeout, and the daemon then exited with an error.
func TestRunEndsStreamsWhenItStops(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Listen: addr}, Deps{Engine: eng, Auth: as, Log: fwlog.NewRing(10)}, slog.New(slog.DiscardHandler))
	}()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	send := func(method, path string, body any) (*http.Response, error) {
		var rdr io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rdr = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, "http://"+addr+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(RequestHeader, RequestHeaderValue)
		return client.Do(req)
	}
	// The server is up once it answers.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		resp, err := send(http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("setup: %d", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
	resp, err := send(http.MethodGet, "/api/v1/log/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := bufio.NewReader(resp.Body)
	if line, err := body.ReadString('\n'); err != nil || !strings.HasPrefix(line, ": connected") {
		t.Fatalf("the stream opened with %q, %v", line, err)
	}

	start := time.Now()
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run still waits for the stream")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("stopping took %v", took)
	}
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, body)
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Error("the stream is still open")
	}
}
