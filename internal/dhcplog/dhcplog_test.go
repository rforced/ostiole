package dhcplog

import (
	"testing"

	"ostiole/internal/logsearch"
)

// Lines as dnsmasq wrote them in podman on the six images (2.85 to 2.93,
// 2026-09-28), at Info and, with a transaction's number first, at Debug.
func TestParseReadsWhatDnsmasqWrites(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]Event{
		"DHCPDISCOVER(v0) 02:00:5e:00:53:8a ":                         {Message: "DISCOVER", Interface: "v0", MAC: "02:00:5e:00:53:8a"},
		"DHCPOFFER(v0) 10.9.0.99 02:00:5e:00:53:8a ":                  {Message: "OFFER", Interface: "v0", Address: "10.9.0.99", MAC: "02:00:5e:00:53:8a"},
		"DHCPACK(v0) 10.9.0.99 02:00:5E:00:53:8A labhost":             {Message: "ACK", Interface: "v0", Address: "10.9.0.99", MAC: "02:00:5e:00:53:8a", Name: "labhost"},
		"1251320881 DHCPACK(v0) 10.9.0.78 02:00:5e:00:53:90 labhost":  {Message: "ACK", Interface: "v0", Address: "10.9.0.78", MAC: "02:00:5e:00:53:90", Name: "labhost"},
		"DHCPNAK(eth1) 10.0.0.5 aa:bb:cc:dd:ee:ff wrong network":      {Message: "NAK", Interface: "eth1", Address: "10.0.0.5", MAC: "aa:bb:cc:dd:ee:ff", Detail: "wrong network"},
		"DHCPDISCOVER(eth1) aa:bb:cc:dd:ee:ff no address available":   {Message: "DISCOVER", Interface: "eth1", MAC: "aa:bb:cc:dd:ee:ff", Detail: "no address available"},
		"DHCPSOLICIT(v0) 00:03:00:01:02:00:5e:00:53:8a ":              {Message: "SOLICIT", Interface: "v0", DUID: "00:03:00:01:02:00:5e:00:53:8a"},
		"DHCPREPLY(v0) fd00:9::110 00:03:00:01:02:00:5e:00:53:8a ":    {Message: "REPLY", Interface: "v0", Address: "fd00:9::110", DUID: "00:03:00:01:02:00:5e:00:53:8a"},
		"1401163 DHCPREQUEST(v0) 00:03:00:01:02:00:5e:00:53:90 ":      {Message: "REQUEST", Interface: "v0", DUID: "00:03:00:01:02:00:5e:00:53:90"},
		"DHCPINFORMATION-REQUEST(br0) 00:01:00:01:2c:3d:4e:5f:aa:bb ": {Message: "INFORMATION-REQUEST", Interface: "br0", DUID: "00:01:00:01:2c:3d:4e:5f:aa:bb"},
	} {
		if got, ok := Parse(line); !ok || got != want {
			t.Errorf("%q: %+v, %v", line, got, ok)
		}
	}
}

// What is not a client's message is passed over: the router
// advertisements, Debug's detail lines and the server's own.
func TestParsePassesOverTheRest(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"RTR-SOLICIT(v0) 02:00:5e:00:53:8a",
		"RTR-ADVERT(v0) fd00:9::",
		"1251320881 client provides name: labhost",
		"1251320881 sent size:  4 option: 51 lease-time  12h",
		"1401163 available DHCP range: fd00:9::100 -- fd00:9::1ff",
		"DHCP, IP range 10.9.0.50 -- 10.9.0.99, lease time 12h",
		"DHCPACK(v0)",
		"started, version 2.92 cachesize 150",
	} {
		if e, ok := Parse(line); ok {
			t.Errorf("%q read as %+v", line, e)
		}
	}
}

func TestASearchReadsWhatTheTabShows(t *testing.T) {
	t.Parallel()
	e, _ := Parse("DHCPACK(eth1) 10.0.0.5 aa:bb:cc:dd:ee:ff laptop")
	for q, want := range map[string]bool{
		"ack laptop": true, "10.0.0.5": true, "aa:bb:cc": true, "eth1": true, "desk": true, "nak": false,
	} {
		if got := Matcher(logsearch.Parse(q), func(*Event) string { return "desk" })(&e); got != want {
			t.Errorf("%q: %v", q, got)
		}
	}
}

// A DHCPv6 client of the link-layer kinds is named by the address it
// carries, as a DHCPv4 one is by its MAC.
func TestClientMACReadsLinkLayerIdentifiers(t *testing.T) {
	t.Parallel()
	for duid, want := range map[string]string{
		"00:03:00:01:02:00:5e:00:53:8a":                      "02:00:5e:00:53:8a",
		"00:01:00:01:2c:3d:4e:5f:aa:bb:cc:dd:ee:ff":          "aa:bb:cc:dd:ee:ff",
		"00:02:00:00:ab:11:12:34":                            "",
		"00:04:12:34:56:78:9a:bc:de:f0:12:34:56:78:9a:bc:de": "",
		"nonsense": "",
	} {
		if got := (&Event{DUID: duid}).ClientMAC(); got != want {
			t.Errorf("%s: %q, want %q", duid, got, want)
		}
	}
	if got := (&Event{MAC: "aa:bb:cc:dd:ee:ff", DUID: "00:03:00:01:02:00:5e:00:53:8a"}).ClientMAC(); got != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("a MAC comes first: %s", got)
	}
}
