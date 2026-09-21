// Package sysctl turns on the kernel settings a router needs and persists
// them for boot.
package sysctl

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Forwarding settings a router needs. Keys are /proc/sys paths relative
// to /proc/sys; values are what Ostiole sets.
var Forwarding = map[string]string{
	"net/ipv4/ip_forward":          "1",
	"net/ipv6/conf/all/forwarding": "1",
	// Reverse path filtering in loose mode: strict mode drops legitimate
	// asymmetric traffic on multi-homed routers.
	"net/ipv4/conf/all/rp_filter":     "2",
	"net/ipv4/conf/default/rp_filter": "2",
	// Do not accept ICMP redirects or source-routed packets on a router.
	"net/ipv4/conf/all/accept_redirects":     "0",
	"net/ipv4/conf/default/accept_redirects": "0",
	"net/ipv6/conf/all/accept_redirects":     "0",
	"net/ipv4/conf/all/send_redirects":       "0",
	"net/ipv4/conf/all/accept_source_route":  "0",
	"net/ipv6/conf/all/accept_source_route":  "0",
	"net/ipv4/tcp_syncookies":                "1",
}

// Tuning holds host settings that suit an appliance rather than a desktop.
//
// Every key here is scale-free on purpose: a policy or a ratio that means
// the same thing on a one-core virtual machine and on a 32-core router with
// 64 GB of memory. Capacity limits are deliberately absent, because something
// already sizes them from installed memory and does it better than any
// constant could:
//
//   - fs.file-max is raised to LONG_MAX by systemd at boot, not by the
//     kernel, so on a router running systemd any number written here would
//     be a reduction.
//   - net.netfilter.nf_conntrack_max scales with memory: 7680 on a 1 GB router
//     against 262144 on a 64 GB one. A fixed value either starves the large
//     router or hands tens of megabytes of the small one to the conntrack
//     table. The module is also usually unloaded when sysctls are applied.
//     A router that does run out can say so itself, which is Settings and
//     ConntrackMaxKey: an override somebody asked for, not a default.
//   - net.core.somaxconn has defaulted to 4096 since kernel 5.4, which is
//     below the kernel floor in internal/kernel, so it is always already set.
var Tuning = map[string]string{
	// Reclaim page cache before swapping. A router's working set is small
	// and paging a packet path back in adds latency where it hurts most.
	"vm/swappiness": "5",
	// Keep dentry and inode caches against that lower swappiness. This is a
	// ratio against page cache pressure, so it holds at any memory size.
	"vm/vfs_cache_pressure": "50",
	// Fight bufferbloat on forwarded traffic. Distributions disagree here:
	// systemd's own default is fq_codel, but cloud images often override it
	// with fq, which paces a host's own sockets rather than managing queues
	// of traffic passing through. A gateway wants the active queue
	// management.
	"net/core/default_qdisc": "fq_codel",
}

// Hardening closes the doors a router never needs open. Nothing here
// costs throughput or changes what the firewall does; each one takes
// away something an attacker with a foothold would reach for first.
var Hardening = map[string]string{
	// Hide kernel addresses from everybody, root included: a leaked
	// pointer is the first step of most kernel exploits.
	"kernel/kptr_restrict": "2",
	// Only root reads the kernel log, which is where those addresses and
	// the odd hardware secret end up.
	"kernel/dmesg_restrict": "1",
	// Nothing unprivileged on a router loads BPF programs. 1 rather than
	// 2 because it holds until reboot: a value nobody can quietly turn
	// back is the point.
	"kernel/unprivileged_bpf_disabled": "1",
	// Blind the JIT's constants against being sprayed into executable
	// memory, for the programs that do get loaded.
	"net/core/bpf_jit_harden": "2",
	// A process may only ptrace its own descendants, so a compromised
	// service cannot read the memory of another one running as the same
	// user.
	"kernel/yama/ptrace_scope": "1",
	// Do not answer a ping sent to a broadcast address; it is an
	// amplifier and nothing else.
	"net/ipv4/icmp_echo_ignore_broadcasts": "1",
}

// optional marks keys a kernel may not have. They are persisted with
// systemd's "-" prefix so a missing qdisc is not an error at boot.
var optional = map[string]bool{
	"net/core/default_qdisc": true,
	// Yama is a build option, and the JIT knob is only there with one.
	"kernel/yama/ptrace_scope": true,
	"net/core/bpf_jit_harden":  true,
}

// All returns every setting Ostiole manages, keyed by /proc/sys path.
func All() map[string]string {
	all := make(map[string]string, len(Forwarding)+len(Tuning)+len(Hardening))
	maps.Copy(all, Forwarding)
	maps.Copy(all, Tuning)
	maps.Copy(all, Hardening)
	return all
}

// ConfFile is where the settings are persisted for systemd-sysctl. The 99
// prefix matters: systemd-sysctl sorts drop-ins by file name across all of
// its directories and the last one wins, so a 90- file loses to a vendor
// drop-in like 90-vultr.conf. 99- is the conventional last word.
const ConfFile = "/etc/sysctl.d/99-ostiole.conf"

