// Package nfttest provides an in-memory nft.Runner for tests.
package nfttest

import (
	"context"
	"strings"
	"sync"

	"github.com/rforced/ostiole/internal/nft"
)

// Fake records applied rulesets and can be told to fail.
type Fake struct {
	mu       sync.Mutex
	applied  []string
	CheckErr error
	ApplyErr error
	// TableJSON is returned by ListTableJSON when a table is loaded.
	TableJSON string
}

var _ nft.Runner = (*Fake)(nil)

// Check implements nft.Runner.
func (f *Fake) Check(_ context.Context, _ string) error { return f.CheckErr }

// Apply implements nft.Runner.
func (f *Fake) Apply(_ context.Context, rs string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ApplyErr != nil {
		return f.ApplyErr
	}
	f.applied = append(f.applied, rs)
	return nil
}

// ListTableJSON implements nft.Runner. The table exists once a non-empty
// ruleset has been applied.
func (f *Fake) ListTableJSON(_ context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.tableLoaded() {
		return nil, nft.ErrNoTable
	}
	if f.TableJSON == "" {
		return []byte(`{"nftables":[]}`), nil
	}
	return []byte(f.TableJSON), nil
}

func (f *Fake) tableLoaded() bool {
	if len(f.applied) == 0 {
		return false
	}
	last := strings.TrimSpace(f.applied[len(f.applied)-1])
	return !strings.HasSuffix(last, "delete table inet ostiole")
}

// Applied returns a copy of every ruleset applied so far.
func (f *Fake) Applied() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.applied...)
}

// Last returns the most recently applied ruleset, or "".
func (f *Fake) Last() string {
	a := f.Applied()
	if len(a) == 0 {
		return ""
	}
	return a[len(a)-1]
}
