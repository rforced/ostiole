package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// FakeProbes stands in for the prober and the router where the daemon
// cannot probe, as in the browser tests: next hops and answers come from
// the JSON file at Path, read again at every probe, and no route moves.
type FakeProbes struct {
	Path string

	mu   sync.Mutex
	sent map[string]int
}

// fakeFile is what the file holds. Hops are each interface's next hops;
// Addresses say how an address answers. An address not listed answers in
// a millisecond.
type fakeFile struct {
	Hops      map[string]struct{ IPv4, IPv6 string } `json:"hops"`
	Addresses map[string]struct {
		MS     float64 `json:"ms"`
		Loss   int     `json:"loss"`
		Silent bool    `json:"silent"`
	} `json:"addresses"`
}

func (f *FakeProbes) read() fakeFile {
	var out fakeFile
	if raw, err := os.ReadFile(f.Path); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// Probe implements Prober: the address answers as the file says, losing
// its share of probes evenly.
func (f *FakeProbes) Probe(_ context.Context, address, _ string, timeout time.Duration) (time.Duration, error) {
	a, ok := f.read().Addresses[address]
	if !ok {
		return time.Millisecond, nil
	}
	f.mu.Lock()
	if f.sent == nil {
		f.sent = map[string]int{}
	}
	f.sent[address]++
	n := f.sent[address]
	f.mu.Unlock()
	if a.Silent || n*a.Loss/100 > (n-1)*a.Loss/100 {
		return 0, errors.New("timeout")
	}
	rtt := time.Duration(a.MS * float64(time.Millisecond))
	if rtt > timeout {
		return 0, errors.New("timeout")
	}
	return rtt, nil
}

// Resolve implements Router.
func (f *FakeProbes) Resolve(g Status) (v4, v6 string) {
	if g.Address != "" {
		if familyOf(g.Address) == FamilyIPv6 {
			return "", g.Address
		}
		return g.Address, ""
	}
	h := f.read().Hops[g.Interface]
	return h.IPv4, h.IPv6
}

// Demote implements Router and moves nothing.
func (*FakeProbes) Demote(Status) (bool, error) { return false, nil }

// Restore implements Router and moves nothing.
func (*FakeProbes) Restore(Status) (bool, error) { return false, nil }

// Forget implements Router.
func (*FakeProbes) Forget(Status) error { return nil }

// Carriers implements Router: the first gateway that has answered.
func (*FakeProbes) Carriers(gs []Status) ([]string, error) {
	for _, g := range gs {
		if g.Online {
			return []string{g.Name}, nil
		}
	}
	return nil, nil
}
