package fwlog

import "ostiole/internal/logring"

// Ring keeps the last N entries, none older than it is told to keep, and
// fans new ones out to subscribers. It grows into its ceiling as packets
// arrive, so a router told to keep a million pays for them only once it
// has seen them.
type Ring = logring.Ring[Entry, *Entry]

// NewRing returns a ring holding up to size entries, of any age.
func NewRing(size int) *Ring {
	return logring.New[Entry, *Entry](size, 0)
}
