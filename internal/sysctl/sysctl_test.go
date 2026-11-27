package sysctl

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
	if err := (Proc{Root: root}).Apply(Settings{}); err != nil {
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
	// Hardening is persisted with the rest, and the keys a kernel may be
	// built without are optional there too.
	for _, want := range []string{"kernel.kptr_restrict = 2", "kernel.dmesg_restrict = 1",
		"kernel.unprivileged_bpf_disabled = 1", "-kernel.yama.ptrace_scope = 2", "-net.core.bpf_jit_harden = 2",
		"-net.mptcp.enabled = 0", "net.ipv4.tcp_migrate_req = 0", "-kernel.kexec_load_disabled = 1",
		"-kernel.io_uring_disabled = 2", "-kernel.unprivileged_userns_clone = 0", "fs.suid_dumpable = 0"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("hardening %q missing from persisted content:\n%s", want, raw)
		}
	}
}

// The connection ceiling is written only when somebody has asked for a
// number, and it takes the hash table with it so the chains stay the
// length the kernel sizes its own for.
// The kernel sends a redirect when all or the interface says so, and
// reads an IPv6 interface's accept_redirects alone, so both are set on
// every interface, and the persisted pattern reaches those that come later.
func TestRedirectsAreOffOnEveryInterface(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	keys := []string{}
	for _, iface := range []string{"all", "default", "eth0", "eth0.10", "wg0"} {
		keys = append(keys, "net/ipv4/conf/"+iface+"/send_redirects", "net/ipv6/conf/"+iface+"/accept_redirects",
			"net/ipv4/conf/"+iface+"/accept_redirects",
			"net/ipv4/conf/"+iface+"/accept_source_route", "net/ipv6/conf/"+iface+"/accept_source_route")
	}
	for _, k := range keys {
		p := filepath.Join(root, k)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := (Proc{Root: root}).Apply(Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if raw, _ := os.ReadFile(filepath.Join(root, k)); strings.TrimSpace(string(raw)) != "0" {
			t.Errorf("%s = %q", k, raw)
		}
	}
	for _, want := range []string{"net.ipv4.conf.*.send_redirects = 0", "net.ipv6.conf.*.accept_redirects = 0",
		"net.ipv4.conf.*.accept_redirects = 0", "net.ipv4.conf.*.accept_source_route = 0",
		"net.ipv6.conf.*.accept_source_route = 0"} {
		if !strings.Contains(Content(), want+"\n") {
			t.Errorf("%q missing from the persisted file:\n%s", want, Content())
		}
	}
}

func TestConntrackMax(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, k := range []string{ConntrackMaxKey, ConntrackBucketsKey} {
		p := filepath.Join(root, k)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("262144\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proc := Proc{Root: root}

	// Zero is not a ceiling of zero: it leaves the kernel's own limit.
	if err := proc.Apply(Settings{}); err != nil {
		t.Fatal(err)
	}
	if got, err := proc.ConntrackMax(); err != nil || got != 262144 {
		t.Fatalf("ConntrackMax() = %d, %v; want the kernel's 262144 untouched", got, err)
	}

	if err := proc.Apply(Settings{ConntrackMax: 1_000_000}); err != nil {
		t.Fatal(err)
	}
	if got, err := proc.ConntrackMax(); err != nil || got != 1_000_000 {
		t.Fatalf("ConntrackMax() = %d, %v; want 1000000", got, err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, ConntrackBucketsKey))
	if strings.TrimSpace(string(raw)) != "250000" {
		t.Errorf("buckets = %q; want the ceiling over %d", raw, conntrackRatio)
	}
}

// Every connection gets counters and announces its end, whatever else is
// set: a connection only gets either if it opened while they were on.
func TestConntrackCountersAndEventsAreAlwaysOn(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, k := range []string{ConntrackAcctKey, ConntrackEventsKey, ConntrackMaxKey} {
		p := filepath.Join(root, k)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := (Proc{Root: root}).Apply(Settings{}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{ConntrackAcctKey: "1", ConntrackEventsKey: "1", ConntrackMaxKey: "0"} {
		if raw, _ := os.ReadFile(filepath.Join(root, key)); strings.TrimSpace(string(raw)) != want {
			t.Errorf("%s = %q, want %s", key, raw, want)
		}
	}
	// Not in the drop-in either: the module is not there when it is read.
	for _, key := range []string{ConntrackAcctKey, ConntrackEventsKey} {
		if _, ok := All()[key]; ok || strings.Contains(Content(), strings.ReplaceAll(key, "/", ".")) {
			t.Errorf("%s is persisted", key)
		}
	}
}

// What the kernel holds is read back exactly, so undoing an apply that
// raised the ceiling puts back the kernel's own figures, table size and
// all, rather than ones worked out from the ceiling.
func TestConntrackReadBackAndRestored(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for k, v := range map[string]string{ConntrackMaxKey: "65536", ConntrackBucketsKey: "65536", ConntrackTCPEstablishedKey: "432000"} {
		p := filepath.Join(root, k)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proc := Proc{Root: root}
	before, err := proc.Current()
	if err != nil || before != (Settings{ConntrackMax: 65536, ConntrackBuckets: 65536, TCPEstablished: 432000}) {
		t.Fatalf("Current() = %+v, %v", before, err)
	}
	if err := proc.Apply(Settings{ConntrackMax: 1_000_000}); err != nil {
		t.Fatal(err)
	}
	if got, err := proc.Current(); err != nil || got.TCPEstablished != DefaultTCPEstablished {
		t.Fatalf("an apply that names no idle time left %+v, %v; want a day", got, err)
	}
	if err := proc.Apply(before); err != nil {
		t.Fatal(err)
	}
	if got, err := proc.Current(); err != nil || got != before {
		t.Errorf("after putting it back: %+v, %v; want %+v", got, err, before)
	}
	if _, err := (Proc{Root: t.TempDir()}).Current(); err == nil {
		t.Error("a kernel without the module read as having a ceiling")
	}
}

// A kernel with the module unloaded has no ceiling to read, and the
// connections page has nothing to count against. That is not an error.
func TestConntrackMaxMissing(t *testing.T) {
	t.Parallel()
	proc := Proc{Root: t.TempDir()}
	if err := proc.Apply(Settings{ConntrackMax: 1_000_000}); err != nil {
		t.Errorf("Apply with no conntrack sysctls = %v; want it skipped", err)
	}
	if _, err := proc.ConntrackMax(); err == nil {
		t.Error("ConntrackMax() on a kernel without the module = nil error")
	}
}

// Ostiole only sets values that mean the same thing on a one-core virtual
// machine and on a large router. Capacity limits belong to the kernel, which
// sizes them from installed memory; a constant here would starve one end of
// the range or waste memory on the other. The connection ceiling is an
// override rather than a default, so it must not appear here or in the
// drop-in either: nothing has loaded nf_conntrack when systemd-sysctl runs.
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
		if dotted := strings.ReplaceAll(key, "/", "."); strings.Contains(Content(), dotted) {
			t.Errorf("%s is in the drop-in, where systemd-sysctl runs too early to write it", dotted)
		}
	}
}

// fakeKernel lays out /proc/sys files under a temporary root, each written
// in the past so that a write shows in its time.
func fakeKernel(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	past := time.Date(2020, 6, 1, 12, 0, 0, 0, time.UTC)
	for k, v := range files {
		p := filepath.Join(root, k)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func written(t *testing.T, root, key string) bool {
	t.Helper()
	fi, err := os.Stat(filepath.Join(root, key))
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime().Year() != 2020
}

// The hourly check reads; it writes nothing while every setting holds,
// and a table size the kernel rounded is no setting that moved.
func TestHealWritesNothingWhileTheSettingsHold(t *testing.T) {
	t.Parallel()
	files := map[string]string{ConntrackMaxKey: "1000000", ConntrackBucketsKey: "250368"}
	for k, v := range All() {
		files[strings.ReplaceAll(k, "*", "eth0")] = v
	}
	maps.Copy(files, map[string]string{ConntrackAcctKey: "1", ConntrackEventsKey: "1",
		ConntrackTCPLooseKey: "0", ConntrackTCPEstablishedKey: "86400"})
	maps.Copy(files, conntrackFixed)
	// As the kernel shows the pair.
	files["net/ipv4/ip_local_port_range"] = "10000\t60999"
	root := fakeKernel(t, files)
	moved, err := (Proc{Root: root}).Heal(Settings{ConntrackMax: 1_000_000, Configured: true})
	if err != nil || len(moved) != 0 {
		t.Fatalf("Heal = %+v, %v; want nothing moved", moved, err)
	}
	for k := range files {
		if written(t, root, k) {
			t.Errorf("%s was written though it held", k)
		}
	}
}

// Only what moved is written back, an interface's own setting under a
// pattern included, and a ceiling that moved takes its table size along.
func TestHealPutsBackOnlyWhatMoved(t *testing.T) {
	t.Parallel()
	root := fakeKernel(t, map[string]string{
		"net/ipv4/ip_forward":                  "0",
		"vm/swappiness":                        "5",
		"net/mptcp/enabled":                    "1",
		"net/ipv4/conf/all/send_redirects":     "0",
		"net/ipv4/conf/eth0.10/send_redirects": "1",
		ConntrackAcctKey:                       "1",
		ConntrackMaxKey:                        "262144",
		ConntrackBucketsKey:                    "65536",
	})
	moved, err := (Proc{Root: root}).Heal(Settings{ConntrackMax: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	want := []Drift{
		{Key: "net/ipv4/conf/eth0.10/send_redirects", Found: "1", Want: "0"},
		{Key: "net/ipv4/ip_forward", Found: "0", Want: "1"},
		{Key: "net/mptcp/enabled", Found: "1", Want: "0"},
		{Key: ConntrackMaxKey, Found: "262144", Want: "1000000"},
	}
	if !slices.Equal(moved, want) {
		t.Errorf("moved = %+v\nwant %+v", moved, want)
	}
	for key, value := range map[string]string{"net/ipv4/ip_forward": "1", "net/mptcp/enabled": "0",
		"net/ipv4/conf/eth0.10/send_redirects": "0", ConntrackMaxKey: "1000000", ConntrackBucketsKey: "250000"} {
		if raw, _ := os.ReadFile(filepath.Join(root, key)); strings.TrimSpace(string(raw)) != value {
			t.Errorf("%s = %q, want %s", key, raw, value)
		}
	}
	for _, key := range []string{"vm/swappiness", "net/ipv4/conf/all/send_redirects", ConntrackAcctKey} {
		if written(t, root, key) {
			t.Errorf("%s was written though it held", key)
		}
	}
}

// A setting the kernel will not take back is reported by its key, the
// others still go back. The kernel's own release file refuses every
// writer, root too.
func TestHealSaysWhatItCouldNotPutBack(t *testing.T) {
	t.Parallel()
	root := fakeKernel(t, map[string]string{"net/ipv4/ip_forward": "0"})
	if err := os.MkdirAll(filepath.Join(root, "net/mptcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/sys/kernel/osrelease", filepath.Join(root, "net/mptcp/enabled")); err != nil {
		t.Fatal(err)
	}
	moved, err := (Proc{Root: root}).Heal(Settings{})
	if err == nil || !strings.HasPrefix(err.Error(), "net/mptcp/enabled: ") || strings.Contains(err.Error(), "/proc/sys") {
		t.Errorf("err = %v, want one naming net/mptcp/enabled alone", err)
	}
	if len(moved) != 2 {
		t.Errorf("moved = %+v, want ip_forward and mptcp", moved)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "net/ipv4/ip_forward")); strings.TrimSpace(string(raw)) != "1" {
		t.Errorf("ip_forward = %q, want it put back beside the failure", raw)
	}
}

// A unicast packet in a broadcast frame is dropped on every interface,
// and a kernel or a container without the neighbour defaults and the
// receive queue's sizes must not turn the drop-in into a boot error.
func TestLinkSettingsReachEveryInterfaceAndOptionalKeysAreMarked(t *testing.T) {
	t.Parallel()
	files := map[string]string{}
	for _, iface := range []string{"all", "default", "eth0", "ppp0"} {
		files["net/ipv4/conf/"+iface+"/drop_unicast_in_l2_multicast"] = "0"
		files["net/ipv6/conf/"+iface+"/drop_unicast_in_l2_multicast"] = "0"
	}
	root := fakeKernel(t, files)
	if err := (Proc{Root: root}).Apply(Settings{}); err != nil {
		t.Fatal(err)
	}
	for k := range files {
		if raw, _ := os.ReadFile(filepath.Join(root, k)); strings.TrimSpace(string(raw)) != "1" {
			t.Errorf("%s = %q, want 1", k, raw)
		}
	}
	for key := range Tuning {
		if !strings.Contains(key, "/neigh/default/") && !strings.HasPrefix(key, "net/core/netdev_") {
			continue
		}
		if line := "\n-" + strings.ReplaceAll(key, "/", ".") + " = "; !strings.Contains(Content(), line) {
			t.Errorf("%s is not optional in the persisted file, and only the first network namespace has it", key)
		}
	}
}

// The idle time of an established connection is a day unless the
// configuration names one, the closing states are held to a minute, and
// connections are tracked from their handshake only once a configuration
// is in force: an unconfigured router leaves that as the kernel has it.
func TestConntrackTimeoutsAndTheHandshakeRule(t *testing.T) {
	t.Parallel()
	kernel := map[string]string{
		ConntrackTCPEstablishedKey:                          "432000",
		ConntrackTCPLooseKey:                                "1",
		"net/netfilter/nf_conntrack_tcp_timeout_fin_wait":   "120",
		"net/netfilter/nf_conntrack_tcp_timeout_close_wait": "10",
	}
	for _, tc := range []struct {
		name string
		s    Settings
		want map[string]string
	}{
		{"unconfigured", Settings{}, map[string]string{ConntrackTCPEstablishedKey: "86400", ConntrackTCPLooseKey: "1"}},
		{"configured", Settings{Configured: true}, map[string]string{ConntrackTCPEstablishedKey: "86400", ConntrackTCPLooseKey: "0"}},
		{"configured overrides tcpLoose", Settings{Configured: true, TCPLoose: "1"}, map[string]string{ConntrackTCPEstablishedKey: "86400", ConntrackTCPLooseKey: "0"}},
		{"a set idle time", Settings{Configured: true, TCPEstablished: 18000}, map[string]string{ConntrackTCPEstablishedKey: "18000", ConntrackTCPLooseKey: "0"}},
	} {
		root := fakeKernel(t, kernel)
		if err := (Proc{Root: root}).Apply(tc.s); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		maps.Copy(tc.want, conntrackFixed)
		for key, want := range tc.want {
			if raw, _ := os.ReadFile(filepath.Join(root, key)); strings.TrimSpace(string(raw)) != want {
				t.Errorf("%s: %s = %q, want %s", tc.name, key, raw, want)
			}
		}
		if tc.name == "unconfigured" && written(t, root, ConntrackTCPLooseKey) {
			t.Error("an unconfigured router wrote the handshake rule")
		}
	}
	// Undoing a first apply puts back the rule read before it.
	strict := maps.Clone(kernel)
	strict[ConntrackTCPLooseKey] = "0"
	strict[ConntrackMaxKey], strict[ConntrackBucketsKey] = "65536", "65536"
	root := fakeKernel(t, strict)
	if err := (Proc{Root: root}).Apply(Settings{TCPLoose: "1"}); err != nil {
		t.Fatal(err)
	}
	if now, err := (Proc{Root: root}).Current(); err != nil || now.TCPLoose != "1" {
		t.Errorf("after putting the rule back the kernel holds %+v, %v; want TCPLoose 1", now, err)
	}
	// None of it is in the drop-in: the module is not there when it is read.
	if strings.Contains(Content(), "nf_conntrack") {
		t.Errorf("a conntrack key is persisted:\n%s", Content())
	}
}

// The hourly check puts back an idle time and a handshake rule something
// moved, and leaves the handshake rule alone on a router with no
// configuration.
func TestHealWatchesTheConntrackTimeouts(t *testing.T) {
	t.Parallel()
	kernel := map[string]string{ConntrackTCPEstablishedKey: "432000", ConntrackTCPLooseKey: "1"}
	root := fakeKernel(t, kernel)
	moved, err := (Proc{Root: root}).Heal(Settings{Configured: true, TCPEstablished: 18000})
	if err != nil {
		t.Fatal(err)
	}
	want := []Drift{
		{Key: ConntrackTCPLooseKey, Found: "1", Want: "0"},
		{Key: ConntrackTCPEstablishedKey, Found: "432000", Want: "18000"},
	}
	if !slices.Equal(moved, want) {
		t.Errorf("moved = %+v\nwant %+v", moved, want)
	}
	for key, value := range map[string]string{ConntrackTCPLooseKey: "0", ConntrackTCPEstablishedKey: "18000"} {
		if raw, _ := os.ReadFile(filepath.Join(root, key)); strings.TrimSpace(string(raw)) != value {
			t.Errorf("%s = %q, want %s", key, raw, value)
		}
	}

	root = fakeKernel(t, map[string]string{ConntrackTCPEstablishedKey: "86400", ConntrackTCPLooseKey: "1"})
	if moved, err := (Proc{Root: root}).Heal(Settings{}); err != nil || len(moved) != 0 {
		t.Errorf("unconfigured: Heal = %+v, %v; want the handshake rule left alone", moved, err)
	}
	if written(t, root, ConntrackTCPLooseKey) {
		t.Error("unconfigured: the handshake rule was written")
	}
}

// The kernel shows a port range with a tab between its ends. That is the
// range as written, not one that moved, and a range that did move goes back.
func TestHealReadsAPortRangeAsTheKernelShowsIt(t *testing.T) {
	t.Parallel()
	const key = "net/ipv4/ip_local_port_range"
	root := fakeKernel(t, map[string]string{key: "10000\t60999"})
	if moved, err := (Proc{Root: root}).Heal(Settings{}); err != nil || len(moved) != 0 {
		t.Fatalf("Heal = %+v, %v; want nothing moved", moved, err)
	}
	if written(t, root, key) {
		t.Error("the range was written though it held")
	}
	root = fakeKernel(t, map[string]string{key: "32768\t60999"})
	moved, err := (Proc{Root: root}).Heal(Settings{})
	if err != nil || !slices.Equal(moved, []Drift{{Key: key, Found: "32768 60999", Want: "10000 60999"}}) {
		t.Fatalf("Heal = %+v, %v; want the kernel's own range reported", moved, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, key)); strings.TrimSpace(string(raw)) != "10000 60999" {
		t.Errorf("%s = %q", key, raw)
	}
}