// legacyConfFile is where earlier releases wrote the file. Persist removes
// it so an upgraded router is not left with two copies disagreeing.
const legacyConfFile = "/etc/sysctl.d/90-ostiole.conf"

// Settings are the parts of the configuration the kernel knobs depend on.
// Everything else Ostiole sets is a constant, so this is all an apply has
// to carry down here.
type Settings struct {
	// ConntrackMax is the ceiling on connections the kernel tracks at
	// once. Zero leaves the kernel's own limit, which is the right answer
	// almost always; see ConntrackMaxKey.
	ConntrackMax int
}

// ConntrackMaxKey is the ceiling on tracked connections, and the one
// capacity limit Ostiole will write — but only when somebody has asked
// for a number, never as a default. Past the ceiling the kernel drops
// packets and says so once in dmesg, which is not a place anybody looks,
// so the number is worth being able to raise.
//
// It is absent from All() on purpose: the kernel sizes it from installed
// memory, from 7680 on a 1 GB router to 262144 on a 64 GB one, and a
// constant here would starve the large router or spend tens of megabytes
// of the small one. An override is a different thing from a default.
const ConntrackMaxKey = "net/netfilter/nf_conntrack_max"

// ConntrackBucketsKey sizes the hash table the tracked connections live
// in. It is not a setting of its own: the kernel sizes its own table for
// four entries per bucket, so raising the ceiling without it leaves the
// chains four times longer than the kernel intended, on the one path
// every forwarded packet takes.
const ConntrackBucketsKey = "net/netfilter/nf_conntrack_buckets"

// conntrackRatio is that four, kept here so the two keys cannot drift.
const conntrackRatio = 4

// Bounds on the ceiling. The floor is not a memory figure but a lockout
// one: a router that cannot open a few thousand connections cannot be
// reached to undo the setting. The ceiling is about 1.5 GB of kernel
// memory at roughly 350 bytes an entry.
const (
	MinConntrackMax = 16384
	MaxConntrackMax = 4_194_304
)

// Applier sets kernel parameters; the engine uses it after every apply.
type Applier interface {
	Apply(Settings) error
}

// Proc writes settings straight into /proc/sys. Root is the base path,
// overridden in tests.
type Proc struct {
	Root string
}

func (p Proc) root() string {
	if p.Root == "" {
		return "/proc/sys"
	}
	return p.Root
}

// Apply implements Applier. Missing keys (a kernel without IPv6, say) are
// skipped; other errors are reported together.
func (p Proc) Apply(s Settings) error {
	want := All()
	maps.Copy(want, conntrack(s))
	var errs []error
	for key, value := range want {
		path := filepath.Join(p.root(), key)
		err := os.WriteFile(path, []byte(value+"\n"), 0o644) //nolint:gosec // sysfs files, mode is ignored
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// conntrack renders the ceiling, or nothing when it is left to the kernel.
//
// These keys are deliberately not in the persisted drop-in. nf_conntrack
// is a module, and nothing has loaded it when systemd-sysctl runs at boot,
// so a line in sysctl.d would be skipped on the one boot it mattered. The
// engine applies these after the ruleset instead, which is the moment the
// module is guaranteed to be there, at boot and on every apply alike.
func conntrack(s Settings) map[string]string {
	if s.ConntrackMax <= 0 {
		return nil
	}
	return map[string]string{
		ConntrackMaxKey:     strconv.Itoa(s.ConntrackMax),
		ConntrackBucketsKey: strconv.Itoa(s.ConntrackMax / conntrackRatio),
	}
}

// ConntrackMax reads the ceiling the kernel is enforcing now, which is
// the denominator the connections page counts against. A kernel with the
// module unloaded has no such file and no connections to count either.
func (p Proc) ConntrackMax() (int, error) {
	raw, err := os.ReadFile(filepath.Join(p.root(), ConntrackMaxKey))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", ConntrackMaxKey, err)
	}
	return n, nil
}

// Content renders the persisted sysctl.d file.
func Content() string {
	var b strings.Builder
	b.WriteString("# Written by ostiole. Overwritten on install and on every apply.\n")
	section(&b, "# Settings a router needs.", Forwarding)
	section(&b, "# Appliance tuning. Capacity limits are left to the kernel,\n"+
		"# which sizes them from installed memory.", Tuning)
	section(&b, "# Hardening. Nothing here costs throughput.", Hardening)
	return b.String()
}

// section writes one commented group of settings, sorted for a stable file.
func section(b *strings.Builder, header string, m map[string]string) {
	fmt.Fprintf(b, "\n%s\n", header)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		prefix := ""
		if optional[k] {
			prefix = "-"
		}
		fmt.Fprintf(b, "%s%s = %s\n", prefix, strings.ReplaceAll(k, "/", "."), m[k])
	}
}

// Persist writes the sysctl.d file so the settings hold from boot.
func Persist(path string) error {
	if path == "" {
		path = ConfFile
		// Drop the file earlier releases wrote, so an upgraded router does not
		// keep a stale copy that a later drop-in could still win against.
		if err := os.Remove(legacyConfFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // system config dir
		return err
	}
	return os.WriteFile(path, []byte(Content()), 0o644) //nolint:gosec // world-readable like its siblings
}
