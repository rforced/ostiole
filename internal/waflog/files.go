package waflog

import (
	"encoding/json"
	"time"

	"ostiole/internal/logfile"
	"ostiole/internal/model"
)

// The WAF events' directory under logfile.Dir, and the format of its
// lines: an entry as the API serves it, its number included, which it
// keeps for good.
const (
	FileName    = "events"
	FileVersion = 1
)

// Files describes the log to the writer that keeps it in files. Switching
// the proxy off stops new events, not the history, so it is always on.
func (l *Log) Files() logfile.Log {
	return logfile.Log{
		Name: FileName, Version: FileVersion,
		On: func(*model.Config) bool { return true },
		Days: func(c *model.Config) int {
			return int(c.Services.Proxy.Events.Retention() / (24 * time.Hour))
		},
		Newest: l.Newest,
		Size:   l.Size,
		Lines: logfile.Lines(l.After, func(e *Entry) uint64 { return e.Seq }, func(e *Entry) time.Time { return e.Logged },
			func() func([]byte, *Entry) []byte { return appendLine }),
	}
}

// appendLine appends an entry's line.
func appendLine(buf []byte, e *Entry) []byte {
	raw, err := json.Marshal(e)
	if err != nil {
		return buf
	}
	return append(buf, raw...)
}

// ParseLine reads a line of the WAF events' files. An entry is placed by
// when it was logged, not by when its request opened.
func ParseLine(line []byte) (Entry, time.Time, error) {
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return Entry{}, time.Time{}, err
	}
	// Files written before events were redacted come back without what
	// they should not have held.
	e.Event = e.Redacted()
	return e, e.Logged, nil
}
