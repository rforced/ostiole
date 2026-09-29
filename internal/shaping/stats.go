package shaping

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// CakeStats is one queue as the kernel reports it. Every counter is
// cumulative since the queue was installed: a rate is the difference
// between two samples, which the page showing it works out rather than
// the router keeping a history nobody asked for.
type CakeStats struct {
	// Bits is the ceiling the queue was given, in bit/s. tc reports it in
	// bytes per second, which is nobody's idea of a line speed.
	Bits        int64      `json:"bits"`
	Bytes       uint64     `json:"bytes"`
	Packets     uint64     `json:"packets"`
	Drops       uint64     `json:"drops"`
	Backlog     uint64     `json:"backlog"`
	MemoryUsed  uint64     `json:"memoryUsed"`
	MemoryLimit uint64     `json:"memoryLimit"`
	Tins        []TinStats `json:"tins"`
}

// TinStats is one priority tier of one queue. PeakDelayUs is the figure
// worth watching: it is how long the longest-waiting packet sat in this
// queue, and its staying small while the line is full is the whole point
// of shaping.
type TinStats struct {
	Tier              model.Tier `json:"tier,omitempty"`
	SentBytes         uint64     `json:"sentBytes"`
	SentPackets       uint64     `json:"sentPackets"`
	Drops             uint64     `json:"drops"`
	ECNMarks          uint64     `json:"ecnMarks"`
	BacklogBytes      uint64     `json:"backlogBytes"`
	PeakDelayUs       uint64     `json:"peakDelayUs"`
	AvgDelayUs        uint64     `json:"avgDelayUs"`
	SparseFlows       uint64     `json:"sparseFlows"`
	BulkFlows         uint64     `json:"bulkFlows"`
	UnresponsiveFlows uint64     `json:"unresponsiveFlows"`
	// ThresholdBits is the share of the line this tier is guaranteed
	// before it starts borrowing from the others, in bit/s.
	ThresholdBits int64 `json:"thresholdBits"`
}

// The shapes tc prints. Only the fields worth showing are named; tc emits
// a good deal more that nobody would read.
type rawQdisc struct {
	Kind    string `json:"kind"`
	Handle  string `json:"handle"`
	Dev     string `json:"dev"`
	Root    bool   `json:"root"`
	Options struct {
		// Bandwidth is a number, or the string "unlimited" when the queue
		// was given no ceiling.
		Bandwidth any `json:"bandwidth"`
	} `json:"options"`
	Bytes       uint64   `json:"bytes"`
	Packets     uint64   `json:"packets"`
	Drops       uint64   `json:"drops"`
	Backlog     uint64   `json:"backlog"`
	MemoryUsed  uint64   `json:"memory_used"`
	MemoryLimit uint64   `json:"memory_limit"`
	Tins        []rawTin `json:"tins"`
}

type rawTin struct {
	ThresholdRate     uint64 `json:"threshold_rate"`
	SentBytes         uint64 `json:"sent_bytes"`
	SentPackets       uint64 `json:"sent_packets"`
	BacklogBytes      uint64 `json:"backlog_bytes"`
	PeakDelayUs       uint64 `json:"peak_delay_us"`
	AvgDelayUs        uint64 `json:"avg_delay_us"`
	Drops             uint64 `json:"drops"`
	ECNMark           uint64 `json:"ecn_mark"`
	SparseFlows       uint64 `json:"sparse_flows"`
	BulkFlows         uint64 `json:"bulk_flows"`
	UnresponsiveFlows uint64 `json:"unresponsive_flows"`
}

// ParseQdiscs reads one `tc -s -j qdisc show` and returns the queues
// Ostiole owns, keyed by the device each is on. Asking for every device
// at once is one command rather than one per interface, and each entry
// names where it came from.
func ParseQdiscs(raw []byte) (map[string]CakeStats, error) {
	var doc []rawQdisc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse tc json: %w", err)
	}
	out := map[string]CakeStats{}
	for _, q := range doc {
		if q.Kind != "cake" || !q.Root || q.Handle != HandleString() || q.Dev == "" {
			continue
		}
		st := CakeStats{
			Bits:        toBits(q.Options.Bandwidth),
			Bytes:       q.Bytes,
			Packets:     q.Packets,
			Drops:       q.Drops,
			Backlog:     q.Backlog,
			MemoryUsed:  q.MemoryUsed,
			MemoryLimit: q.MemoryLimit,
		}
		for i, t := range q.Tins {
			tin := TinStats{
				SentBytes:         t.SentBytes,
				SentPackets:       t.SentPackets,
				Drops:             t.Drops,
				ECNMarks:          t.ECNMark,
				BacklogBytes:      t.BacklogBytes,
				PeakDelayUs:       t.PeakDelayUs,
				AvgDelayUs:        t.AvgDelayUs,
				SparseFlows:       t.SparseFlows,
				BulkFlows:         t.BulkFlows,
				UnresponsiveFlows: t.UnresponsiveFlows,
				ThresholdBits:     bitsFromBytes(t.ThresholdRate),
			}
			// diffserv4 gives four tins in the order the tiers are named;
			// any other count is a queue somebody else configured and its
			// tins answer to no tier of ours.
			if len(q.Tins) == len(model.Tiers) {
				tin.Tier = model.Tiers[i]
			}
			st.Tins = append(st.Tins, tin)
		}
		out[q.Dev] = st
	}
	return out, nil
}

