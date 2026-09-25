package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/wol"
)

// A wake goes to a machine on an inside interface of the configuration in
// force. A bad address, an interface that cannot carry a wake or is off,
// and one only in a draft are the caller's mistakes; a daemon that is not
// root says so; a viewer cannot send one.
func TestWake(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var sent []string
	var fail error
	srv := newTestServerWith(t, func(d *Deps) {
		tokens, err := auth.NewTokens(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d.Tokens = tokens
		d.Wake = func(iface string, mac net.HardwareAddr) error {
			mu.Lock()
			defer mu.Unlock()
			if fail != nil {
				return fail
			}
			sent = append(sent, iface+" "+mac.String())
			return nil
		}
	})
	failWith := func(err error) {
		mu.Lock()
		fail = err
		mu.Unlock()
	}
	wake := func(iface, mac string) (*http.Response, []byte) {
		return do(t, srv, http.MethodPost, "/api/v1/wol/wake", wakeRequest{Interface: iface, MAC: mac})
	}

	if resp, raw := wake("eth1", "aa:bb:cc:00:00:01"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("before an apply: %d %s", resp.StatusCode, raw)
	}
	cfg := starter()
	cfg.Interfaces = append(cfg.Interfaces,
		model.Interface{Name: "eth2", Zone: "lan", IPv4: model.IPv4{Mode: model.AddrNone}, IPv6: model.IPv6{Mode: model.AddrNone}})
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: cfg}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}

	if resp, raw := wake("eth1", "AA:BB:CC:00:00:01"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("wake: %d %s", resp.StatusCode, raw)
	}
	for _, tc := range []struct {
		iface, mac string
		status     int
		want       string
	}{
		{"eth1", "aa:bb:cc", http.StatusBadRequest, "not a MAC address"},
		{"eth1", "ff:ff:ff:ff:ff:ff", http.StatusBadRequest, "group address"},
		{"eth0", "aa:bb:cc:00:00:01", http.StatusBadRequest, "external zone"},
		{"eth2", "aa:bb:cc:00:00:01", http.StatusBadRequest, "is off"},
		{"eth9", "aa:bb:cc:00:00:01", http.StatusBadRequest, "not in the applied configuration"},
	} {
		resp, raw := wake(tc.iface, tc.mac)
		if resp.StatusCode != tc.status || !strings.Contains(string(raw), tc.want) {
			t.Errorf("%s %s: %d %s, want %d %q", tc.iface, tc.mac, resp.StatusCode, raw, tc.status, tc.want)
		}
	}
	mu.Lock()
	got := strings.Join(sent, ",")
	mu.Unlock()
	if got != "eth1 aa:bb:cc:00:00:01" {
		t.Errorf("sent = %q, want the one good wake", got)
	}

	for _, tc := range []struct {
		err    error
		status int
		want   string
	}{
		{fmt.Errorf("packet socket: %w", syscall.EPERM), http.StatusServiceUnavailable, "run as root"},
		{fmt.Errorf("send on eth1: %w", syscall.ENETDOWN), http.StatusBadRequest, "is down"},
		{fmt.Errorf("eth1: %w", wol.ErrNoLink), http.StatusBadRequest, "not on this router"},
	} {
		failWith(tc.err)
		resp, raw := wake("eth1", "aa:bb:cc:00:00:01")
		if resp.StatusCode != tc.status || !strings.Contains(string(raw), tc.want) {
			t.Errorf("%v: %d %s, want %d %q", tc.err, resp.StatusCode, raw, tc.status, tc.want)
		}
	}
	failWith(nil)

	body := wakeRequest{Interface: "eth1", MAC: "aa:bb:cc:00:00:02"}
	viewer := mintToken(t, srv, "look", string(auth.RoleViewer))
	if resp, raw := sendAs(t, srv, "/api/v1/wol/wake", viewer, body); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer: %d %s", resp.StatusCode, raw)
	}
	operator := mintToken(t, srv, "wake", string(auth.RoleOperator))
	if resp, raw := sendAs(t, srv, "/api/v1/wol/wake", operator, body); resp.StatusCode != http.StatusNoContent {
		t.Errorf("operator: %d %s", resp.StatusCode, raw)
	}
}

// dnsmasq's lease file does not say where a lease was handed out, so the
// configuration in force names the interface whose network holds it.
func TestLeasesNameTheirInterface(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ostiole.leases")
	file := "1900000000 aa:bb:cc:00:00:01 10.0.0.20 calcifer 01:aa:bb:cc:00:00:01\n" +
		"0 aa:bb:cc:00:00:02 192.168.77.5 * *\n" +
		"duid 00:01:00:01:2c:00:00:00:aa:bb:cc:00:00:03\n" +
		"1900000000 1234 fd00::20 howl 00:01:00:01:2c:00:00:00:aa:bb:cc:00:00:03\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newTestServerWith(t, func(d *Deps) { d.Services = &services.Dnsmasq{Leases: path} })
	read := func() map[string]string {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/dhcp/leases", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("leases: %d %s", resp.StatusCode, raw)
		}
		var rows []lease
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range rows {
			out[r.IP] = r.Interface
		}
		if len(out) != 3 {
			t.Fatalf("rows = %s", raw)
		}
		return out
	}
	if got := read(); got["10.0.0.20"] != "" {
		t.Errorf("named an interface before anything was applied: %v", got)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: starter()}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	got := read()
	for ip, want := range map[string]string{"10.0.0.20": "eth1", "192.168.77.5": "", "fd00::20": ""} {
		if got[ip] != want {
			t.Errorf("%s on %q, want %q", ip, got[ip], want)
		}
	}
}
