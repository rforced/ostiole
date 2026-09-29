package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// quietRunner is a router on which every command prints nothing, so the
// report never asks the machine the test runs on.
type quietRunner struct{}

func (quietRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }

// hostServer is a signed-in admin on a router whose daemon is root, so
// the host actions are offered rather than refused out of hand. The
// router has nothing installed and no leftovers, whatever the machine
// running the test has.
func hostServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := host.Deps{
		Root: true, Dir: dir, Run: quietRunner{}, Proc: t.TempDir(),
		Locate: func(string) string { return "" },
	}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Host: h}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv
}

// The report answers on a router with nothing on it, which is what the
// end-to-end server and a dev run are.
func TestHostReportAnswers(t *testing.T) {
	t.Parallel()
	srv := hostServer(t)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/host", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	body := string(raw)
	for _, want := range []string{`"units"`, `"present"`, `"network"`, `"legacy"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the report has no %s: %s", want, body)
		}
	}
}

// leftoverKernel is the nftables side of a router. It lets go of a table
// once it is deleted, as the kernel does, so a status read after a flush
// shows the router as it now is.
type leftoverKernel struct {
	mu      sync.Mutex
	chains  []nft.ChainRef
	deleted []string
}

func (k *leftoverKernel) ListChains(context.Context) ([]nft.ChainRef, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.chains), nil
}

func (k *leftoverKernel) DeleteTable(_ context.Context, family, name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deleted = append(k.deleted, family+" "+name)
	k.chains = slices.DeleteFunc(k.chains, func(c nft.ChainRef) bool { return c.Family == family && c.Table == name })
	return nil
}

// deletions are the tables deleted so far, sorted.
func (k *leftoverKernel) deletions() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Sorted(slices.Values(k.deleted))
}

// legacyIptables is the iptables command on a router whose legacy filter
// table holds a rule until it is flushed.
type legacyIptables struct {
	mu      sync.Mutex
	flushed bool
	calls   []string
}

func (r *legacyIptables) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(filepath.Base(name) + " " + strings.Join(args, " "))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, line)
	switch {
	case line == "iptables -t filter -F":
		r.flushed = true
	case line == "iptables -t filter -S" && r.flushed:
		return []byte("-P INPUT ACCEPT\n-P FORWARD ACCEPT\n-P OUTPUT ACCEPT\n"), nil
	case line == "iptables -t filter -S":
		return []byte("-P INPUT DROP\n-P FORWARD DROP\n-P OUTPUT ACCEPT\n-A INPUT -p tcp --dport 22 -j ACCEPT\n"), nil
	}
	return nil, nil
}

// changes are the commands that changed a legacy table rather than read
// one.
func (r *legacyIptables) changes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if !strings.HasSuffix(c, " -S") && !strings.HasSuffix(c, " --version") {
			out = append(out, c)
		}
	}
	return out
}

// hasIptables is a router with the iptables commands and none of the
// daemons, whatever the machine running the test has.
func hasIptables(name string) string {
	if name == "iptables" || name == "ip6tables" {
		return "/usr/sbin/" + name
	}
	return ""
}

// leftoverRouter is a router with the admin signed in, whose kernel holds
// these chains and whose legacy module has these tables loaded.
func leftoverRouter(t *testing.T, root bool, legacy string, chains ...nft.ChainRef) (*httptest.Server, *leftoverKernel, *legacyIptables) {
	t.Helper()
	proc := t.TempDir()
	if legacy != "" {
		if err := os.WriteFile(filepath.Join(proc, "ip_tables_names"), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kernel := &leftoverKernel{chains: chains}
	run := &legacyIptables{}
	srv := newTestServerWith(t, func(d *Deps) {
		d.Host = host.Deps{Root: root, Kernel: kernel, Run: run, Proc: proc, Locate: hasIptables, Dir: t.TempDir()}
	})
	return srv, kernel, run
}

// oldFirewall is a router an older firewall ran on: nf_tables filter
// tables in both families, Docker's nat table, and a legacy filter table
// with a rule in it, beside Ostiole's own table.
func oldFirewall(t *testing.T, root bool) (*httptest.Server, *leftoverKernel, *legacyIptables) {
	t.Helper()
	return leftoverRouter(t, root, "filter\n",
		nft.ChainRef{Family: "ip", Table: "filter", Name: "INPUT"},
		nft.ChainRef{Family: "ip", Table: "nat", Name: "DOCKER"},
		nft.ChainRef{Family: "ip", Table: "nat", Name: "POSTROUTING"},
		nft.ChainRef{Family: "ip6", Table: "filter", Name: "INPUT"},
		nft.ChainRef{Family: "inet", Table: "ostiole", Name: "input"},
	)
}

// flushLeftovers is the page's request: no tables sweeps, and names clear
// exactly those.
func flushLeftovers(t *testing.T, srv *httptest.Server, tables ...string) (*http.Response, []byte) {
	t.Helper()
	return do(t, srv, http.MethodPost, "/api/v1/host/legacy/flush", map[string][]string{"tables": append([]string{}, tables...)})
}

// leftoverIDs are the leftovers a report lists, sorted.
func leftoverIDs(rep host.Report) []string {
	var ids []string
	for _, t := range rep.Legacy.Tables {
		ids = append(ids, t.ID())
	}
	slices.Sort(ids)
	return ids
}

// Clear leftovers takes what nothing recognisable owns, of both kinds, and
// leaves Docker's table and Ostiole's own alone. The answer carries the
// report read afterwards, which is what the page redraws from.
func TestClearingLeftoversSweepsOnlyWhatNothingOwns(t *testing.T) {
	t.Parallel()
	srv, kernel, legacy := oldFirewall(t, true)

	resp, raw := flushLeftovers(t, srv)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if got := kernel.deletions(); !slices.Equal(got, []string{"ip filter", "ip6 filter"}) {
		t.Errorf("deleted %v, want the two nftables tables nothing owns", got)
	}
	if !slices.Contains(legacy.changes(), "iptables -t filter -F") {
		t.Errorf("the legacy filter table was not flushed: %v", legacy.changes())
	}
	var res hostResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"table ip filter", "table ip6 filter", "legacy iptables table filter"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output %q does not mention %s", res.Output, want)
		}
	}
	// Docker's table is left. The module keeps the legacy table once it has
	// loaded it, but emptied it filters nothing, so the page stops offering
	// to clear it.
	if got := leftoverIDs(res.Status); !slices.Equal(got, []string{"ip nat"}) {
		t.Errorf("the status afterwards lists %v, want only Docker's table", got)
	}
}

// Naming a table clears exactly that one: a table something owns, which is
// what Clear anyway sends, or a legacy table with an nftables namesake.
func TestClearingANamedLeftoverTakesExactlyThatTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		id      string
		deleted []string
		flushed bool
	}{
		{id: "ip nat", deleted: []string{"ip nat"}},
		{id: "legacy ip filter", flushed: true},
	} {
		srv, kernel, legacy := oldFirewall(t, true)
		resp, raw := flushLeftovers(t, srv, tc.id)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.id, resp.StatusCode, raw)
		}
		if got := kernel.deletions(); !slices.Equal(got, tc.deleted) {
			t.Errorf("%s: deleted %v, want %v", tc.id, got, tc.deleted)
		}
		if got := slices.Contains(legacy.changes(), "iptables -t filter -F"); got != tc.flushed {
			t.Errorf("%s: legacy filter table flushed = %v, want %v", tc.id, got, tc.flushed)
		}
	}
}

// A request naming a table that is not a leftover, Ostiole's own included,
// is refused whole: not even the tables it named rightly are cleared.
func TestClearingSomethingThatIsNotALeftoverClearsNothing(t *testing.T) {
	t.Parallel()
	srv, kernel, legacy := oldFirewall(t, true)

	resp, raw := flushLeftovers(t, srv, "ip filter", "inet ostiole")
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "inet ostiole is not a leftover") {
		t.Errorf("%d %s, want 400 naming the table", resp.StatusCode, raw)
	}
	if got := kernel.deletions(); len(got) != 0 {
		t.Errorf("deleted %v", got)
	}
	if got := legacy.changes(); len(got) != 0 {
		t.Errorf("changed the legacy tables: %v", got)
	}
}

// A sweep with nothing to clear says so. A router whose only leftover is
// Docker's has nothing to clear either: the sweep never falls back to a
// table something is using.
func TestClearingARouterWithNothingToClearIsRefused(t *testing.T) {
	t.Parallel()
	ostiole := nft.ChainRef{Family: "inet", Table: "ostiole", Name: "input"}

	srv, kernel, _ := leftoverRouter(t, true, "", ostiole)
	resp, raw := flushLeftovers(t, srv)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "no leftover iptables rulesets") {
		t.Errorf("clean router: %d %s, want 400 saying there is nothing", resp.StatusCode, raw)
	}
	if got := kernel.deletions(); len(got) != 0 {
		t.Errorf("clean router: deleted %v", got)
	}

	// The page never offers this sweep, but the API says what the command
	// line does rather than that nothing was found.
	srv, kernel, _ = leftoverRouter(t, true, "", ostiole, nft.ChainRef{Family: "ip", Table: "nat", Name: "DOCKER"})
	if resp, raw := flushLeftovers(t, srv); resp.StatusCode != http.StatusBadRequest ||
		!strings.Contains(string(raw), "every leftover belongs to something still running") {
		t.Errorf("Docker's table alone: %d %s, want 400 saying whose it is", resp.StatusCode, raw)
	}
	if got := kernel.deletions(); len(got) != 0 {
		t.Errorf("deleted %v, which Docker is using", got)
	}
}

// A daemon that is not root cannot change the kernel, and says so before
// trying.
func TestClearingLeftoversNeedsARootDaemon(t *testing.T) {
	t.Parallel()
	srv, kernel, legacy := oldFirewall(t, false)

	resp, raw := flushLeftovers(t, srv)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "needs root") {
		t.Errorf("%d %s, want 400 saying the daemon is not root", resp.StatusCode, raw)
	}
	if got := kernel.deletions(); len(got) != 0 {
		t.Errorf("deleted %v", got)
	}
	if got := legacy.changes(); len(got) != 0 {
		t.Errorf("changed the legacy tables: %v", got)
	}
}
