package nft

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netnstest"
	"github.com/rforced/ostiole/internal/testenv"
)

// namespaced returns an Exec that runs nft inside a fresh unprivileged user
// and network namespace, or skips the test if that is not possible here
// (no nft, no unshare, or nested containers without userns).
func namespaced(t *testing.T) *Exec {
	t.Helper()
	for _, bin := range []string{"nft", "unshare"} {
		if _, err := exec.LookPath(bin); err != nil {
			testenv.Unavailable(t, "%s not installed", bin)
		}
	}
	x := &Exec{Wrap: []string{"unshare", "-Urn"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := x.Version(ctx); err != nil {
		testenv.Unavailable(t, "cannot run nft in an unprivileged namespace: %v", err)
	}
	return x
}

func TestGoldenRulesetsLoadInKernel(t *testing.T) {
	x := namespaced(t)
	for _, in := range goldenCases(t) {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			cfg := loadConfig(t, in)
			ruleset, err := Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := x.Check(ctx, ruleset); err != nil {
				t.Fatalf("nft -c rejected rendered ruleset:\n%v\n--- ruleset ---\n%s", err, ruleset)
			}
		})
	}
}

func TestApplyAndReadBackCounters(t *testing.T) {
	namespaced(t) // skip if namespaces are unavailable
	cfg := loadConfig(t, "testdata/full.json")
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Each unshare invocation is a fresh namespace, so apply and list must
	// happen in one shell.
	script := "nft -f - <<'EOF' && nft -j list table inet ostiole\n" + ruleset + "EOF\n"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", "-Urn", "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apply+list failed: %v\n%s", err, out)
	}
	counters, err := ParseCounters(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"allow-lan", "web-from-servers", "block-smtp", "dns-to-self", "pf-web", "nat-lan"} {
		if _, ok := counters[id]; !ok {
			t.Errorf("counter for %q not found; have %v", id, keys(counters))
		}
	}
	if _, ok := counters["input/default-drop"]; !ok {
		t.Errorf("baseline counter input/default-drop missing")
	}
}

