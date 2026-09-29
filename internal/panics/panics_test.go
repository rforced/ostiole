package panics

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func captured() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// A loop that panics is started again, and one that returns is not.
func TestLoopStartsAPanickedLoopAgain(t *testing.T) {
	t.Parallel()
	log, buf := captured()
	runs := 0
	loop(t.Context(), log, "watcher", func(context.Context) {
		runs++
		if runs < 3 {
			panic("parser bug")
		}
	}, time.Millisecond, 4*time.Millisecond)
	if runs != 3 {
		t.Errorf("ran %d times, want 3: twice panicking, then returning", runs)
	}
	if got := strings.Count(buf.String(), "parser bug"); got != 2 {
		t.Errorf("logged %d panics, want 2:\n%s", got, buf)
	}
	if !strings.Contains(buf.String(), "panics_test.go") {
		t.Errorf("the log has no stack:\n%s", buf)
	}
}

// Shutting down while a loop waits to start again ends it.
func TestLoopStopsWithItsContext(t *testing.T) {
	t.Parallel()
	log, _ := captured()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop(ctx, log, "watcher", func(context.Context) { panic("again") }, time.Hour, time.Hour)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop outlived its context")
	}
}

func TestIntoFailsTheWork(t *testing.T) {
	t.Parallel()
	log, _ := captured()
	work := func() (err error) {
		defer Into(&err, log, "backup")
		panic("unexpected answer")
	}
	if err := work(); err == nil || !strings.Contains(err.Error(), "backup panicked") {
		t.Errorf("err = %v", err)
	}
	ok := func() (err error) {
		defer Into(&err, log, "backup")
		return errors.New("plain")
	}
	if err := ok(); err == nil || err.Error() != "plain" {
		t.Errorf("an error without a panic became %v", err)
	}
}

// A goroutine that panics ends there, and every panic is logged.
func TestRecoverLogsEveryPanic(t *testing.T) {
	t.Parallel()
	log, buf := captured()
	for range 3 {
		func() {
			defer Recover(log, "order")
			panic("bad answer")
		}()
	}
	if got := strings.Count(buf.String(), "bad answer"); got != 3 {
		t.Errorf("logged %d of 3 panics", got)
	}
}

// A packet that panics its parser is dropped, and the log hears of one a
// minute however many arrive.
func TestDropDropsThePacketAndSamplesTheLog(t *testing.T) {
	t.Parallel()
	log, buf := captured()
	hook := func() int {
		defer Drop(log, "test packet")
		panic("short header")
	}
	for range 50 {
		hook()
	}
	if got := strings.Count(buf.String(), "short header"); got != 1 {
		t.Errorf("logged %d of 50 panics, want 1", got)
	}
	if n, ok := sample("test packet", time.Now().Add(sampleEvery)); !ok || n != 49 {
		t.Errorf("a minute on: logged %v, with %d unlogged; want 49", ok, n)
	}
}
