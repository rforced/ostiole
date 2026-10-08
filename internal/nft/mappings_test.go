package nft

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"ostiole/internal/netnstest"
)

// One UDP and one TCP mapping in the chain the daemon writes into, next to
// a rule of ours in another chain and a rule in the same chain that is not
// a mapping at all. Only the two mappings come back.
const mappingsJSON = `{"nftables":[
 {"metainfo":{"version":"1.1.5"}},
 {"rule":{"family":"inet","table":"ostiole","chain":"nat_prerouting","handle":7,"expr":[
   {"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":443}},
   {"dnat":{"addr":"192.168.1.10","port":443}}]}},
 {"rule":{"family":"inet","table":"ostiole","chain":"upnp_prerouting","handle":11,"expr":[
   {"match":{"op":"==","left":{"meta":{"key":"nfproto"}},"right":"ipv4"}},
   {"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":19132}},
   {"dnat":{"addr":"192.168.50.40","port":19132}}]}},
 {"rule":{"family":"inet","table":"ostiole","chain":"upnp_prerouting","handle":12,"expr":[
   {"match":{"op":"==","left":{"meta":{"key":"l4proto"}},"right":"tcp"}},
   {"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":32400}},
   {"dnat":{"addr":"192.168.50.20"}}]}},
 {"rule":{"family":"inet","table":"ostiole","chain":"upnp_prerouting","handle":13,"expr":[
   {"counter":{"packets":0,"bytes":0}}]}}
]}`

func TestParseMappings(t *testing.T) {
	t.Parallel()
	got, err := ParseMappings([]byte(mappingsJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d mappings, want 2: %+v", len(got), got)
	}
	// Sorted by external port, so the media server comes second.
	if got[0] != (Mapping{Protocol: "UDP", Internal: "192.168.50.40", ExternalPort: 19132, InternalPort: 19132}) {
		t.Errorf("first mapping = %+v", got[0])
	}
	// A dnat that names only an address keeps the port it arrived on.
	if got[1] != (Mapping{Protocol: "TCP", Internal: "192.168.50.20", ExternalPort: 32400, InternalPort: 32400}) {
		t.Errorf("second mapping = %+v", got[1])
	}
}

// A router with the service off has the chain but nothing in it, and one
// with no table at all never gets here. Neither is an error.
func TestParseMappingsWithNothingMapped(t *testing.T) {
	t.Parallel()
	got, err := ParseMappings([]byte(`{"nftables":[{"chain":{"name":"upnp_prerouting"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
	if _, err := ParseMappings([]byte("not json")); err == nil {
		t.Error("parsing rubbish succeeded")
	}
}

// A port written as anything but a number, and an address written as a
// range or a set, are not what the daemon writes for a mapping. A
// half-read rule would be worse than none, so they are skipped.
func TestParseMappingsSkipsWhatItCannotRead(t *testing.T) {
	t.Parallel()
	raw := `{"nftables":[
	 {"rule":{"chain":"upnp_prerouting","expr":[
	   {"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":{"range":[100,200]}}},
	   {"dnat":{"addr":"192.168.50.9","port":100}}]}},
	 {"rule":{"chain":"upnp_prerouting","expr":[
	   {"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":8080}},
	   {"dnat":{"addr":{"prefix":{"addr":"192.168.50.0","len":24}},"port":8080}}]}},
	 {"rule":{"chain":"upnp_prerouting","expr":[
	   {"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":"9000"}},
	   {"dnat":{"addr":"192.168.50.8","port":"9000"}}]}}
	]}`
	got, err := ParseMappings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	// Only the last survives: a port quoted as a string is still a port.
	if len(got) != 1 || got[0].ExternalPort != 9000 || got[0].Internal != "192.168.50.8" {
		t.Errorf("got %+v, want only the quoted port", got)
	}
}

// chainRunner answers the chain listing and HasTable, and fails a test that
// lists the whole table: on a router with feeds that took 364 ms.
type chainRunner struct {
	t        *testing.T
	chain    string
	chainErr error
	loaded   bool
	tableErr error
	asked    []string
}

func (r *chainRunner) Check(context.Context, string) error { return nil }
func (r *chainRunner) Apply(context.Context, string) error { return nil }
func (r *chainRunner) ListTableJSON(context.Context) ([]byte, error) {
	r.t.Error("the whole table was listed")
	return nil, errors.New("not here")
}
func (r *chainRunner) ListTableTerseJSON(context.Context) ([]byte, error) {
	r.t.Error("the whole table was listed")
	return nil, errors.New("not here")
}
func (r *chainRunner) ListChainJSON(_ context.Context, chain string) ([]byte, error) {
	r.asked = append(r.asked, chain)
	return []byte(r.chain), r.chainErr
}
func (r *chainRunner) HasTable(context.Context) (bool, error) {
	r.asked = append(r.asked, "tables")
	return r.loaded, r.tableErr
}

// The mappings come from the daemon's chain alone. Without the chain, UPnP
// is off and nothing is mapped; without the table, callers still hear so.
func TestReadMappingsListsTheChainAlone(t *testing.T) {
	t.Parallel()
	r := &chainRunner{t: t, chain: mappingsJSON}
	got, err := ReadMappings(t.Context(), r)
	if err != nil || len(got) != 2 || !slices.Equal(r.asked, []string{UPnPPreroutingChain}) {
		t.Errorf("mappings = %+v, %v, asked %q", got, err, r.asked)
	}

	r = &chainRunner{t: t, chainErr: ErrNoTable, loaded: true}
	if got, err := ReadMappings(t.Context(), r); err != nil || got == nil || len(got) != 0 {
		t.Errorf("UPnP off: mappings = %#v, %v", got, err)
	}

	r = &chainRunner{t: t, chainErr: ErrNoTable}
	if _, err := ReadMappings(t.Context(), r); !errors.Is(err, ErrNoTable) {
		t.Errorf("no table: %v, want ErrNoTable", err)
	}

	broken := errors.New("nft: cannot list tables")
	r = &chainRunner{t: t, chainErr: ErrNoTable, tableErr: broken}
	if _, err := ReadMappings(t.Context(), r); !errors.Is(err, broken) {
		t.Errorf("tables unreadable: %v", err)
	}
}

// What nft itself lists for the chain, with the daemon's rule in it, with
// UPnP switched off, and with no table at all.
func TestReadMappingsInKernel(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	x := &Exec{}
	if _, err := ReadMappings(ctx, x); !errors.Is(err, ErrNoTable) {
		t.Fatalf("before a load: %v, want ErrNoTable", err)
	}
	on, err := Render(loadConfig(t, "testdata/upnp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Apply(ctx, on); err != nil {
		t.Fatal(err)
	}
	if err := x.Apply(ctx, "add rule inet ostiole upnp_prerouting iifname eth0 udp dport 19132 dnat ip to 192.168.50.40:19132\n"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMappings(ctx, x)
	want := []Mapping{{Protocol: "UDP", Internal: "192.168.50.40", ExternalPort: 19132, InternalPort: 19132}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("with a mapping: %+v, %v", got, err)
	}
	off, err := Render(loadConfig(t, "testdata/minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Apply(ctx, off); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadMappings(ctx, x); err != nil || got == nil || len(got) != 0 {
		t.Errorf("UPnP off: %#v, %v", got, err)
	}
}
