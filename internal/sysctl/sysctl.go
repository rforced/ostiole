// Package sysctl turns on the kernel settings a router needs and persists
// them for boot.
package sysctl

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ostiole/internal/atomicfile"
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
	// Do not accept ICMP redirects or source-routed packets on a router,
	// nor send redirects. The kernel sends one when all or the interface
	// says so, and IPv6 reads accept_redirects of the interface alone, so
	// these are set on every interface, those that appear later too:
	// systemd-sysctl applies a pattern to each as it comes. A pattern
	// takes in all and default.
	"net/ipv4/conf/*/accept_redirects":    "0",
	"net/ipv6/conf/*/accept_redirects":    "0",
	"net/ipv4/conf/*/accept_source_route": "0",
	"net/ipv6/conf/*/accept_source_route": "0",
	"net/ipv4/conf/*/send_redirects":      "0",
	// Secure redirects only narrow which redirects are taken, and none
	// are, so this is for the reader of the file as much as the kernel.
	"net/ipv4/conf/*/secure_redirects": "0",
	"net/ipv4/tcp_syncookies":          "1",
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
//
// The neighbour table's thresholds and the receive queue's sizes are fixed
// numbers all the same. They are not sized from memory by anyone: the
// kernel's own are constants too, set for a host with a handful of
// neighbours, and a router with a few hundred devices or a busy line runs
// into them.
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
	// Share an equal-tier gateway group by connection, not by address
	// pair: every connection of two hosts otherwise takes one line.
	"net/ipv4/fib_multipath_hash_policy": "1",
	"net/ipv6/fib_multipath_hash_policy": "1",
	// The neighbour table is not collected below the first, is collected
	// hard above the second and refuses entries above the third. The
	// kernel's 128, 512 and 1024 are spent by a few hundred devices with
	// both families, and "neighbour table overflow" drops packets.
	"net/ipv4/neigh/default/gc_thresh1": "512",
	"net/ipv4/neigh/default/gc_thresh2": "2048",
	"net/ipv4/neigh/default/gc_thresh3": "4096",
	"net/ipv6/neigh/default/gc_thresh1": "512",
	"net/ipv6/neigh/default/gc_thresh2": "2048",
	"net/ipv6/neigh/default/gc_thresh3": "4096",
	// Packets a CPU may have waiting, and how many and for how long one
	// receive pass takes them before giving the CPU back. The kernel's
	// time is two ticks, 8000 µs at Debian's HZ of 250, so 8000 is a floor
	// on every supported distribution.
	"net/core/netdev_max_backlog":  "4096",
	"net/core/netdev_budget":       "600",
	"net/core/netdev_budget_usecs": "8000",
	// The router's own outgoing connections start above every port it
	// listens on by default, the web UI's 9443 included. The kernel shows
	// the pair with a tab between.
	"net/ipv4/ip_local_port_range": "10000 65535",
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
	// Do not answer a ping sent to a broadcast address; it is an
	// amplifier and nothing else.
	"net/ipv4/icmp_echo_ignore_broadcasts": "1",
	// The kernel's defaults, held against a distribution that changes
	// them: no kernel log line for the bogus ICMP errors some routers
	// send in answer to broadcasts, and no moving a half-open connection
	// to another listener (CVE-2026-98432's second path).
	"net/ipv4/icmp_ignore_bogus_error_responses": "1",
	"net/ipv4/tcp_migrate_req":                   "0",
	// A reset cannot end a connection that is waiting out TIME-WAIT
	// (RFC 1337). Only the router's own connections have that state.
	"net/ipv4/tcp_rfc1337": "1",
	// Go has listened with multipath TCP by default since 1.24, which
	// puts the UI and the proxy behind code no router needs; with it off
	// Go listens with plain TCP. Forwarded multipath connections are
	// plain TCP to this router and are not touched.
	"net/mptcp/enabled": "0",
	// Nothing replaces the running kernel without a reboot: no kexec,
	// and with it no kdump, whose dump would hold all of memory. The
	// install script removes kdump, which would fail at every boot. Like
	// the BPF switch, it cannot be turned back until the next boot.
	"kernel/kexec_load_disabled": "1",
	// No magic SysRq from a keyboard or a serial console's break; root's
	// /proc/sysrq-trigger still works.
	"kernel/sysrq": "0",
	// Only root profiles, on kernels that know 3; elsewhere it acts as 2.
	"kernel/perf_event_paranoid": "3",
	// Only a process with CAP_SYS_PTRACE attaches, so root keeps strace.
	"kernel/yama/ptrace_scope": "2",
	// Only root loads a line discipline (PPPoE is a socket, not one) or
	// pushes keystrokes into another terminal.
	"dev/tty/ldisc_autoload": "0",
	"dev/tty/legacy_tiocsti": "0",
	// Only root uses userfaultfd, which exploits use to stall the kernel
	// in the middle of a copy.
	"vm/unprivileged_userfaultfd": "0",
	// No io_uring: nothing a router runs uses it, and it has carried a
	// large share of the kernel's exploitable bugs.
	"kernel/io_uring_disabled": "2",
	// Debian's and Ubuntu's kernels can refuse user namespaces to
	// everyone but root; the others cannot without refusing root too.
	// Every unit Ostiole sandboxes refuses them on its own.
	"kernel/unprivileged_userns_clone": "0",
	// No creating open of somebody else's file or FIFO in a world- or
	// group-writable sticky directory such as /tmp, unless the directory's
	// owner owns it.
	"fs/protected_fifos":   "2",
	"fs/protected_regular": "2",
	// The kernel's and every distribution's defaults, held against
	// something that moves them: no following a link in such a directory
	// to somebody else's file, no hard link to a file one cannot read or
	// write, and addresses shuffled for stack, libraries and heap.
	"fs/protected_hardlinks":    "1",
	"fs/protected_symlinks":     "1",
	"kernel/randomize_va_space": "2",
	// A unicast packet in a broadcast or multicast frame was sent to
	// every host on the link to reach one that should not have had it.
	"net/ipv4/conf/*/drop_unicast_in_l2_multicast": "1",
	"net/ipv6/conf/*/drop_unicast_in_l2_multicast": "1",
	// A process that changed its credentials leaves no core: the
	// resolvers' would hold the network's DNS names.
	"fs/suid_dumpable": "0",
}

