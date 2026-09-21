package dnsblock

import (
	"bufio"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// Finding answers "why is this name blocked?" — the question every support
// conversation about DNS blocking starts with.
type Finding struct {
	Name string `json:"name"`
	// Blocked is what this router would answer for it now.
	Blocked bool            `json:"blocked"`
	Mode    model.BlockMode `json:"mode,omitempty"`
	// Matched is the entry that decided it, which is often a parent of the
	// name that was asked about.
	Matched string `json:"matched,omitempty"`
	// Reason is what kind of entry that was: a list, the deny list, the
	// allow list, a name this router answers for, a domain it has delegated,
	// or the Firefox canary.
	Reason string `json:"reason,omitempty"`
	// Lists names every enabled list that carries it, so "which one do I
	// turn off" has an answer.
	Lists []string `json:"lists,omitempty"`
}

// Reasons a name is or is not blocked.
const (
	ReasonList      = "list"
	ReasonDeny      = "deny"
	ReasonAllow     = "allow"
	ReasonNever     = "never"
	ReasonDelegated = "delegated"
	ReasonCanary    = "canary"
	ReasonNotAName  = "not-a-name"
	// ReasonOff is the DNS server being off: nothing is blocked at all.
	ReasonOff = "off"
	// ReasonListsOff is the lists being off, with nothing else naming it.
	ReasonListsOff = "lists-off"
)

// Has reports whether a list holds an entry at or above key, which is in
// reversed-label form, and returns that entry as a name.
type Has func(list, key string) (string, bool)

// Lookup works out what this router would do with a name, and why, reading
// the lists off the disk.
func Lookup(o Options, c *Cache, name string) Finding {
	return Decide(o, func(list, key string) (string, bool) { return coveringName(c, list, key) }, name)
}

// Decide is the order every question about a name is answered in: never,
// delegated, allow, the lists, deny, the canary. Lookup and the query log
// both go through it, so they cannot disagree.
func Decide(o Options, has Has, name string) Finding {
	f := Finding{Name: name, Mode: o.Mode, Lists: []string{}}
	target, ok := Normalize(name)
	if !ok {
		f.Reason = ReasonNotAName
		return f
	}
	f.Name = target
	key := ReverseLabels(target)

	// What this router answers for itself is never blocked, whatever a list
	// says. Nor is a domain it has handed to resolvers of its own. The
	// operator's allow list comes next.
	if m, hit := coveringEntry(o.Never, key); hit {
		f.Reason, f.Matched = ReasonNever, m
		return f
	}
	if m, hit := coveringEntry(o.Delegated, key); hit {
		f.Reason, f.Matched = ReasonDelegated, m
		return f
	}
	if m, hit := coveringEntry(o.Allow, key); hit {
		f.Reason, f.Matched = ReasonAllow, m
		return f
	}
	if !o.Enabled {
		f.Reason = ReasonOff
		return f
	}

	// Every list that carries it is worth reporting, even once the first
	// one has settled the answer. Lists is empty while the lists are off.
	for _, list := range o.Lists {
		if m, hit := has(list, key); hit {
			f.Blocked = true
			f.Lists = append(f.Lists, list)
			if f.Matched == "" {
				f.Reason, f.Matched = ReasonList, m
			}
		}
	}
	if m, hit := coveringEntry(o.Deny, key); hit {
		f.Blocked = true
		if f.Matched == "" || len(m) >= len(f.Matched) {
			f.Reason, f.Matched = ReasonDeny, m
		}
	}
	if o.Canary && (target == FirefoxCanary || strings.HasSuffix(target, "."+FirefoxCanary)) {
		f.Blocked = true
		if f.Matched == "" {
			f.Reason, f.Matched = ReasonCanary, FirefoxCanary
		}
	}
	if !f.Blocked && !o.UseLists {
		f.Reason = ReasonListsOff
	}
	return f
}

// coveringEntry finds the entry in a hand-written list that sits at or
// above key, and returns it as a name.
func coveringEntry(entries []string, key string) (string, bool) {
	for _, e := range entries {
		n, ok := Normalize(e)
		if !ok {
			continue
		}
		if covers(ReverseLabels(n), key) {
			return n, true
		}
	}
	return "", false
}

// coveringName looks for an entry in a cached list that sits at or above
// key. The file is in reversed order, so the scan stops as soon as it is
// past where the answer would be.
func coveringName(c *Cache, list, key string) (string, bool) {
	rc, err := c.Open(list)
	if err != nil {
		return "", false
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		k := ReverseLabels(line)
		if k > key {
			break // sorted, so nothing further can cover it
		}
		if covers(k, key) {
			return line, true
		}
	}
	return "", false
}