// toBits converts tc's bytes per second, leaving a queue with no ceiling
// at zero.
func toBits(v any) int64 {
	f, ok := v.(float64)
	if !ok || f < 0 || f > math.MaxInt64/8 {
		return 0
	}
	return int64(f) * 8
}

// bitsFromBytes is the same for a counter the kernel reports unsigned. A
// figure that would not fit is reported as the largest one that does
// rather than wrapping into a negative rate nobody can read.
func bitsFromBytes(v uint64) int64 {
	if v > math.MaxInt64/8 {
		return math.MaxInt64
	}
	return int64(v) * 8
}

// Report is the live state of everything the configuration shapes.
type Report struct {
	// Available says the tc command is there. Without it nothing below was
	// ever installed.
	Available bool `json:"available"`
	// TC is the binary in use, when there is one.
	TC string `json:"tc,omitempty"`
	// Reason says what is wrong, and what to install, when it is not.
	Reason string `json:"reason,omitempty"`
	// SampledAt is when the counters were read.
	SampledAt  time.Time         `json:"sampledAt"`
	Interfaces []InterfaceStatus `json:"interfaces"`
}

// InterfaceStatus is one shaped interface, with a block for each
// direction named as the operator named it.
type InterfaceStatus struct {
	Name        string         `json:"name"`
	Zone        string         `json:"zone,omitempty"`
	Description string         `json:"description,omitempty"`
	External    bool           `json:"external"`
	Link        model.LinkType `json:"link"`
	// Present says the link exists right now. A dialled session that has
	// not come up yet has nothing on it and nothing wrong with it.
	Present  bool            `json:"present"`
	Download DirectionStatus `json:"download"`
	Upload   DirectionStatus `json:"upload"`
}

// DirectionStatus is what was asked for in one direction and what is
// there.
type DirectionStatus struct {
	// Rate is the figure the operator gave, zero when this direction is
	// not shaped at all.
	Rate int64 `json:"rate"`
	// Installed says the queue is in the kernel.
	Installed bool       `json:"installed"`
	Stats     *CakeStats `json:"stats,omitempty"`
}

// Status reads the live queues and names each direction the way the
// operator does. It only reads, so it does not wait on an apply.
func (s *Shaper) Status(ctx context.Context, cfg *model.Config) (*Report, error) {
	rep := &Report{SampledAt: time.Now().UTC()}
	path, ok := Available(s.Bin)
	rep.Available, rep.TC = ok, path
	if !ok {
		rep.Reason = MissingMessage(s.PackageManager)
	}
	if cfg == nil {
		return rep, nil
	}

	stats := map[string]CakeStats{}
	if ok {
		raw, err := s.Run.QdiscsJSON(ctx)
		if err != nil {
			rep.Reason = err.Error()
		} else if stats, err = ParseQdiscs(raw); err != nil {
			rep.Reason = err.Error()
		}
	}

	for _, p := range Plans(cfg) {
		in, found := cfg.Interface(p.Interface)
		if !found {
			continue
		}
		st := InterfaceStatus{
			Name: p.Interface, Zone: in.Zone, Description: in.Description,
			External: p.External, Link: in.Shaping.LinkType(),
		}
		if s.Kernel != nil {
			st.Present, _ = s.Kernel.LinkExists(p.Interface)
		}
		for _, q := range []*Queue{p.Egress, p.Ingress} {
			if q == nil {
				continue
			}
			d := DirectionStatus{Rate: q.Rate}
			if cs, live := stats[q.Device]; live {
				d.Installed, d.Stats = true, &cs
			}
			if q.Direction == Download {
				st.Download = d
			} else {
				st.Upload = d
			}
		}
		rep.Interfaces = append(rep.Interfaces, st)
	}
	return rep, nil
}
