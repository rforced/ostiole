package sysctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcApplyAndPersist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// Only some keys exist on this fake kernel; the rest must be skipped.
	for _, k := range []string{"net/ipv4/ip_forward", "net/ipv4/tcp_syncookies"} {
		p := filepath.Join(root, k)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := (Proc{Root: root}).Apply(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "net/ipv4/ip_forward"))
	if strings.TrimSpace(string(raw)) != "1" {
		t.Errorf("ip_forward = %q", raw)
	}

	conf := filepath.Join(t.TempDir(), "sysctl.d", "90-ostiole.conf")
	if err := Persist(conf); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(conf)
	if !strings.Contains(string(raw), "net.ipv4.ip_forward = 1") || !strings.Contains(string(raw), "net.ipv6.conf.all.forwarding = 1") {
		t.Errorf("persisted content:\n%s", raw)
	}
	if strings.Index(string(raw), "net.ipv4.conf.all") > strings.Index(string(raw), "net.ipv6") {
		t.Error("keys are not sorted")
	}
}
