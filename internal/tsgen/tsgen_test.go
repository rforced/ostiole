package tsgen

import (
	"flag"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/server"
)

var update = flag.Bool("update", false, "rewrite web/src/lib/types.gen.ts")

const goldenPath = "../../web/src/lib/types.gen.ts"

// apiRoots generates the config model and every API reply and request body,
// reading string enums from any package under internal/.
func apiRoots(t *testing.T) (Result, error) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dirs["ostiole/internal/"+filepath.ToSlash(rel)] = path
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dirs["ostiole/internal"] = root
	return Generate(append([]reflect.Type{reflect.TypeFor[model.Config]()}, server.APITypes()...), dirs)
}

func TestTypesGolden(t *testing.T) {
	res, err := apiRoots(t)
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(goldenPath, res.Source, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing %s (run: go test ./internal/tsgen -update): %v", goldenPath, err)
	}
	if string(want) != string(res.Source) {
		t.Errorf("web/src/lib/types.gen.ts is stale: run `go test ./internal/tsgen -update` and commit it")
	}
}

type Mode string

type ModeE string

type Level int

type Plain string

type Names []string

type Base struct {
	ID   string `json:"id"`
	Skip string `json:"-"`
}

type Inner struct {
	N int `json:"n"`
}

type Toy struct {
	Base
	*Inner
	Mode     Mode                  `json:"mode"`
	ModeOpt  Mode                  `json:"modeOpt,omitempty"`
	ModePtr  *Mode                 `json:"modePtr"`
	Modes    []Mode                `json:"modes"`
	ModeE    ModeE                 `json:"modeEmpty"`
	Level    Level                 `json:"level"`
	Plain    Plain                 `json:"plain"`
	Names    Names                 `json:"names"`
	Tags     []string              `json:"tags"`
	Opt      []string              `json:"opt,omitempty"`
	Ptr      *Inner                `json:"ptr"`
	OptPtr   *Inner                `json:"optPtr,omitempty"`
	Zero     Inner                 `json:"zero,omitzero"`
	Addr     netip.Addr            `json:"addr"`
	Net      *netip.Prefix         `json:"net"`
	At       time.Time             `json:"at"`
	Wait     time.Duration         `json:"wait"`
	Raw      []byte                `json:"raw"`
	ByInt    map[int]Inner         `json:"byInt"`
	ByPrefix map[netip.Prefix]bool `json:"byPrefix,omitempty"`
	Any      any                   `json:"any"`
	Nest     [][]*Inner            `json:"nest"`
	Count    int                   `json:"count,string"`
	Fixed    [2]string             `json:"fixed"`
	Dash     string                `json:"a-b"`
	NoTag    float64
	hidden   int
	Vals     map[string]*Inner `json:"vals"`
}

const toyConsts = `package toy

const (
	ModeA Mode = "a"
	ModeB Mode = "b'q"
	ModeA2 Mode = "a"
)

const (
	ModeEEmpty ModeE = ""
	ModeEX     ModeE = "x"
)

const (
	LevelLow Level = iota
	LevelHigh
)
`

func toyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "consts.go"), []byte(toyConsts), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func toy(t *testing.T) string {
	t.Helper()
	dir := toyDir(t)
	res, err := Generate([]reflect.Type{reflect.TypeFor[Toy]()}, map[string]string{reflect.TypeFor[Toy]().PkgPath(): dir})
	if err != nil {
		t.Fatal(err)
	}
	return string(res.Source)
}

func TestMapping(t *testing.T) {
	_ = Toy{hidden: 1}
	out := toy(t)
	for _, want := range []string{
		"export interface Toy {\n  id: string\n  n?: number\n  mode: Mode | ''\n",
		"export type Mode = 'a' | 'b\\'q'\n",
		"export type Level = number\n",
		"export type Plain = string\n",
		"export type Names = string[]\n",
		"  names: Names | null\n",
		"  modeOpt?: Mode\n",
		"  modePtr: Mode | null\n",
		"  modes: Mode[] | null\n",
		"  modeEmpty: ModeE\n",
		"  tags: string[] | null\n",
		"  opt?: string[]\n",
		"  ptr: Inner | null\n",
		"  optPtr?: Inner\n",
		"  zero?: Inner\n",
		"  addr: string\n",
		"  net: string | null\n",
		"  at: string\n",
		"  wait: number\n",
		"  raw: string | null\n",
		"  byInt: Record<string, Inner> | null\n",
		"  byPrefix?: Record<string, boolean>\n",
		"  any: unknown\n",
		"  nest: ((Inner | null)[] | null)[] | null\n",
		"  count: string\n",
		"  fixed: [string, string]\n",
		"  'a-b': string\n",
		"  NoTag: number\n",
		"  vals: Record<string, Inner | null> | null\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, bad := range []string{"Skip", "hidden", "Base", "netip"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q in\n%s", bad, out)
		}
	}
	if !strings.HasPrefix(out, Header) {
		t.Errorf("header missing")
	}
}

func TestEmbeddedPointerIsOptional(t *testing.T) {
	type E struct {
		*Inner
	}
	res, err := Generate([]reflect.Type{reflect.TypeFor[E]()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Source), "  n?: number\n") {
		t.Errorf("got\n%s", res.Source)
	}
}

type original struct {
	A string `json:"a"`
}

type redefined original

type Record struct {
	N int `json:"n"`
}

type retagged struct {
	A string `json:"b"`
}

func TestRedefinedStructIsAnAlias(t *testing.T) {
	res, err := Generate([]reflect.Type{
		reflect.TypeFor[original](), reflect.TypeFor[redefined](), reflect.TypeFor[retagged](),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Source)
	if !strings.Contains(out, "export type Redefined = Original\n") || !strings.Contains(out, "export interface Retagged {") {
		t.Errorf("got\n%s", out)
	}
}

func TestArrayIsATuple(t *testing.T) {
	type W struct {
		P [3]float64 `json:"p"`
	}
	res, err := Generate([]reflect.Type{reflect.TypeFor[W]()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Source), "  p: [number, number, number]\n") {
		t.Errorf("got\n%s", res.Source)
	}
}

func TestGlobalNamesArePrefixed(t *testing.T) {
	type W struct {
		R Record            `json:"r"`
		M map[string]string `json:"m"`
	}
	res, err := Generate([]reflect.Type{reflect.TypeFor[W]()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Source)
	if !strings.Contains(out, "export interface TsgenRecord {") || !strings.Contains(out, "  r: TsgenRecord\n") {
		t.Errorf("got\n%s", out)
	}
}

type custom struct{}

func (custom) MarshalJSON() ([]byte, error) { return nil, nil }

func TestMarshalJSONFails(t *testing.T) {
	type W struct {
		C custom `json:"c"`
	}
	_, err := Generate([]reflect.Type{reflect.TypeFor[W]()}, nil)
	if err == nil || !strings.Contains(err.Error(), "MarshalJSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadMapKeyFails(t *testing.T) {
	type W struct {
		M map[bool]string `json:"m"`
	}
	if _, err := Generate([]reflect.Type{reflect.TypeFor[W]()}, nil); err == nil {
		t.Fatal("want an error")
	}
}
