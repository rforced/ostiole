package iptables

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/nft"
)

// fakeRunner answers commands from a table keyed by the whole command
// line, and keeps the transcript: the order commands ran in is part of
// what this package has to get right.
type fakeRunner struct {
	out   map[string]string
	fail  map[string]bool
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	// The binary is found by path, so the transcript uses its base name to
	// stay readable wherever the test is run.
	line := strings.TrimSpace(filepath.Base(name) + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	if f.fail[line] {
		return []byte("nope"), errors.New("failed")
	}
	return []byte(f.out[line]), nil
}

func (f *fakeRunner) index(want string) int {
	for i, c := range f.calls {
		if c == want {
			return i
		}
	}
	return -1
}

// fakeKernel is the nftables side.
type fakeKernel struct {
	chains  []nft.ChainRef
	err     error
	deleted []string
	refuse  map[string]bool
}

func (f *fakeKernel) ListChains(context.Context) ([]nft.ChainRef, error) {
	return f.chains, f.err
}

func (f *fakeKernel) DeleteTable(_ context.Context, family, name string) error {
	if f.refuse[family+" "+name] {
		return errors.New("refused")
	}
	f.deleted = append(f.deleted, family+" "+name)
	return nil
}

// hasIptables answers for a router with the ordinary commands and none
// of the suffixed builds, which is the modern case. It is injected so
// this package is never tested against whatever the build machine
// happens to have installed.
func hasIptables(name string) string {
	switch name {
	case "iptables", "ip6tables":
		return "/usr/sbin/" + name
	}
	return ""
}

// hasBothBuilds answers for a distribution that ships the legacy and
// nf_tables builds side by side under suffixed names.
func hasBothBuilds(name string) string { return "/usr/sbin/" + name }

// procDir writes the files the legacy modules publish.
func procDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectFindsBothKinds(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{out: map[string]string{
		"iptables -t filter -S": "-P INPUT DROP\n-N ALLOW\n-A INPUT -p tcp --dport 22 -j ACCEPT\n-A INPUT -j ALLOW\n",
		"iptables -t nat -S":    "-P PREROUTING ACCEPT\n",
	}}
	kernel := &fakeKernel{chains: []nft.ChainRef{
		{Family: "ip", Table: "filter", Name: "INPUT"},
		{Family: "ip", Table: "nat", Name: "POSTROUTING"},
		{Family: "ip6", Table: "filter", Name: "FORWARD"},
		// Ostiole's own table, and a ruleset somebody wrote by hand: both
		// are none of this package's business.
		{Family: "inet", Table: "ostiole", Name: "input"},
		{Family: "inet", Table: "filter", Name: "input"},
	}}
	rep := Detect(context.Background(), Deps{
		Run:    run,
		Kernel: kernel,
		Locate: hasIptables,
		Proc:   procDir(t, map[string]string{"ip_tables_names": "filter\nnat\n"}),
	})

	var ids []string
	for _, tb := range rep.Tables {
		ids = append(ids, tb.ID())
	}
	want := []string{"ip filter", "ip nat", "ip6 filter", "legacy ip filter", "legacy ip nat"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("tables = %v, want %v", ids, want)
	}
	legacy, ok := rep.Find("legacy ip filter")
	if !ok {
		t.Fatal("no legacy filter table")
	}
	if legacy.Rules != 2 {
		t.Errorf("legacy filter rules = %d, want 2", legacy.Rules)
	}
	if strings.Join(legacy.Chains, " ") != "INPUT ALLOW" {
		t.Errorf("legacy filter chains = %v, want the policy and the added chain", legacy.Chains)
	}
	if rep.Clean() {
		t.Error("a router with five leftovers reported clean")
	}
}

// The inet family is what tells a hand-written ruleset apart from an
// iptables front end's, which never writes it.
func TestDetectIgnoresTablesNoFrontEndWrites(t *testing.T) {
	t.Parallel()
	kernel := &fakeKernel{chains: []nft.ChainRef{
		{Family: "inet", Table: "filter", Name: "input"},
		{Family: "ip", Table: "something-else", Name: "x"},
	}}
	rep := Detect(context.Background(), Deps{Run: &fakeRunner{}, Kernel: kernel, Proc: t.TempDir(), Locate: hasIptables})
	if len(rep.Tables) != 0 {
		t.Errorf("tables = %+v, want none", rep.Tables)
	}
	if !rep.Clean() {
		t.Error("a router with nothing left behind reported work to do")
	}
}

func TestDetectRecognisesOwners(t *testing.T) {
	t.Parallel()
	kernel := &fakeKernel{chains: []nft.ChainRef{
		{Family: "ip", Table: "nat", Name: "DOCKER"},
		{Family: "ip", Table: "nat", Name: "POSTROUTING"},
		{Family: "ip", Table: "filter", Name: "f2b-sshd"},
		{Family: "ip6", Table: "filter", Name: "INPUT"},
	}}
	rep := Detect(context.Background(), Deps{Run: &fakeRunner{}, Kernel: kernel, Proc: t.TempDir(), Locate: hasIptables})
	for id, want := range map[string]string{
		"ip nat":     "Docker",
		"ip filter":  "fail2ban",
		"ip6 filter": "",
	} {
		tb, ok := rep.Find(id)
		if !ok {
			t.Fatalf("no table %s", id)
		}
		if tb.Owner != want {
			t.Errorf("%s owner = %q, want %q", id, tb.Owner, want)
		}
	}
	// A sweep takes only what nothing else is using.
	var sweep []string
	for _, tb := range rep.Sweepable() {
		sweep = append(sweep, tb.ID())
	}
	if strings.Join(sweep, ",") != "ip6 filter" {
		t.Errorf("sweepable = %v, want only the unowned table", sweep)
	}
}

func TestDetectReadsTheBackendFromTheVersion(t *testing.T) {
	t.Parallel()
	for version, want := range map[string]Backend{
		"iptables v1.8.10 (nf_tables)": NFT,
		"iptables v1.8.7 (legacy)":     Legacy,
		"iptables v1.4.21":             Backend(""),
	} {
		if got := backendOf(version); got != want {
			t.Errorf("backendOf(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestFlushDeletesNftTables(t *testing.T) {
	t.Parallel()
	kernel := &fakeKernel{}
	out, err := Flush(context.Background(), Deps{Run: &fakeRunner{}, Kernel: kernel, Locate: hasIptables},
		[]Table{{Family: "ip", Name: "nat", Backend: NFT}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(kernel.deleted, ",") != "ip nat" {
		t.Errorf("deleted = %v, want ip nat", kernel.deleted)
	}
	if !strings.Contains(out, "ip nat") {
		t.Errorf("said %q, which does not name the table", out)
	}
}

// The policies go back to accept before the rules are flushed. The other
// order leaves a router dropping everything for as long as it takes to
// run the next command, which on a slow router is long enough to notice.
func TestFlushSetsPoliciesBeforeFlushing(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{}
	if _, err := Flush(context.Background(), Deps{Run: run, Locate: hasIptables},
		[]Table{{Family: "ip", Name: "filter", Backend: Legacy}}); err != nil {
		t.Fatal(err)
	}
	policy := run.index("iptables -t filter -P INPUT ACCEPT")
	flush := run.index("iptables -t filter -F")
	del := run.index("iptables -t filter -X")
	if policy < 0 || flush < 0 || del < 0 {
		t.Fatalf("transcript = %v", run.calls)
	}
	if policy > flush || flush > del {
		t.Errorf("ran in the wrong order: %v", run.calls)
	}
}

// A table somebody else is using is refused even when the sweep is handed
// it, because Flush is also reached from the API.
func TestFlushRefusesAnOwnedTable(t *testing.T) {
	t.Parallel()
	kernel := &fakeKernel{}
	_, err := Flush(context.Background(), Deps{Run: &fakeRunner{}, Kernel: kernel, Locate: hasIptables},
		[]Table{{Family: "ip", Name: "nat", Backend: NFT, Owner: "Docker"}})
	if err == nil || !strings.Contains(err.Error(), "Docker") {
		t.Errorf("err = %v, want it to name Docker", err)
	}
	if len(kernel.deleted) != 0 {
		t.Errorf("deleted %v anyway", kernel.deleted)
	}
}

// Where both builds are installed, the legacy tables are cleared with the
// command that talks to them and not with the one that talks to nftables.
func TestFlushPrefersTheLegacyBuild(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{}
	if _, err := Flush(context.Background(), Deps{Run: run, Locate: hasBothBuilds},
		[]Table{{Family: "ip6", Name: "filter", Backend: Legacy}}); err != nil {
		t.Fatal(err)
	}
	if run.index("ip6tables-legacy -t filter -F") < 0 {
		t.Errorf("transcript = %v, want ip6tables-legacy", run.calls)
	}
}

func TestFlushNeedsSomethingToDo(t *testing.T) {
	t.Parallel()
	if _, err := Flush(context.Background(), Deps{}, nil); !errors.Is(err, ErrNothingToFlush) {
		t.Errorf("err = %v, want ErrNothingToFlush", err)
	}
}

// An nf_tables leftover with no nft to delete it with is reported rather
// than silently skipped.
func TestFlushWithoutNft(t *testing.T) {
	t.Parallel()
	_, err := Flush(context.Background(), Deps{Run: &fakeRunner{}, Locate: hasIptables},
		[]Table{{Family: "ip", Name: "nat", Backend: NFT}})
	if err == nil || !strings.Contains(err.Error(), "nft") {
		t.Errorf("err = %v, want it to say nft is not available", err)
	}
}
