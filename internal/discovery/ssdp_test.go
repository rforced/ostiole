package discovery

import (
	"strings"
	"testing"
)

func crlf(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n") + "\r\n\r\n")
}

func TestParseSSDPReadsWhatDevicesSend(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		payload []byte
		want    SSDP
	}{
		"home assistant search": {
			payload: crlf("M-SEARCH * HTTP/1.1", "HOST: 239.255.255.250:1900", `MAN: "ssdp:discover"`, "MX: 4", "ST: ssdp:all"),
			want:    SSDP{Kind: KindSearch, ST: "ssdp:all", MX: 4},
		},
		"search with MAN unquoted": {
			payload: crlf("M-SEARCH * HTTP/1.1", "MAN: ssdp:discover", "ST: upnp:rootdevice"),
			want:    SSDP{Kind: KindSearch, ST: "upnp:rootdevice"},
		},
		"reply": {
			payload: crlf("HTTP/1.1 200 OK", "CACHE-CONTROL: max-age = 1800", "EXT:", "LOCATION: http://192.0.2.10:49152/desc.xml",
				"SERVER: Linux/6.1 UPnP/1.0 Quillbox/2.3", "ST: urn:schemas-upnp-org:device:MediaRenderer:1",
				"USN: uuid:0b1d2e3f::urn:schemas-upnp-org:device:MediaRenderer:1"),
			want: SSDP{Kind: KindReply, ST: "urn:schemas-upnp-org:device:MediaRenderer:1",
				USN: "uuid:0b1d2e3f::urn:schemas-upnp-org:device:MediaRenderer:1", Location: "http://192.0.2.10:49152/desc.xml",
				Server: "Linux/6.1 UPnP/1.0 Quillbox/2.3", MaxAge: 1800},
		},
		"alive": {
			payload: crlf("NOTIFY * HTTP/1.1", "HOST: 239.255.255.250:1900", "CACHE-CONTROL: max-age=120", "LOCATION: http://192.0.2.11/d.xml",
				"NT: upnp:rootdevice", "NTS: ssdp:alive", "USN: uuid:a1::upnp:rootdevice"),
			want: SSDP{Kind: KindAlive, NT: "upnp:rootdevice", USN: "uuid:a1::upnp:rootdevice", Location: "http://192.0.2.11/d.xml", MaxAge: 120},
		},
		"byebye": {
			payload: crlf("NOTIFY * HTTP/1.1", "NT: upnp:rootdevice", "NTS: ssdp:byebye", "USN: uuid:a1::upnp:rootdevice"),
			want:    SSDP{Kind: KindByebye, NT: "upnp:rootdevice", USN: "uuid:a1::upnp:rootdevice"},
		},
		"update": {
			payload: crlf("NOTIFY * HTTP/1.1", "NT: upnp:rootdevice", "NTS: ssdp:update", "USN: uuid:a1::upnp:rootdevice"),
			want:    SSDP{Kind: KindUpdate, NT: "upnp:rootdevice", USN: "uuid:a1::upnp:rootdevice"},
		},
		"lower-case header names": {
			payload: crlf("NOTIFY * HTTP/1.1", "nt: upnp:rootdevice", "nts: ssdp:alive", "usn: uuid:b2", "location: http://192.0.2.12/", "cache-control: max-age=60"),
			want:    SSDP{Kind: KindAlive, NT: "upnp:rootdevice", USN: "uuid:b2", Location: "http://192.0.2.12/", MaxAge: 60},
		},
		"LF line ends, no final blank line": {
			payload: []byte("M-SEARCH * HTTP/1.1\nMAN: \"ssdp:discover\"\nMX: 2\nST: ssdp:all"),
			want:    SSDP{Kind: KindSearch, ST: "ssdp:all", MX: 2},
		},
		"MX not a number": {
			payload: crlf("M-SEARCH * HTTP/1.1", `MAN: "ssdp:discover"`, "MX: abc", "ST: ssdp:all"),
			want:    SSDP{Kind: KindSearch, ST: "ssdp:all"},
		},
		"headers after the blank line ignored": {
			payload: []byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nST: ssdp:all\r\n\r\nST: upnp:rootdevice\r\nMX: 9\r\n"),
			want:    SSDP{Kind: KindSearch, ST: "ssdp:all"},
		},
	} {
		got, err := ParseSSDP(tc.payload)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if *got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, *got, tc.want)
		}
	}
}

