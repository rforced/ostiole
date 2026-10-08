package fwlog

import (
	"encoding/json"
	"time"

	"ostiole/internal/logfile"
	"ostiole/internal/model"
)

// The firewall log's directory under logfile.Dir, and the format of its
// lines: an entry as the API serves it, its number included, which it
// keeps for good. Format 1 had no number.
const (
	FileName    = "firewall"
	FileVersion = 2
)

// Files describes the ring to the writer that keeps it in files. The
// firewall log is always on.
func (r *Ring) Files() logfile.Log {
	return logfile.Log{
		Name: FileName, Version: FileVersion,
		On: func(*model.Config) bool { return true },
		Days: func(c *model.Config) int {
			return int(c.System.Management.FirewallLog.Retention() / (24 * time.Hour))
		},
		Newest: r.Newest,
		Size:   r.Size,
		Lines: logfile.Lines(r.After, func(e *Entry) uint64 { return e.Seq }, func(e *Entry) time.Time { return e.Time },
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

// ParseLine reads a line of the firewall log's files.
func ParseLine(line []byte) (Entry, time.Time, error) {
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil {
		return Entry{}, time.Time{}, err
	}
	return e, e.Time, nil
}
