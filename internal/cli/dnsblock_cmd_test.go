package cli

import (
	"bytes"
	"testing"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/store"
)

// A name under a domain override is never blocked, and "why" has to say
// that rather than "no list has it".
func TestWhyNamesTheDomainOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := model.Starter(model.StarterOptions{Hostname: "gateway"})
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Domain = "lan"
	cfg.Services.DNS.Upstreams = []string{"1.1.1.1"}
	cfg.Services.DNS.DomainOverrides = []model.DomainOverride{{Domain: "corp.example", Servers: []string{"10.0.0.53"}}}
	if _, err := store.New(dir).Save(cfg, ""); err != nil {
		t.Fatal(err)
	}

	cmd := newDNSBlockWhyCmd(&globals{configDir: dir})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"host.corp.example"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "host.corp.example is not blocked: corp.example is a domain override, answered by its own resolvers\n"
	if got := out.String(); got != want {
		t.Errorf("why said %q, want %q", got, want)
	}
}
