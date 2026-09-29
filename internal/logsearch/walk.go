package logsearch

import (
	"context"
	"time"
)

// Chunk is how many entries are copied out of a log per hold of its lock.
// Matching happens outside the lock, so a search never makes the logging
// wait on it.
const Chunk = 4096

// Budget is how far one read walks before it answers with how far it got.
// The next read carries on from there.
type Budget struct {
	Entries int
	Time    time.Duration
}

// DefaultBudget is a million entries or a second, whichever comes first.
var DefaultBudget = Budget{Entries: 1_000_000, Time: time.Second}

// Log is a log as a walk reads it.
type Log[T any] interface {
	// Before copies the entries older than seq into buf, newest first, as
	// many as fit; zero seq starts at the newest. It reports how many it
	// copied and whether older ones remain.
	Before(seq uint64, buf []T) (n int, more bool)
}

// Page is one read of a log, newest first.
type Page[T any] struct {
	Entries []T
	// Next is the entry to read before for the page after; More says
	// there is one. The walk stops at the last entry it looked at, which
	// after a search is not always the last one it kept.
	Next uint64
	More bool
	// SearchedTo is when the entry the walk stopped at was logged, set
	// when the budget ran out before the page filled.
	SearchedTo time.Time
	// Last is the last entry the walk looked at and LastAt when it was
	// logged, zero when it looked at none; Walked is how many it looked
	// at. A read that carries on elsewhere, into a log's files, starts
	// from there with what is left of the budget.
	Last   uint64
	LastAt time.Time
	Walked int
}

// Walk reads a log from the newest entry older than before, keeping those
// keep accepts, until limit are kept, the log runs out, the budget does or
// ctx is done. seq and at say where an entry is and when it was logged.
func Walk[T any](ctx context.Context, log Log[T], before uint64, limit int, budget Budget,
	seq func(*T) uint64, at func(*T) time.Time, keep func(*T) bool,
) (Page[T], error) {
	page := Page[T]{Entries: make([]T, 0, min(limit, 256))}
	buf := make([]T, Chunk)
	started := time.Now()
	walked := 0
	cursor := before
	for {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		n, more := log.Before(cursor, buf)
		if n == 0 {
			return page, nil
		}
		for i := range n {
			e := &buf[i]
			cursor = seq(e)
			walked++
			page.Last, page.LastAt, page.Walked = cursor, at(e), walked
			left := i < n-1 || more
			if keep(e) {
				page.Entries = append(page.Entries, *e)
				if len(page.Entries) >= limit {
					page.Next, page.More = cursor, left
					return page, nil
				}
			}
			// The clock is read now and then: it costs more than a match.
			if walked >= budget.Entries || (walked%256 == 0 && time.Since(started) >= budget.Time) {
				page.Next, page.More = cursor, left
				if left {
					page.SearchedTo = at(e)
				}
				return page, nil
			}
		}
		if !more {
			return page, nil
		}
	}
}
