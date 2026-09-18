package nft

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// namespaced returns an Exec that runs nft inside a fresh unprivileged user
// and network namespace, or skips the test if that is not possible here
// (no nft, no unshare, or nested containers without userns).
func namespaced(t *testing.T) *Exec {
	t.Helper()
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not installed")
	}
	x := &Exec{Wrap: []string{"unshare", "-Urn"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := x.Version(ctx); err != nil {
		t.Skipf("cannot run nft in an unprivileged namespace: %v", err)
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
}
