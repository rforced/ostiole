package services

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
)

func blockingConfig() *model.Config {
	cfg := &model.Config{}
	cfg.System.Hostname = "gateway"
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Domain = "lan"
	cfg.Blocking = model.Blocking{
		Enabled: true,
		Lists:   []model.BlockList{{Name: "ads", Enabled: true}},
	}
	return cfg
}

// blockBackend returns a backend over temporary directories with one list
// already fetched.
func blockBackend(t *testing.T) (*DNSBlock, *fakeCmd) {
	t.Helper()
	cache := dnsblock.NewCache(t.TempDir())
	if err := cache.Save(dnsblock.Meta{Name: "ads"}, []string{"ads.example.com", "tracker.example.net"}); err != nil {
		t.Fatal(err)
	}
	cmd := &fakeCmd{installed: true, active: true}
	return &DNSBlock{Dir: t.TempDir(), Cache: cache, Cmd: cmd}, cmd
}

func TestDNSBlockRenderCarriesOnlyTheInputs(t *testing.T) {
	d, _ := blockBackend(t)
	files, err := d.Render(blockingConfig())
	if err != nil {
		t.Fatal(err)
	}
	state, ok := files[blockStateName]
	if !ok {
		t.Fatalf("no state file in %v", files.Names())
	}
	if strings.Contains(state, "ads.example.com") {
		t.Errorf("the names themselves travelled through apply:\n%s", state)
	}
	if !strings.Contains(state, `"lists"`) || !strings.Contains(state, `"ads"`) {
		t.Errorf("the state does not name the lists:\n%s", state)
	}
	if len(files) != 1 {
		t.Errorf("rendered %v; want only the state file", files.Names())
	}
}

func TestDNSBlockRendersNothingWithoutDNS(t *testing.T) {
	d, _ := blockBackend(t)
	cfg := blockingConfig()
	cfg.Services.DNS.Enabled = false
	files, err := d.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("rendered %v with DNS off; want nothing", files.Names())
	}
}

func TestDNSBlockApplyWritesTheListAndRestartsOnce(t *testing.T) {
	d, cmd := blockBackend(t)
	files, err := d.Render(blockingConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(d.ConfPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "local=/ads.example.com/") {
		t.Errorf("the list did not reach dnsmasq's include:\n%s", raw)
	}
	if !cmd.has("systemctl", "try-restart", Unit) {
		t.Errorf("dnsmasq was not restarted, so it is still answering from the old list: %v", cmd.calls)
	}

	// Applying the same thing again must not interrupt DNS.
	before := len(cmd.calls)
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	for _, c := range cmd.calls[before:] {
		if strings.Join(c, " ") == "systemctl try-restart "+Unit {
			t.Errorf("an apply that changed nothing restarted dnsmasq anyway")
		}
	}
}

func TestDNSBlockApplyRestartsWhenTheListChanges(t *testing.T) {
	d, cmd := blockBackend(t)
	files, _ := d.Render(blockingConfig())
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if err := d.Cache.Save(dnsblock.Meta{Name: "ads"}, []string{"ads.example.com", "new.example.org"}); err != nil {
		t.Fatal(err)
	}
	before := len(cmd.calls)
	changed, err := d.Load(context.Background(), func(w io.Writer) error {
		o := dnsblock.OptionsFor(blockingConfig())
		_, rerr := dnsblock.Render(w, o, d.Cache)
		return rerr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a refreshed list was reported as no change")
	}
	raw, _ := os.ReadFile(d.ConfPath())
	if !strings.Contains(string(raw), "local=/new.example.org/") {
		t.Errorf("the refreshed list did not reach the include:\n%s", raw)
	}
	restarted := false
	for _, c := range cmd.calls[before:] {
		if strings.Join(c, " ") == "systemctl try-restart "+Unit {
			restarted = true
		}
	}
	if !restarted {
		t.Errorf("a changed list did not restart dnsmasq: %v", cmd.calls)
	}
}

func TestDNSBlockApplyClearsUpWhenDNSGoesAway(t *testing.T) {
	d, _ := blockBackend(t)
	files, _ := d.Render(blockingConfig())
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{d.ConfPath(), filepath.Join(d.dir(), blockStateName)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there for a later dnsmasq to read", p)
		}
	}
}

// Lists off but DNS on still writes the include, because dnsmasq's own
// configuration names it either way, and the deny list is still in it.
func TestDNSBlockWithListsOffStillWritesTheDenyList(t *testing.T) {
	d, _ := blockBackend(t)
	cfg := blockingConfig()
	cfg.Blocking.Enabled = false
	cfg.Blocking.Deny = []string{"typed.example.com"}
	files, _ := d.Render(cfg)
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(d.ConfPath())
	if err != nil {
		t.Fatalf("dnsmasq is told to include a file that is not there: %v", err)
	}
	if strings.Contains(string(raw), "local=/ads.example.com/") {
		t.Errorf("the lists are off but a list name is still blocked:\n%s", raw)
	}
	if !strings.Contains(string(raw), "local=/typed.example.com/") {
		t.Errorf("the lists are off and the deny list went with them:\n%s", raw)
	}
}
