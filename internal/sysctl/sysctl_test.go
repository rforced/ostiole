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
	for _, k := range []string{"net/ipv4/ip_forward", "net/ipv4/tcp_syncookies", "vm/swappiness"} {
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
	// Tuning is applied alongside the forwarding settings, not only persisted.
	raw, _ = os.ReadFile(filepath.Join(root, "vm/swappiness"))
	if strings.TrimSpace(string(raw)) != "5" {
		t.Errorf("swappiness = %q", raw)
	}

	conf := filepath.Join(t.TempDir(), "sysctl.d", "99-ostiole.conf")
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
	if !strings.Contains(string(raw), "vm.swappiness = 5") || !strings.Contains(string(raw), "vm.vfs_cache_pressure = 50") {
		t.Errorf("tuning missing from persisted content:\n%s", raw)
	}
	// A kernel without fq_codel must not turn the drop-in into a boot error.
	if !strings.Contains(string(raw), "-net.core.default_qdisc = fq_codel") {
		t.Errorf("default_qdisc is not marked optional:\n%s", raw)
	}
}

// Ostiole only sets values that mean the same thing on a one-core virtual
// machine and on a large box. Capacity limits belong to the kernel, which
// sizes them from installed memory; a constant here would starve one end of
// the range or waste memory on the other.
func TestNoCapacityLimits(t *testing.T) {
	t.Parallel()
	for _, key := range []string{
		"fs/file-max",
		"fs/nr_open",
		"net/core/somaxconn",
		"net/netfilter/nf_conntrack_max",
		"net/netfilter/nf_conntrack_buckets",
	} {
		if _, ok := All()[key]; ok {
			t.Errorf("%s is memory-scaled by the kernel and must not be pinned to a constant", key)
		}
	}
}