// optional marks keys a kernel may not have. They are persisted with
// systemd's "-" prefix so a missing qdisc is not an error at boot.
var optional = map[string]bool{
	"net/core/default_qdisc": true,
	// Yama is a build option, and the JIT knob is only there with one.
	"kernel/yama/ptrace_scope": true,
	"net/core/bpf_jit_harden":  true,
	// So are multipath TCP, kexec, SysRq, perf, the legacy TIOCSTI
	// switch, userfaultfd and io_uring; the user namespace switch is a
	// Debian patch.
	"net/mptcp/enabled":                true,
	"kernel/kexec_load_disabled":       true,
	"kernel/sysrq":                     true,
	"kernel/perf_event_paranoid":       true,
	"dev/tty/legacy_tiocsti":           true,
	"vm/unprivileged_userfaultfd":      true,
	"kernel/io_uring_disabled":         true,
	"kernel/unprivileged_userns_clone": true,
	// Only the first network namespace has the neighbour defaults and the
	// receive queue's sizes; a container's has neither.
	"net/ipv4/neigh/default/gc_thresh1": true,
	"net/ipv4/neigh/default/gc_thresh2": true,
	"net/ipv4/neigh/default/gc_thresh3": true,
	"net/ipv6/neigh/default/gc_thresh1": true,
	"net/ipv6/neigh/default/gc_thresh2": true,
	"net/ipv6/neigh/default/gc_thresh3": true,
	"net/core/netdev_max_backlog":       true,
	"net/core/netdev_budget":            true,
	"net/core/netdev_budget_usecs":      true,
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
	ConntrackMax int `json:"conntrackMax"`
	// ConntrackBuckets sizes the table they live in. Zero sizes it from
	// the ceiling; a number is only ever one read back from the kernel,
	// to be put back.
	ConntrackBuckets int `json:"conntrackBuckets"`
	// TCPEstablished is how many seconds an established TCP connection
	// may carry nothing before the kernel forgets it. Zero is
	// DefaultTCPEstablished.
	TCPEstablished int `json:"tcpEstablished,omitempty"`
	// Configured says a configuration is in force or being applied. Only
	// then is picking up TCP connections midway turned off: a router
	// still on its bootstrap ruleset has just loaded connection tracking
	// under the session that installs it, which has to be picked up.
	Configured bool `json:"-"`
	// TCPLoose is the pickup rule read back before an apply, "0" or "1",
	// put back when undoing it leaves the router unconfigured. Empty
	// leaves the kernel's.
	TCPLoose string `json:"tcpLoose,omitempty"`
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

// ConntrackAcctKey gives each connection byte and packet counters, which
// Traffic counts devices by and the Connections page sorts by. A
// connection gets them only if it opened while this was on, so it is
// always on rather than following the setting that uses it.
const ConntrackAcctKey = "net/netfilter/nf_conntrack_acct"

// ConntrackEventsKey at 1 announces every connection's end, where the
// kernel's own 2 announces only those that opened while somebody listened:
// at 1 a restarted daemon still hears the last bytes of what was open.
const ConntrackEventsKey = "net/netfilter/nf_conntrack_events"

// ConntrackTCPLooseKey at 0 has the kernel track a TCP connection only from
// its handshake. At the kernel's 1 any packet starts one, so a connection
// whose entry timed out or was removed comes back without ever being held
// to the rules a new connection meets.
const ConntrackTCPLooseKey = "net/netfilter/nf_conntrack_tcp_loose"

// ConntrackTCPEstablishedKey is how long an idle established TCP
// connection is kept.
const ConntrackTCPEstablishedKey = "net/netfilter/nf_conntrack_tcp_timeout_established"

// The idle time of an established TCP connection, in seconds. The default
// is a day, as pf keeps one, where the kernel keeps five. The floor is
// the 2 hours 4 minutes of RFC 5382: under it a connection that sends
// nothing but TCP's keepalives, two hours apart by default, is forgotten
// between two of them.
const (
	DefaultTCPEstablished = 86400
	MinTCPEstablished     = 7440
	MaxTCPEstablished     = 30 * 86400
)

// conntrackFixed are the TCP timeouts that are no setting: a connection
// one end has closed, or both have, is kept a minute, where the kernel
// keeps the first two.
var conntrackFixed = map[string]string{
	"net/netfilter/nf_conntrack_tcp_timeout_fin_wait":   "60",
	"net/netfilter/nf_conntrack_tcp_timeout_close_wait": "60",
}

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

// Reader is an Applier that can say what the kernel holds now, which is
// what undoing an apply puts back: the kernel keeps a raised ceiling until
// something lowers it.
type Reader interface {
	Current() (Settings, error)
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
// skipped; other errors are reported together. A key with a * is written
// wherever it matches.
func (p Proc) Apply(s Settings) error {
	want := All()
	maps.Copy(want, conntrack(s))
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(want)) {
		for _, path := range p.paths(key) {
			if err := p.write(path, want[key]); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Drift is a setting the kernel no longer holds as Ostiole wrote it.
type Drift struct {
	// Key is the /proc/sys path, an interface's own for a pattern.
	Key   string
	Found string
	Want  string
}

// Healer is an Applier that can put back only what moved.
type Healer interface {
	Heal(Settings) ([]Drift, error)
}

// Heal reads every setting Apply writes and writes back only those the
// kernel no longer holds, so a router whose settings held is left
// untouched. Keys the kernel lacks are skipped, as Apply skips them. The
// table size is not compared, since the kernel rounds what it is given;
// it is written again with a ceiling that moved.
func (p Proc) Heal(s Settings) ([]Drift, error) {
	want := All()
	tracked := conntrack(s)
	maps.Copy(want, tracked)
	delete(want, ConntrackBucketsKey)
	var moved []Drift
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(want)) {
		for _, path := range p.paths(key) {
			raw, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			rel := strings.TrimPrefix(path, p.root()+"/")
			if err != nil {
				errs = append(errs, keyErr(rel, err))
				continue
			}
			// The kernel puts a tab between the two ports of a range.
			found := strings.Join(strings.Fields(string(raw)), " ")
			if found == want[key] {
				continue
			}
			moved = append(moved, Drift{Key: rel, Found: found, Want: want[key]})
			if err := p.write(path, want[key]); err != nil {
				errs = append(errs, err)
			}
			if key == ConntrackMaxKey {
				if err := p.write(filepath.Join(p.root(), ConntrackBucketsKey), tracked[ConntrackBucketsKey]); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return moved, errors.Join(errs...)
}

// paths are the files a key names, every match for a pattern.
func (p Proc) paths(key string) []string {
	path := filepath.Join(p.root(), key)
	if !strings.Contains(key, "*") {
		return []string{path}
	}
	matches, _ := filepath.Glob(path)
	return matches
}

// write sets one file. One that is not there, a key this kernel lacks or
// an interface gone since the glob, is no error.
func (p Proc) write(path, value string) error {
	err := os.WriteFile(path, []byte(value+"\n"), 0o644) //nolint:gosec // sysfs files, mode is ignored
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return keyErr(strings.TrimPrefix(path, p.root()+"/"), err)
}

// keyErr names the key a read or write failed on, without the path the
// error already carries.
func keyErr(key string, err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		err = pe.Err
	}
	return fmt.Errorf("%s: %w", key, err)
}

// conntrack renders the accounting, the events and the TCP timeouts, the
// handshake rule once a configuration is in force, and the ceiling when
// one was asked for rather than left to the kernel.
//
// These keys are deliberately not in the persisted drop-in. nf_conntrack
// is a module, and nothing has loaded it when systemd-sysctl runs at boot,
// so a line in sysctl.d would be skipped on the one boot it mattered. The
// engine applies these after the ruleset instead, which is the moment the
// module is guaranteed to be there, at boot and on every apply alike.
func conntrack(s Settings) map[string]string {
	out := map[string]string{ConntrackAcctKey: "1", ConntrackEventsKey: "1"}
	maps.Copy(out, conntrackFixed)
	established := s.TCPEstablished
	if established <= 0 {
		established = DefaultTCPEstablished
	}
	out[ConntrackTCPEstablishedKey] = strconv.Itoa(established)
	switch {
	case s.Configured:
		out[ConntrackTCPLooseKey] = "0"
	case s.TCPLoose != "":
		out[ConntrackTCPLooseKey] = s.TCPLoose
	}
	if s.ConntrackMax <= 0 {
		return out
	}
	buckets := s.ConntrackBuckets
	if buckets <= 0 {
		buckets = s.ConntrackMax / conntrackRatio
	}
	out[ConntrackMaxKey] = strconv.Itoa(s.ConntrackMax)
	out[ConntrackBucketsKey] = strconv.Itoa(buckets)
	return out
}

// ConntrackMax reads the ceiling the kernel is enforcing now, which is
// the denominator the connections page counts against. A kernel with the
// module unloaded has no such file and no connections to count either.
func (p Proc) ConntrackMax() (int, error) {
	return p.read(ConntrackMaxKey)
}

// Current implements Reader: the ceiling, the table size, the idle time
// and the pickup rule in force.
func (p Proc) Current() (Settings, error) {
	ceiling, err := p.read(ConntrackMaxKey)
	if err != nil {
		return Settings{}, err
	}
	buckets, err := p.read(ConntrackBucketsKey)
	if err != nil {
		return Settings{}, err
	}
	now := Settings{ConntrackMax: ceiling, ConntrackBuckets: buckets}
	now.TCPEstablished, _ = p.read(ConntrackTCPEstablishedKey)
	if loose, err := p.read(ConntrackTCPLooseKey); err == nil {
		now.TCPLoose = strconv.Itoa(loose)
	}
	return now, nil
}

func (p Proc) read(key string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(p.root(), key))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

// Content renders the persisted sysctl.d file.
func Content() string {
	var b strings.Builder
	b.WriteString("# Written by ostiole. Overwritten on install and on every update.\n")
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
	return atomicfile.Write(path, []byte(Content()), 0o644)
}
