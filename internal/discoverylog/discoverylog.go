// Package discoverylog keeps the packets the discovery relay saw, relayed
// or dropped, and keeps the relay in step with the applied configuration.
package discoverylog

import (
	"context"
	"time"

	"ostiole/internal/discovery"
	"ostiole/internal/logfile"
	"ostiole/internal/logring"
	"ostiole/internal/logsearch"
	"ostiole/internal/model"
)

// Event is one packet the relay saw.
type Event = discovery.Event

// Log is the relay's packets in memory.
type Log = logring.Ring[Event, *Event]

// New returns an empty log of the default size.
func New() *Log {
	return logring.New[Event, *Event](model.DefaultDiscoveryLogEntries, model.Logging{}.MemoryKeep())
}

// Settings sizes the log in a configuration.
func Settings(c *model.Config) (int, time.Duration) {
	return c.Services.Discovery.Log.Size(model.DefaultDiscoveryLogEntries), c.System.Logging.MemoryKeep()
}

// Kept says whether the configuration keeps the log at all: while the
// relay runs.
func Kept(c *model.Config) bool { return c.DiscoveryActive() }

// The log's directory under logfile.Dir, and the format of its lines.
const (
	FileName    = "discovery"
	FileVersion = 1
)

// Files describes the log to the writer that keeps it in files.
func Files(l *Log) logfile.Log {
	return l.Files(FileName, FileVersion, Kept)
}

// ParseLine reads a line of the log's files.
func ParseLine(line []byte) (Event, time.Time, error) {
	return logring.Parse[Event, *Event](line)
}

// search hands over the values the Log tab shows for the event.
func search(a logsearch.Adder, e *Event) {
	a.Add(e.Protocol)
	a.Add(string(e.Kind))
	a.Add(e.From)
	for _, to := range e.To {
		a.Add(to)
	}
	a.Add(e.Name)
	a.Add(e.Source)
	a.Add(e.Dropped)
}

// Matcher is a search's test of an event, the same for the log's files as
// for its memory.
func Matcher(q logsearch.Query) func(*Event) bool {
	row := q.Row()
	return func(e *Event) bool {
		if q.Empty() {
			return true
		}
		row.Reset()
		search(&row, e)
		return row.Match()
	}
}

// RelayConfig is what the relay runs for a configuration: nothing unless
// discovery is active.
func RelayConfig(c *model.Config) discovery.Config {
	if c == nil || !c.DiscoveryActive() {
		return discovery.Config{}
	}
	d := c.Services.Discovery
	out := discovery.Config{
		MDNS: d.MDNS, SSDP: d.SSDP, Services: d.Services,
		ReplyPorts: [2]int{model.DiscoveryReplyPortFirst, model.DiscoveryReplyPortLast},
	}
	for _, l := range c.DiscoveryLinks() {
		out.Links = append(out.Links, discovery.Link{Name: l.Interface, Asks: l.Asks, Answers: l.Answers})
	}
	return out
}

// DefaultFollow is how often the follower reads the configuration.
const DefaultFollow = 5 * time.Second

// Follower keeps the relay and its log in step with the configuration the
// router runs, as the query log's watcher does.
type Follower struct {
	Relay interface{ Configure(discovery.Config) }
	Log   *Log
	// Source is the applied configuration, its logs sized to the budget.
	Source func() *model.Config
	// Interval is how often to look; zero means DefaultFollow.
	Interval time.Duration
}

// Run follows the configuration until ctx is done, the first pass at once.
func (f *Follower) Run(ctx context.Context) {
	interval := f.Interval
	if interval <= 0 {
		interval = DefaultFollow
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		f.Tick()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick puts the relay and the log in step once.
func (f *Follower) Tick() {
	cfg := f.Source()
	f.Relay.Configure(RelayConfig(cfg))
	if cfg != nil && f.Log != nil {
		f.Log.Configure(Settings(cfg))
	}
}
