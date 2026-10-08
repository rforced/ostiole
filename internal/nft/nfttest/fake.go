// Package nfttest provides an in-memory nft.Runner for tests.
package nfttest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"ostiole/internal/nft"
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

// ListTableTerseJSON implements nft.Runner: TableJSON with the sets'
// elements left out, as nft -t lists it.
func (f *Fake) ListTableTerseJSON(ctx context.Context) ([]byte, error) {
	raw, err := f.ListTableJSON(ctx)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Nftables []map[string]map[string]json.RawMessage `json:"nftables"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return raw, nil
	}
	for _, item := range doc.Nftables {
		delete(item["set"], "elem")
	}
	return json.Marshal(doc)
}

// ListChainJSON implements nft.Runner: the chain and its rules out of
// TableJSON, as nft lists one chain. A chain TableJSON does not mention
// lists empty rather than missing.
func (f *Fake) ListChainJSON(ctx context.Context, chain string) ([]byte, error) {
	raw, err := f.ListTableJSON(ctx)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return raw, nil
	}
	kept := []map[string]json.RawMessage{}
	for _, item := range doc.Nftables {
		var of struct {
			Chain string `json:"chain"`
			Name  string `json:"name"`
		}
		switch {
		case item["rule"] != nil && json.Unmarshal(item["rule"], &of) == nil && of.Chain == chain,
			item["chain"] != nil && json.Unmarshal(item["chain"], &of) == nil && of.Name == chain:
			kept = append(kept, item)
		}
	}
	doc.Nftables = kept
	return json.Marshal(doc)
}

// HasTable implements nft.Runner.
func (f *Fake) HasTable(_ context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tableLoaded(), nil
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
