package nft

import "testing"

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
