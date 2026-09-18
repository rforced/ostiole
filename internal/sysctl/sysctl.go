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

// Applier sets kernel parameters; the engine uses it after every apply.
type Applier interface {
	Apply() error
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
func (p Proc) Apply() error {
	var errs []error
	for key, value := range All() {
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
