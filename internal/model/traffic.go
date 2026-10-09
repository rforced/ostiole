package model

// Traffic counts what crosses the router per device. Links are always
// counted: they count links, not people.
type Traffic struct {
	// Devices counts traffic per device, from the bytes each connection
	// carried. Off by default: it records when each device is busy.
	Devices bool `json:"devices,omitempty"`
	// Destinations records what each device talks to. It needs Devices.
	Destinations TrafficDestinations `json:"destinations,omitzero"`
}

// TrafficDestinations records what each device talks to: a row an hour
// for each device, destination, protocol and port. Off by default, as
// the query log is.
type TrafficDestinations struct {
	Enabled bool `json:"enabled,omitempty"`
	// Entries is the most rows kept; zero keeps DefaultDestinationEntries.
	Entries int `json:"entries,omitempty"`
}

// Destination defaults and bounds. A row costs DestinationBytes with its
// names counted, and the rows grow towards the ceiling as they are made.
const (
	DefaultDestinationEntries = 100_000
	MaxDestinationEntries     = 10_000_000
	DestinationBytes          = 200
)

// DestinationsOn reports whether destinations are recorded: switched on, with the
// devices counted that they belong to.
func (t Traffic) DestinationsOn() bool { return t.Devices && t.Destinations.Enabled }

// Size is how many rows are kept, filling in the default.
func (d TrafficDestinations) Size() int {
	if d.Entries > 0 {
		return d.Entries
	}
	return DefaultDestinationEntries
}
