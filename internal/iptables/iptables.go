// Package iptables finds what an older firewall left in the kernel and
// clears it out.
//
// Two different things are called iptables on a modern router, and they
// have to be looked for in two different places. The legacy
// implementation keeps tables inside the kernel module, and the only
// question that does not change the answer by asking it is to read
// /proc/net/ip_tables_names: the file exists exactly when the module is
// loaded, whereas running iptables-legacy-save loads the module and
// creates the empty tables it then reports. The nf_tables implementation
// is a front end that writes ordinary nftables tables — ip filter, ip
// nat and the rest — which is why they appear in the dashboard's
// foreign-table warning, and why deleting them is a job for nft.
//
// Nothing here runs on its own. Ostiole owns one table and never flushes
// anybody else's by default, so this is a report the operator reads and a
// sweep they ask for. The report names the chains it found, and a table
// that plainly belongs to something still running — Docker, libvirt,
// fail2ban, Tailscale — carries the name of its owner and is left out of
// the sweep unless somebody names that table explicitly.
package iptables

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rforced/ostiole/internal/nft"
)

// Backend is which implementation an iptables command talks to.
type Backend string

// The two backends, as iptables itself spells them in --version.
const (
	// NFT is the nf_tables front end, whose leftovers are nftables tables.
	NFT Backend = "nft"
	// Legacy is the original implementation, with tables of its own.
	Legacy Backend = "legacy"
)

// Runner runs a command and returns its combined output; swapped for a
// fake in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Kernel is the nftables side: what chains exist, and the one call that
// removes a table. *nft.Exec satisfies it, which is the only
// implementation outside tests.
type Kernel interface {
	ListChains(ctx context.Context) ([]nft.ChainRef, error)
	DeleteTable(ctx context.Context, family, name string) error
}

// Table is one leftover ruleset.
type Table struct {
	// Family is the nftables family ("ip", "ip6", "bridge", "arp"), or
	// empty for a legacy table, which has none.
	Family string `json:"family,omitempty"`
	// Name is filter, nat, mangle, raw or security.
	Name string `json:"name"`
	// Backend says which of the two put it there, and so which command
	// clears it.
	Backend Backend `json:"backend"`
	// Chains are the chains inside it, so the operator can see what a
	// table is for before agreeing to delete it.
	Chains []string `json:"chains,omitempty"`
	// Rules counts the rules a legacy table holds, where they could be
	// counted; the nf_tables side is counted by the ruleset view already.
	Rules int `json:"rules,omitempty"`
	// Owner is what recognisably put the table there, or empty. A table
	// with an owner is reported and never swept.
	Owner string `json:"owner,omitempty"`
}

// ID is how the table is named on the command line and in the API, e.g.
// "ip nat" for an nf_tables leftover and "legacy ip filter" for the other
// kind. The two implementations can both have a table called filter, and
// an operator asking for one must never get the other.
func (t Table) ID() string {
	if t.Backend == Legacy {
		return "legacy " + t.Family + " " + t.Name
	}
	return t.Family + " " + t.Name
}

// Report is what an older firewall left behind.
type Report struct {
	// Command is the iptables binary found on this router, or empty.
	Command string `json:"command,omitempty"`
	// Version is what it says about itself, backend and all.
	Version string `json:"version,omitempty"`
	// Backend is the implementation that command talks to.
	Backend Backend `json:"backend,omitempty"`
	// Tables are the leftovers, sorted so the report reads the same way
	// twice.
	Tables []Table `json:"tables"`
}

// Sweepable is the tables a flush would clear: everything with no
// recognisable owner still using it.
func (r Report) Sweepable() []Table {
	out := make([]Table, 0, len(r.Tables))
	for _, t := range r.Tables {
		if t.Owner == "" {
			out = append(out, t)
		}
	}
	return out
}

// Clean reports whether there is nothing to do.
func (r Report) Clean() bool { return len(r.Sweepable()) == 0 }

