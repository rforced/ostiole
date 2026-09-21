package cli

import (
	"net/url"
	"testing"
)

// The probe turns certificate verification off, so which hosts count as
// loopback is the whole of that decision's security boundary.
func TestLoopbackHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.53", true}, // all of 127/8, not just the one address
		{"::1", true},
		{"localhost", true},
		{"", false},
		{"0.0.0.0", false},
		{"10.2.96.3", false},
		{"example.com", false},
		{"localhost.example.com", false},
		{"2606:4700:4700::1111", false},
	} {
		if got := loopbackHost(tc.host); got != tc.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// The health URLs the installer writes have a port, and an IPv6 one would be
// bracketed; the check reads the host through url.Hostname so both forms
// arrive as a bare address.
func TestLoopbackHostFromHealthURL(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"https://127.0.0.1:443/api/v1/health", true},
		{"http://127.0.0.1:8080/api/v1/health", true},
		{"https://[::1]:443/api/v1/health", true},
		{"https://example.com:443/api/v1/health", false},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := loopbackHost(u.Hostname()); got != tc.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
