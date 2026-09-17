package shaping

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadConfig(t *testing.T, path string) *model.Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &cfg
}

func TestRenderGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	if len(inputs) == 0 {
		t.Fatal("no testdata")
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files, err := Render(loadConfig(t, in))
			if err != nil {
				t.Fatal(err)
			}
			got := files.String()
			golden := strings.TrimSuffix(in, ".json") + ".tc"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("mismatch (run with -update to accept)\n--- got ---\n%s", got)
			}
		})
	}
}

// A direction with no figure gets no queue. Which half that leaves
// depends on where the interface faces: an upload figure on a WAN leaves
// the line, so it needs no helper device, and the same figure on a LAN
// arrives, so it needs nothing else.
func TestOneDirectionOnly(t *testing.T) {
	t.Parallel()
	files, err := Render(loadConfig(t, "testdata/upload-only.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(files.Names(), " "), "eth0.egress.tc eth1.ingress.tc"; got != want {
		t.Errorf("rendered %q, want %q", got, want)
	}
}

// A disabled interface is not shaped: it carries no traffic, and its
// helper device would be one more thing to explain.
func TestDisabledInterfaceIsIgnored(t *testing.T) {
	t.Parallel()
	files, err := Render(loadConfig(t, "testdata/full.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files.Names() {
		if strings.HasPrefix(name, "eth3.") {
			t.Errorf("eth3 is disabled, yet %s was rendered", name)
		}
	}
}

// The direction that leaves a WAN is the upload and the direction that
// leaves a LAN is the download, which is the whole of what "external"
// changes for the operator.
func TestDirectionFollowsWhereTheInterfaceFaces(t *testing.T) {
	t.Parallel()
	plans := Plans(loadConfig(t, "testdata/full.json"))
	got := map[string][2]Direction{}
	for _, p := range plans {
		var out [2]Direction
		if p.Egress != nil {
			out[0] = p.Egress.Direction
		}
		if p.Ingress != nil {
			out[1] = p.Ingress.Direction
		}
		got[p.Interface] = out
	}
	for iface, want := range map[string][2]Direction{
		"eth0":    {Upload, Download}, // faces the internet
		"eth1":    {Download, Upload}, // faces hosts
		"dsl":     {Upload, Download}, // a dialled session is a WAN
		"eth1.30": {Download, ""},     // capped one way only
	} {
		if got[iface] != want {
			t.Errorf("%s: egress/ingress = %v, want %v", iface, got[iface], want)
		}
	}
}

// A tagged frame carries four bytes the operator never sees, and a
// dialled session over a tagged link carries them too.
func TestVLANAddsItsOwnOverhead(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/full.json")
	tagged := taggedInterfaces(cfg)
	for iface, want := range map[string]bool{
		"eth0":           false,
		"eth1":           false,
		"eth1.30":        true,
		"enp0s31f6.4000": true,
		"dsl":            true, // the session runs over eth2.7
	} {
		if tagged[iface] != want {
			t.Errorf("%s tagged = %v, want %v", iface, tagged[iface], want)
		}
	}
}

// A long interface name will not fit in a device name, so it is cut down
// and hashed. The name has to stay the same across runs or every apply
// would leave the last one behind.
func TestLongNamesGetAShortStableHelper(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"eth0", "enp0s31f6.4000", "a-very-long-interface-name"} {
		ifb := IFBName(name)
		if len(ifb) > 15 {
			t.Errorf("IFBName(%q) = %q, which the kernel will refuse", name, ifb)
		}
		if ifb != IFBName(name) {
			t.Errorf("IFBName(%q) is not stable", name)
		}
	}
	if got, want := IFBName("eth0"), "ifb-eth0"; got != want {
		t.Errorf("IFBName(eth0) = %q, want %q", got, want)
	}
	if IFBName("enp0s31f6.4000") == IFBName("enp0s31f6.4001") {
		t.Error("two long names that differ only at the end collide")
	}
}