// Deps are the ways this package reaches the router.
type Deps struct {
	// Run runs iptables commands; nil means the real ones.
	Run Runner
	// Kernel reads and deletes nftables tables; nil skips the nf_tables
	// side, which is what a router with no nft to talk to gets.
	Kernel Kernel
	// Proc is where the legacy module reports its tables; empty means
	// /proc/net, and tests point it at a directory of their own.
	Proc string
	// Locate finds a command, so a test can describe a router that has
	// iptables without the machine running the test having one. nil looks
	// on PATH and in the sbin directories.
	Locate func(name string) string
}

// locate finds a command the way this router's dependencies say to.
func (d Deps) locate(name string) string {
	if d.Locate != nil {
		return d.Locate(name)
	}
	return locate(name)
}

// runner is the command runner, real ones by default.
func (d Deps) runner() Runner {
	if d.Run == nil {
		return ExecRunner{}
	}
	return d.Run
}

// iptablesTables are the names both implementations use. A table with one
// of these names in one of the families an iptables front end writes to
// came from iptables; anything else is somebody's own nftables ruleset
// and none of this package's business.
var iptablesTables = map[string]bool{
	"filter": true, "nat": true, "mangle": true, "raw": true, "security": true,
}

// iptablesFamilies are the families the nf_tables front end writes. It
// never writes inet, which is what tells its tables apart from a
// hand-written ruleset that happens to be called filter.
var iptablesFamilies = map[string]bool{
	"ip": true, "ip6": true, "bridge": true, "arp": true,
}

// owners recognise a table that is still in use by the thing that made
// it. The prefix of a chain name is enough: every one of these tools
// names its chains after itself.
var owners = []struct {
	prefix string
	owner  string
}{
	{"DOCKER", "Docker"},
	{"LIBVIRT", "libvirt"},
	{"f2b-", "fail2ban"},
	{"ts-", "Tailscale"},
	{"KUBE-", "Kubernetes"},
	{"CNI-", "a container runtime"},
	{"FLANNEL", "Flannel"},
	{"LOG_AND_DROP", "Podman"},
	{"NETAVARK", "Podman"},
}

// ownerOf names what a set of chains belongs to, or "" when nothing in
// them is recognised.
func ownerOf(chains []string) string {
	for _, c := range chains {
		upper := strings.ToUpper(c)
		for _, o := range owners {
			if strings.HasPrefix(upper, strings.ToUpper(o.prefix)) {
				return o.owner
			}
		}
	}
	return ""
}

// commands are the iptables front ends, in the order they are looked for.
// The suffixed names exist on distributions that ship both; where there
// is only one, `iptables` is it and --version says which.
var commands = []string{"iptables", "iptables-nft", "iptables-legacy"}

// Detect reports what is there. It never fails: a router with no iptables
// at all is the answer this is hoping for, and a command that cannot be
// run is reported as a router with nothing to clear rather than as an
// error on a status page.
func Detect(ctx context.Context, d Deps) Report {
	rep := Report{Tables: []Table{}}
	run := d.runner()
	for _, name := range commands {
		if path := d.locate(name); path != "" {
			rep.Command = path
			break
		}
	}
	if rep.Command != "" {
		if out, err := run.Run(ctx, rep.Command, "--version"); err == nil {
			rep.Version = strings.TrimSpace(string(out))
			rep.Backend = backendOf(rep.Version)
		}
	}
	rep.Tables = append(rep.Tables, legacyTables(ctx, d)...)
	rep.Tables = append(rep.Tables, nftTables(ctx, d.Kernel)...)
	sort.Slice(rep.Tables, func(i, j int) bool { return rep.Tables[i].ID() < rep.Tables[j].ID() })
	return rep
}

// backendOf reads the backend out of a version line, e.g.
// "iptables v1.8.10 (nf_tables)".
func backendOf(version string) Backend {
	switch {
	case strings.Contains(version, "nf_tables"):
		return NFT
	case strings.Contains(version, "legacy"):
		return Legacy
	}
	return ""
}

