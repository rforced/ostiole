// Package retry asks again when a request to another service fails for a
// reason that may pass.
package retry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Attempts is how many times a request is made before its failure
	// stands.
	Attempts = 3
	// DefaultWait is the pause before asking again, doubled each time.
	DefaultWait = 2 * time.Second
)

// StatusError is an answer other than the one asked for.
type StatusError int

func (s StatusError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("HTTP %d %s", int(s), http.StatusText(int(s))))
}

// Transient reports whether a failed download is worth asking for again: the
// service answered with a fault of its own, or did not answer in time. A body
// still arriving when the time ran out is not: it would run out again.
func Transient(err error) bool {
	if status, ok := errors.AsType[StatusError](err); ok {
		return status >= 500
	}
	ue, ok := errors.AsType[*url.Error](err)
	return ok && ue.Timeout()
}

// Do runs attempt until it succeeds, fails in a way again turns down, or has
// run Attempts times, pausing wait (DefaultWait when zero) before the second
// and twice as long before each after. It stops when ctx ends.
func Do(ctx context.Context, wait time.Duration, again func(error) bool, attempt func() error) error {
	if wait <= 0 {
		wait = DefaultWait
	}
	for try := 1; ; try++ {
		err := attempt()
		if err == nil || try == Attempts || !again(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait *= 2
	}
}
