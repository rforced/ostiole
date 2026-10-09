package fwlog

import (
	"time"

	"ostiole/internal/logfile"
	"ostiole/internal/logring"
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
func Files(r *Ring) logfile.Log {
	return r.Files(FileName, FileVersion, func(*model.Config) bool { return true })
}

// Settings is the ring's ceiling and how long it keeps an entry under c.
func Settings(c *model.Config) (int, time.Duration) {
	return c.System.Management.FirewallLog.Size(), c.System.Logging.MemoryKeep()
}

// ParseLine reads a line of the firewall log's files.
func ParseLine(line []byte) (Entry, time.Time, error) {
	return logring.Parse[Entry, *Entry](line)
}