// procNames are the files the legacy modules publish, one line per loaded
// table, and the family each module filters.
var procNames = []struct{ file, family, command string }{
	{"ip_tables_names", "ip", "iptables"},
	{"ip6_tables_names", "ip6", "ip6tables"},
}

// legacyTables reads which legacy tables are loaded. The file is absent
// on a router whose legacy module was never loaded, which is the common
// case and not an error.
func legacyTables(ctx context.Context, d Deps) []Table {
	proc := d.Proc
	if proc == "" {
		proc = "/proc/net"
	}
	var out []Table
	for _, p := range procNames {
		raw, err := os.ReadFile(filepath.Join(proc, p.file))
		if err != nil {
			continue
		}
		for _, name := range lines(string(raw)) {
			if !iptablesTables[name] {
				continue
			}
			t := Table{Family: p.family, Name: name, Backend: Legacy}
			t.Chains, t.Rules = legacyContents(ctx, d, p.command, name)
			t.Owner = ownerOf(t.Chains)
			out = append(out, t)
		}
	}
	return out
}

// legacyContents lists the chains in a loaded legacy table and counts its
// rules. The module is already loaded — that is why the table was found —
// so asking costs nothing and changes nothing.
func legacyContents(ctx context.Context, d Deps, command, table string) (chains []string, rules int) {
	bin := d.legacyBin(command)
	if bin == "" {
		return nil, 0
	}
	out, err := d.runner().Run(ctx, bin, "-t", table, "-S")
	if err != nil {
		return nil, 0
	}
	for _, line := range lines(string(out)) {
		switch {
		case strings.HasPrefix(line, "-P "), strings.HasPrefix(line, "-N "):
			fs := strings.Fields(line)
			if len(fs) >= 2 {
				chains = append(chains, fs[1])
			}
		case strings.HasPrefix(line, "-A "):
			rules++
		}
	}
	return chains, rules
}

// nftTables finds the tables the nf_tables front end wrote. They are
// ordinary nftables tables, so the chains inside them are how an empty
// leftover is told from a ruleset something is still using.
func nftTables(ctx context.Context, k Kernel) []Table {
	if k == nil {
		return nil
	}
	chains, err := k.ListChains(ctx)
	if err != nil {
		return nil
	}
	byTable := map[string]*Table{}
	var order []string
	for _, c := range chains {
		if !iptablesFamilies[c.Family] || !iptablesTables[c.Table] {
			continue
		}
		key := c.Family + " " + c.Table
		if byTable[key] == nil {
			byTable[key] = &Table{Family: c.Family, Name: c.Table, Backend: NFT}
			order = append(order, key)
		}
		byTable[key].Chains = append(byTable[key].Chains, c.Name)
	}
	out := make([]Table, 0, len(order))
	for _, key := range order {
		t := byTable[key]
		t.Owner = ownerOf(t.Chains)
		out = append(out, *t)
	}
	return out
}

// ErrNothingToFlush means the sweep was asked to clear a router that is
// already clear.
var ErrNothingToFlush = errors.New("no leftover iptables rulesets were found")

