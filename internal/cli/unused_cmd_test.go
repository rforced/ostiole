package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"ostiole/internal/engine"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
)

// unusedRouter is a router saved with an alias nothing names and a rule
// switched off.
func unusedRouter(t *testing.T) *globals {
	t.Helper()
	dir := t.TempDir()
	cfg := model.Starter(model.StarterOptions{Hostname: "gateway", LAN: "eth1", LANAddress: "192.0.2.1/24", WAN: "eth0"})
	cfg.Aliases = []model.Alias{{Name: "spare_hosts", Type: model.AliasHosts, Entries: []string{"203.0.113.0/24"}}}
	cfg.Rules = append(cfg.Rules, model.Rule{ID: "old-rule", Zone: "lan", Action: model.ActionDrop, Protocol: model.ProtocolAny})
	if _, err := store.New(dir).Save(cfg, "", store.Author{}); err != nil {
		t.Fatal(err)
	}
	return &globals{configDir: dir}
}

func runUnused(t *testing.T, g *globals, args ...string) string {
	t.Helper()
	cmd := newUnusedCmd(g)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestUnusedListsWhatNothingUses(t *testing.T) {
	t.Parallel()
	want := "KIND   NAME         WHY               TAKES\n" +
		"alias  spare_hosts  Nothing names it  \n" +
		"\n" +
		"DISABLED\n" +
		"KIND  NAME\n" +
		"rule  old-rule\n" +
		"\n" +
		"nothing changed; pass --remove to delete the unused items\n"
	if got := runUnused(t, unusedRouter(t)); got != want {
		t.Errorf("unused printed\n%s\nwant\n%s", got, want)
	}
}

func TestUnusedHasNothingToDo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := model.Starter(model.StarterOptions{Hostname: "gateway", LAN: "eth1", LANAddress: "192.0.2.1/24", WAN: "eth0"})
	if _, err := store.New(dir).Save(cfg, "", store.Author{}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--remove", "-y"}} {
		want := "nothing to do: everything is used\n"
		if len(args) > 0 {
			want = "nothing to remove: everything is used\n"
		}
		if got := runUnused(t, &globals{configDir: dir}, args...); got != want {
			t.Errorf("unused %v printed %q, want %q", args, got, want)
		}
	}
}

func TestUnusedRemoveAppliesWithoutThem(t *testing.T) {
	t.Parallel()
	g := unusedRouter(t)
	fake := &nfttest.Fake{}
	g.eng = engine.New(g.store(), fake, nil, slog.New(slog.DiscardHandler))
	got := runUnused(t, g, "--remove", "-y")
	if !strings.Contains(got, "\n1 difference(s):\n  - aliases[spare_hosts] was ") || !strings.HasSuffix(got, "\nremoved and committed\n") {
		t.Errorf("unused --remove printed\n%s", got)
	}
	if len(fake.Applied()) != 1 {
		t.Errorf("applied %d rulesets, want 1", len(fake.Applied()))
	}
	cfg, err := g.store().Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Aliases) != 0 {
		t.Errorf("aliases left: %+v", cfg.Aliases)
	}
	if _, disabled := cfg.Unused(); len(disabled) != 1 || disabled[0].ID != "old-rule" {
		t.Errorf("switched-off items after removal: %+v", disabled)
	}
	want := "DISABLED\nKIND  NAME\nrule  old-rule\n\nnothing to remove: everything is used\n"
	if got := runUnused(t, g, "--remove", "-y"); got != want {
		t.Errorf("a second --remove printed %q, want %q", got, want)
	}
}