func TestParseSSDPRefusesWhatIsNotSSDP(t *testing.T) {
	t.Parallel()
	for name, payload := range map[string][]byte{
		"GET request":            crlf("GET / HTTP/1.1", "Host: 192.0.2.1"),
		"truncated start line":   []byte("M-SEARCH * HT"),
		"search without MAN":     crlf("M-SEARCH * HTTP/1.1", "ST: ssdp:all", "MX: 4"),
		"search with other MAN":  crlf("M-SEARCH * HTTP/1.1", `MAN: "ssdp:other"`, "ST: ssdp:all"),
		"NOTIFY without NTS":     crlf("NOTIFY * HTTP/1.1", "NT: upnp:rootdevice"),
		"NOTIFY with other NTS":  crlf("NOTIFY * HTTP/1.1", "NT: upnp:rootdevice", "NTS: ssdp:propchange"),
		"reply other than 200":   crlf("HTTP/1.1 404 Not Found", "ST: ssdp:all"),
		"MAN after a blank line": []byte("M-SEARCH * HTTP/1.1\r\n\r\nMAN: \"ssdp:discover\"\r\n"),
	} {
		if s, err := ParseSSDP(payload); err == nil {
			t.Errorf("%s: read as %+v", name, *s)
		}
	}
}

func TestParseSSDPSurvivesMalformedInput(t *testing.T) {
	t.Parallel()
	for _, payload := range malformedSSDP() {
		_, _ = ParseSSDP(payload)
	}
}

func FuzzParseSSDP(f *testing.F) {
	for _, payload := range malformedSSDP() {
		f.Add(payload)
	}
	f.Add(crlf("NOTIFY * HTTP/1.1", "NT: upnp:rootdevice", "NTS: ssdp:alive"))
	f.Fuzz(func(_ *testing.T, payload []byte) {
		if s, err := ParseSSDP(payload); err == nil {
			_, _ = s.Name(), s.Type()
		}
	})
}

func malformedSSDP() [][]byte {
	return [][]byte{
		nil,
		{},
		[]byte("\r\n"),
		[]byte("\n\n\n"),
		[]byte("\x00\xff\xfe"),
		[]byte("M-SEARCH"),
		[]byte("M-SEARCH *"),
		[]byte("HTTP/1.1"),
		[]byte("HTTP/1.1 200"),
		[]byte("NOTIFY * HTTP/1.1\r\n:"),
		[]byte("NOTIFY * HTTP/1.1\r\nNTS"),
		[]byte("NOTIFY * HTTP/1.1\r\nNTS:"),
		[]byte("M-SEARCH * HTTP/1.1\r\nMAN: \""),
		[]byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nMX: 99999999999999999999999"),
		[]byte("M-SEARCH * HTTP/1.1\r\nMAN: \"ssdp:discover\"\r\nMX: -4"),
		[]byte("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age="),
		[]byte("HTTP/1.1 200 OK\r\nCACHE-CONTROL: =,=,max-age"),
		[]byte("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=\"\r\n"),
		[]byte(strings.Repeat("A", 9000)),
		[]byte("HTTP/1.1 200 OK\r\n" + strings.Repeat("X: y\r\n", 2000)),
		[]byte("HTTP/1.1 200 OK\r" + strings.Repeat("\r", 500)),
	}
}

func TestSSDPNameAndType(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		s         SSDP
		name, typ string
	}{
		"USN first":          {SSDP{Kind: KindReply, ST: "st", USN: "usn"}, "usn", "st"},
		"search falls to ST": {SSDP{Kind: KindSearch, ST: "ssdp:all"}, "ssdp:all", "ssdp:all"},
		"notify falls to NT": {SSDP{Kind: KindAlive, NT: "nt"}, "nt", "nt"},
		"byebye types by NT": {SSDP{Kind: KindByebye, NT: "nt", ST: "st", USN: "u"}, "u", "nt"},
		"update types by NT": {SSDP{Kind: KindUpdate, NT: "nt"}, "nt", "nt"},
		"reply types by ST":  {SSDP{Kind: KindReply, ST: "st", NT: "nt"}, "st", "st"},
	} {
		if got := tc.s.Name(); got != tc.name {
			t.Errorf("%s: Name %q, want %q", name, got, tc.name)
		}
		if got := tc.s.Type(); got != tc.typ {
			t.Errorf("%s: Type %q, want %q", name, got, tc.typ)
		}
	}
}
