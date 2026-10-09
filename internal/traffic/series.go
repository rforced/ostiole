package traffic

import (
	"time"
)

// The windows a series is read over.
const (
	Window5m  = "5m"
	Window24h = "24h"
	Window30d = "30d"
)

// Windows are the windows a page may ask for, shortest first.
var Windows = []string{Window5m, Window24h, Window30d}

// How long each resolution is kept.
const (
	fineKept   = 5 * time.Minute
	minuteKept = 24 * time.Hour
	hourKept   = 30 * 24 * time.Hour
)

// sample is the bytes moved each way in the interval that ended at end.
type sample struct {
	end      int64 // unix milliseconds
	dur      int64 // milliseconds
	down, up uint64
}

// bucket is the bytes moved each way in a minute or an hour from start.
type bucket struct {
	start    int64 // unix seconds
	down, up uint64
}

// series is what a link or a device moved: every sample of the last five
// minutes, then minutes for a day and hours for a month. A bucket is kept
// only where something moved, so a device that sleeps costs nothing.
type series struct {
	fine    []sample
	minutes []bucket
	hours   []bucket
	// since is when counting began, which a window reaching back further
	// says.
	since time.Time
	// written is the start of the last minute handed to the files.
	written int64
}

func newSeries(since time.Time) *series { return &series{since: since} }

// add counts what moved in the interval of dur that ended at end. loc is
// the router's zone, which the hours start in.
func (s *series) add(end time.Time, dur time.Duration, down, up uint64, loc *time.Location) {
	s.fine = append(s.fine, sample{end: end.UnixMilli(), dur: dur.Milliseconds(), down: down, up: up})
	cut := end.Add(-fineKept - time.Minute).UnixMilli()
	drop := 0
	for drop < len(s.fine) && s.fine[drop].end < cut {
		drop++
	}
	if drop > 0 {
		s.fine = append(s.fine[:0], s.fine[drop:]...)
	}
	if down == 0 && up == 0 {
		return
	}
	s.minutes = addBucket(s.minutes, end.Truncate(time.Minute).Unix(), down, up, end.Add(-minuteKept).Unix())
	s.hours = addBucket(s.hours, hourStart(end, loc).Unix(), down, up, end.Add(-hourKept).Unix())
}

// touch makes sure the minute from start has a bucket, an empty one when
// nothing moved in it, so the minute is handed to the files.
func (s *series) touch(start int64) {
	s.minutes = addBucket(s.minutes, start, 0, 0, start-int64(minuteKept/time.Second))
}

// hourStart is the start of the hour t falls in, in the router's zone, so
// a month's days break at its midnight.
func hourStart(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, loc)
}

// addBucket counts into the bucket from start, and lets go of those that
// started before cut.
func addBucket(bs []bucket, start int64, down, up uint64, cut int64) []bucket {
	if n := len(bs); n > 0 && bs[n-1].start == start {
		bs[n-1].down += down
		bs[n-1].up += up
	} else {
		bs = append(bs, bucket{start: start, down: down, up: up})
	}
	drop := 0
	for drop < len(bs) && bs[drop].start < cut {
		drop++
	}
	if drop > 0 {
		bs = append(bs[:0], bs[drop:]...)
	}
	return bs
}

// Point is a rate at a moment: when, and bits per second each way.
type Point [3]float64

// Totals is what moved each way over a window, in bytes.
type Totals struct {
	Down uint64 `json:"down"`
	Up   uint64 `json:"up"`
}

// read is the series over a window ending at now: its points, oldest
// first, and what moved in all. Minutes and hours are dense, a zero where
// nothing moved, from the window's start or the series', whichever is
// later. The five minutes are the samples as they were taken.
func (s *series) read(window string, now time.Time, loc *time.Location) ([]Point, Totals) {
	var pts []Point
	var tot Totals
	switch window {
	case Window24h, Window30d:
		step, bs, kept := time.Minute, s.minutes, minuteKept
		start := func(t time.Time) time.Time { return t.Truncate(time.Minute) }
		if window == Window30d {
			step, bs, kept = time.Hour, s.hours, hourKept
			start = func(t time.Time) time.Time { return hourStart(t, loc) }
		}
		from := start(now.Add(-kept)).Add(step)
		if first := start(s.since); first.After(from) {
			from = first
		}
		i := 0
		for i < len(bs) && bs[i].start < from.Unix() {
			i++
		}
		for t := from; !t.After(now); t = t.Add(step) {
			var b bucket
			if i < len(bs) && bs[i].start == t.Unix() {
				b = bs[i]
				i++
			}
			// The bucket that is still filling is a rate over what has
			// passed of it.
			secs := step.Seconds()
			if elapsed := now.Sub(t).Seconds(); elapsed < secs {
				secs = max(elapsed, 1)
			}
			pts = append(pts, Point{float64(t.Unix()), bits(b.down, secs), bits(b.up, secs)})
			tot.Down += b.down
			tot.Up += b.up
		}
	default:
		cut := now.Add(-fineKept).UnixMilli()
		for _, x := range s.fine {
			if x.end < cut {
				continue
			}
			secs := max(float64(x.dur)/1000, 0.001)
			pts = append(pts, Point{float64(x.end) / 1000, bits(x.down, secs), bits(x.up, secs)})
			tot.Down += x.down
			tot.Up += x.up
		}
	}
	return pts, tot
}

// bits is bytes over seconds as bits per second.
func bits(bytes uint64, secs float64) float64 { return float64(bytes) * 8 / secs }
