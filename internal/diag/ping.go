// Package diag holds the diagnostics an admin reaches for when something
// is wrong: ping, traceroute, a packet capture, and the journal.
package diag

import (
	"context"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/rforced/ostiole/internal/gateway"
)

// PingOptions parameterise a ping run.
type PingOptions struct {
	// Target is an address or a name this firewall can resolve.
	Target string
	// Interface pins the probes to one link; empty lets routing decide.
	Interface string
	// Count is how many echo requests to send.
	Count int
	// Interval between requests and Timeout per request.
	Interval time.Duration
	Timeout  time.Duration
}

// Probe is one echo request and what came back.
type Probe struct {
	Seq   int     `json:"seq"`
	RTTMS float64 `json:"rttMs,omitempty"`
	Error string  `json:"error,omitempty"`
}

// PingResult is a finished run.
type PingResult struct {
	Target      string  `json:"target"`
	Address     string  `json:"address"`
	Probes      []Probe `json:"probes"`
	Sent        int     `json:"sent"`
	Received    int     `json:"received"`
	LossPercent float64 `json:"lossPercent"`
	MinMS       float64 `json:"minMs,omitempty"`
	AvgMS       float64 `json:"avgMs,omitempty"`
	MaxMS       float64 `json:"maxMs,omitempty"`
}

// Pinger sends one echo request; gateway.ICMPProber implements it.
type Pinger interface {
	Probe(ctx context.Context, address, iface string, timeout time.Duration) (time.Duration, error)
}

// Ping sends Count echo requests and summarises the answers.
func Ping(ctx context.Context, p Pinger, o PingOptions) (*PingResult, error) {
	if p == nil {
		p = gateway.NewICMPProber()
	}
	addr, err := Resolve(ctx, o.Target)
	if err != nil {
		return nil, err
	}
	count := o.Count
	if count <= 0 {
		count = 4
	}
	interval := o.Interval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	res := &PingResult{Target: o.Target, Address: addr, Probes: []Probe{}}
	var rtts []float64
	for i := 1; i <= count; i++ {
		if i > 1 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(interval):
			}
		}
		rtt, err := p.Probe(ctx, addr, o.Interface, timeout)
		res.Sent++
		if err != nil {
			res.Probes = append(res.Probes, Probe{Seq: i, Error: err.Error()})
			continue
		}
		ms := float64(rtt.Microseconds()) / 1000
		rtts = append(rtts, ms)
		res.Received++
		res.Probes = append(res.Probes, Probe{Seq: i, RTTMS: ms})
	}
	if res.Sent > 0 {
		res.LossPercent = float64(res.Sent-res.Received) / float64(res.Sent) * 100
	}
	if len(rtts) > 0 {
		sort.Float64s(rtts)
		res.MinMS, res.MaxMS = rtts[0], rtts[len(rtts)-1]
		var sum float64
		for _, v := range rtts {
			sum += v
		}
		res.AvgMS = sum / float64(len(rtts))
	}
	return res, nil
}

// Resolve turns a name or address into an address, preferring IPv4 so a
// diagnostic matches what most traffic does.
func Resolve(ctx context.Context, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("a target address or name is required")
	}
	if ip := net.ParseIP(target); ip != nil {
		return ip.String(), nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, target)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q: %w", target, err)
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("cannot resolve %q", target)
	}
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			return ip.IP.String(), nil
		}
	}
	return ips[0].IP.String(), nil
}
