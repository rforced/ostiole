package server

import (
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

// A device is called by the name that resolves to it: the bare label in
// the local domain, like a lease, and the full name in any other, where
// the bare label answers nothing.
func TestDeviceNameIsTheOneThatResolves(t *testing.T) {
	t.Parallel()
	cases := []struct {
		h     model.HostOverride
		local string
		want  string
	}{
		{model.HostOverride{Hostname: "printer"}, "lan", "printer"},
		{model.HostOverride{Hostname: "printer", Domain: "LAN."}, "lan", "printer"},
		{model.HostOverride{Hostname: "printer"}, "", "printer"},
		{model.HostOverride{Hostname: "potato", Domain: "test"}, "lan", "potato.test"},
		{model.HostOverride{Hostname: "potato", Domain: "test"}, "", "potato.test"},
	}
	for _, tc := range cases {
		if got := deviceName(tc.h, tc.local); got != tc.want {
			t.Errorf("deviceName(%+v, %q) = %q, want %q", tc.h, tc.local, got, tc.want)
		}
	}
}