func TestListTableJSONNoTable(t *testing.T) {
	x := namespaced(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := x.ListTableJSON(ctx)
	if !errors.Is(err, ErrNoTable) {
		t.Fatalf("err = %v, want ErrNoTable", err)
	}
}

// The counters are on the rules, so a terse listing, which leaves out only
// the sets' elements, reads them in milliseconds where a router with feeds
// takes a third of a second over the full one.
func TestTerseListingKeepsEveryRule(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	x := &Exec{}
	if loaded, err := x.HasTable(ctx); err != nil || loaded {
		t.Fatalf("HasTable before a load = %v, %v", loaded, err)
	}
	if _, err := x.ListTableTerseJSON(ctx); !errors.Is(err, ErrNoTable) {
		t.Fatalf("terse listing before a load: %v, want ErrNoTable", err)
	}
	elements := 0
	for _, in := range goldenCases(t) {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		ruleset, err := Render(loadConfig(t, in))
		if err != nil {
			t.Fatal(err)
		}
		if err := x.Apply(ctx, ruleset); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if loaded, err := x.HasTable(ctx); err != nil || !loaded {
			t.Fatalf("%s: HasTable = %v, %v", name, loaded, err)
		}
		full, err := x.ListTableJSON(ctx)
		if err != nil {
			t.Fatal(err)
		}
		terse, err := x.ListTableTerseJSON(ctx)
		if err != nil {
			t.Fatal(err)
		}
		fullRules, fullElements := listed(t, full)
		terseRules, terseElements := listed(t, terse)
		elements += fullElements
		if terseElements != 0 {
			t.Errorf("%s: the terse listing has %d set elements", name, terseElements)
		}
		if !slices.Equal(fullRules, terseRules) {
			t.Errorf("%s: the rules differ\n--- full ---\n%s\n--- terse ---\n%s", name,
				strings.Join(fullRules, "\n"), strings.Join(terseRules, "\n"))
		}
	}
	if elements == 0 {
		t.Fatal("no golden has a set element to leave out")
	}
	if err := x.Apply(ctx, "delete table inet ostiole\n"); err != nil {
		t.Fatal(err)
	}
	if loaded, err := x.HasTable(ctx); err != nil || loaded {
		t.Fatalf("HasTable after the delete = %v, %v", loaded, err)
	}
}

// listed is a listing's rules, as nft wrote them, and how many set
// elements it carries.
func listed(t *testing.T, raw []byte) (rules []string, elements int) {
	t.Helper()
	var doc struct {
		Nftables []struct {
			Rule json.RawMessage `json:"rule"`
			Set  *struct {
				Elem []json.RawMessage `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, item := range doc.Nftables {
		if item.Rule != nil {
			rules = append(rules, string(item.Rule))
		}
		if item.Set != nil {
			elements += len(item.Set.Elem)
		}
	}
	return rules, elements
}

func TestCheckReportsSyntaxErrors(t *testing.T) {
	x := namespaced(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := x.Check(ctx, "table inet ostiole {\n chain input { type filter hook input priority filter; policy bogus; }\n}\n")
	var nerr *Error
	if !errors.As(err, &nerr) || nerr.Stderr == "" {
		t.Fatalf("expected *Error with stderr, got %v", err)
	}
}

func keys(c Counters) []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}

// The bootstrap ruleset is what a freshly installed router runs until its
// first apply, so it has to load on a real kernel like the rendered ones.
func TestBootstrapLoadsInKernel(t *testing.T) {
	x := namespaced(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, ports := range [][]uint16{{443, 22}, {8443}, nil} {
		ruleset := Bootstrap(ports)
		if err := x.Check(ctx, ruleset); err != nil {
			t.Fatalf("nft -c rejected the bootstrap ruleset for %v:\n%v\n--- ruleset ---\n%s", ports, err, ruleset)
		}
	}
	for _, in := range []string{"testdata/minimal.json", "testdata/full.json"} {
		ruleset := Fallback(loadConfig(t, in), []uint16{9443, 22})
		if err := x.Check(ctx, ruleset); err != nil {
			t.Fatalf("nft -c rejected the fallback ruleset for %s:\n%v\n--- ruleset ---\n%s", in, err, ruleset)
		}
	}
}

// A golden records the order the renderer wrote, right or wrong, so this
// checks the order where it counts: a packet from a mapped host leaves
// through a kernel running the ruleset, and the counters say which rule
// translated it. A NAT statement ends the chain, so only one may count it.
func TestOneToOneBeatsOutboundNATInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}

	cfg := loadConfig(t, "testdata/minimal.json")
	cfg.NAT.Outbound = model.OutboundNAT{Mode: model.OutboundHybrid, Rules: []model.OutboundRule{
		{ID: "nat-all", Enabled: true, Zone: "wan"},
	}}
	cfg.NAT.OneToOne = []model.OneToOneNAT{
		{ID: "one-mail", Enabled: true, Zone: "wan", External: "203.0.113.10", Internal: "192.168.1.25"},
	}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The mapped host's address is local here, so this process can send
	// as it, straight out of the WAN.
	netnstest.Dummy(t, "eth0", "198.51.100.2/24", "192.168.1.25/32")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialUDP("udp4",
		&net.UDPAddr{IP: net.ParseIP("192.168.1.25")},
		&net.UDPAddr{IP: net.ParseIP("198.51.100.1"), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	raw, err := x.ListTableJSON(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counters, err := ParseCounters(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := counters["one-mail"].Packets; got != 1 {
		t.Errorf("the 1:1 rule counted %d packets, want 1", got)
	}
	for _, key := range []string{"nat-all", "nat_postrouting/auto-nat:wan"} {
		if got := counters[key].Packets; got != 0 {
			t.Errorf("%s counted %d packets: it translated the mapped host", key, got)
		}
	}
}