// Flush clears the named tables and reports, line by line, what it did.
// The lines are shown to the operator, so they say what happened to each
// table rather than only that something did.
//
// Order matters inside a legacy table: the policies go back to ACCEPT
// before the rules are flushed, because a DROP policy with its accept
// rules removed is a locked-out router for as long as it takes to run the
// next command.
func Flush(ctx context.Context, d Deps, tables []Table) (string, error) {
	if len(tables) == 0 {
		return "", ErrNothingToFlush
	}
	var said []string
	var errs []error
	for _, t := range tables {
		if t.Owner != "" {
			errs = append(errs, fmt.Errorf("%s belongs to %s and was left alone", t.ID(), t.Owner))
			continue
		}
		switch t.Backend {
		case NFT:
			if d.Kernel == nil {
				errs = append(errs, fmt.Errorf("%s needs nft, which is not available here", t.ID()))
				continue
			}
			if err := d.Kernel.DeleteTable(ctx, t.Family, t.Name); err != nil {
				errs = append(errs, fmt.Errorf("delete table %s: %w", t.ID(), err))
				continue
			}
			said = append(said, "deleted nftables table "+t.ID())
		case Legacy:
			out, err := flushLegacy(ctx, d, t)
			said = append(said, out...)
			if err != nil {
				errs = append(errs, err)
			}
		default:
			errs = append(errs, fmt.Errorf("%s has no backend to clear it with", t.ID()))
		}
	}
	return strings.Join(said, "\n"), errors.Join(errs...)
}

// legacyBin is the command that talks to the legacy tables: the suffixed
// one on a distribution that ships both implementations, and the only one
// there is everywhere else.
func (d Deps) legacyBin(command string) string {
	if bin := d.locate(command + "-legacy"); bin != "" {
		return bin
	}
	return d.locate(command)
}

// builtinChains are the chains the kernel makes for a legacy table, which
// are the ones that carry a policy. A chain a table does not have is not
// an error worth reporting: the command simply says so and the sweep
// carries on.
var builtinChains = map[string][]string{
	"filter":   {"INPUT", "FORWARD", "OUTPUT"},
	"nat":      {"PREROUTING", "INPUT", "OUTPUT", "POSTROUTING"},
	"mangle":   {"PREROUTING", "INPUT", "FORWARD", "OUTPUT", "POSTROUTING"},
	"raw":      {"PREROUTING", "OUTPUT"},
	"security": {"INPUT", "FORWARD", "OUTPUT"},
}

// flushLegacy empties one legacy table: policies first, then the rules,
// then the chains somebody added.
func flushLegacy(ctx context.Context, d Deps, t Table) ([]string, error) {
	command := "iptables"
	if t.Family == "ip6" {
		command = "ip6tables"
	}
	bin := d.legacyBin(command)
	if bin == "" {
		return nil, fmt.Errorf("%s: no %s command to clear it with", t.ID(), command)
	}
	run := d.runner()
	for _, chain := range builtinChains[t.Name] {
		// A policy that cannot be set is either a chain this table does
		// not have or a table that does not take policies, and neither
		// stops the flush.
		_, _ = run.Run(ctx, bin, "-t", t.Name, "-P", chain, "ACCEPT")
	}
	for _, args := range [][]string{{"-F"}, {"-X"}, {"-Z"}} {
		argv := append([]string{"-t", t.Name}, args...)
		if out, err := run.Run(ctx, bin, argv...); err != nil {
			return nil, fmt.Errorf("%s %s: %w: %s", command, strings.Join(argv, " "), err, tail(out))
		}
	}
	return []string{fmt.Sprintf("flushed legacy %s table %s and set its policies to accept", command, t.Name)}, nil
}

// Find returns the table in the report with that ID, so a request can
// name one and be answered about the table this router actually has
// rather than about whatever the request described.
func (r Report) Find(id string) (Table, bool) {
	for _, t := range r.Tables {
		if t.ID() == id {
			return t, true
		}
	}
	return Table{}, false
}

// ExecRunner runs the real commands.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// C locale, because the version line is parsed.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	return cmd.CombinedOutput()
}

// sbinDirs are where the iptables commands live. A service's PATH does
// not always include them.
var sbinDirs = []string{"/usr/sbin", "/sbin", "/usr/local/sbin"}

// locate finds a command on PATH or in the sbin directories.
func locate(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range sbinDirs {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// lines splits output into trimmed, non-empty lines.
func lines(s string) []string {
	var keep []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	return keep
}

// tail keeps the end of a failed command's output, which is where the
// reason is.
func tail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 400 {
		s = "…" + s[len(s)-400:]
	}
	return s
}
