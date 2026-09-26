package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// Unbound paths and names.
const (
	UnboundUnit = "ostiole-unbound.service"
	// UnboundDir holds the generated configuration. It sits inside the
	// package's own directory so SELinux labels it the way unbound_t
	// expects, the lesson dnsmasq taught us.
	UnboundDir = "/etc/unbound"
	// UnboundAddress and UnboundPort are where unbound listens. dnsmasq
	// already owns 127.0.0.1:53, so the resolver takes another loopback
	// address instead of another port: SELinux only lets unbound_t bind
	// ports its policy knows as DNS, and a high port like 5335 is refused
	// with "can't bind socket: Permission denied" on an enforcing router.
	UnboundAddress = "127.0.0.53"
	UnboundPort    = 53
	// UnboundAnchor is the DNSSEC root trust anchor, kept up to date by
	// unbound itself (RFC 5011).
	UnboundAnchor    = "/var/lib/unbound/root.key"
	unboundConfName  = "ostiole.conf"
	unboundDistroSvc = "unbound.service"
)

// certBundles are where distributions keep the CA bundle used to verify
// DNS over TLS servers.
var certBundles = []string{
	"/etc/pki/tls/certs/ca-bundle.crt",       // RHEL family
	"/etc/ssl/certs/ca-certificates.crt",     // Debian and Arch
	"/var/lib/ca-certificates/ca-bundle.pem", // openSUSE
}

// Unbound generates and runs the validating resolver that sits behind
// dnsmasq. It implements network.Backend so the engine can apply and
// revert it with everything else.
type Unbound struct {
	// Dir holds the generated configuration; default UnboundDir.
	Dir string
	// Anchor is the DNSSEC trust anchor file; default UnboundAnchor.
	Anchor string
	// CertBundle verifies DNS over TLS servers; empty means detect.
	CertBundle string
	// Cmd runs systemctl; default execs it.
	Cmd network.Commander
}

// NewUnbound returns a backend with production defaults.
func NewUnbound() *Unbound {
	return &Unbound{Dir: UnboundDir, Anchor: UnboundAnchor, Cmd: execCommander{}}
}

func (u *Unbound) dir() string {
	if u.Dir == "" {
		return UnboundDir
	}
	return u.Dir
}

func (u *Unbound) anchor() string {
	if u.Anchor == "" {
		return UnboundAnchor
	}
	return u.Anchor
}

func (u *Unbound) certBundle() string {
	if u.CertBundle != "" {
		return u.CertBundle
	}
	for _, p := range certBundles {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return certBundles[0]
}

func (u *Unbound) cmd() network.Commander {
	if u.Cmd == nil {
		return execCommander{}
	}
	return u.Cmd
}

// Name implements network.Backend.
func (u *Unbound) Name() string { return "unbound" }

// ResolverEnabled reports whether the configuration wants unbound.
func ResolverEnabled(cfg *model.Config) bool {
	dns := cfg.Services.DNS
	return dns.Enabled && (dns.Resolver == model.ResolverRecursive || dns.Resolver == model.ResolverTLS)
}

// ConfPath is the generated configuration file.
func (u *Unbound) ConfPath() string { return filepath.Join(u.dir(), unboundConfName) }

// Render implements network.Backend. Without a validating resolver it
// returns no files, which Apply turns into "unit stopped".
func (u *Unbound) Render(cfg *model.Config) (network.Files, error) {
	files := network.Files{}
	if !ResolverEnabled(cfg) {
		return files, nil
	}
	files[unboundConfName] = u.render(cfg)
	return files, nil
}

func (u *Unbound) render(cfg *model.Config) string {
	dns := cfg.Services.DNS
	var b strings.Builder
	b.WriteString(fileHeader)
	b.WriteString("server:\n")
	fmt.Fprintf(&b, "    verbosity: %d\n", unboundVerbosity(cfg.System.Logging.EffectiveLevel()))
	// syslog rather than stderr, so each line carries a priority the
	// unit's journal cap can read. The unit's single -d keeps unbound in
	// the foreground and leaves this alone; a second -d would not, and
	// nor would a logfile line: naming one, even as "", turns syslog off.
	b.WriteString("    use-syslog: yes\n")
	// Who asked for what is never written down, at any level.
	b.WriteString("    log-queries: no\n    log-replies: no\n    log-servfail: no\n")
	fmt.Fprintf(&b, "    interface: %s@%d\n", UnboundAddress, UnboundPort)
	// It answers over IPv4 on the loopback but queries the internet over
	// both families.
	b.WriteString("    do-ip4: yes\n    do-ip6: yes\n    do-udp: yes\n    do-tcp: yes\n")
	// Only dnsmasq talks to it; everyone else is refused.
	b.WriteString("    access-control: 0.0.0.0/0 refuse\n")
	b.WriteString("    access-control: 127.0.0.0/8 allow\n")
	b.WriteString("    access-control: ::0/0 refuse\n")
	b.WriteString("    username: \"unbound\"\n")
	fmt.Fprintf(&b, "    directory: %q\n", u.dir())
	b.WriteString("    chroot: \"\"\n")
	b.WriteString("    hide-identity: yes\n    hide-version: yes\n    hide-trustanchor: yes\n")
	b.WriteString("    harden-glue: yes\n    harden-dnssec-stripped: yes\n    harden-below-nxdomain: yes\n")
	b.WriteString("    harden-algo-downgrade: yes\n    harden-large-queries: yes\n    harden-short-bufsize: yes\n")
	b.WriteString("    unwanted-reply-threshold: 10000\n    deny-any: yes\n    val-clean-additional: yes\n")
	// Randomised case in the question makes an off-path answer harder to
	// forge; minimal answers and QNAME minimisation keep each query and
	// each reply to what the question needs.
	b.WriteString("    use-caps-for-id: yes\n    minimal-responses: yes\n")
	b.WriteString("    qname-minimisation: yes\n    aggressive-nsec: yes\n    prefetch: yes\n")
	b.WriteString("    rrset-roundrobin: yes\n")
	b.WriteString("    edns-buffer-size: 1232\n")
	b.WriteString("    cache-min-ttl: 60\n")
	cache := dns.ResolverCache()
	fmt.Fprintf(&b, "    msg-cache-size: %dm\n", cache)
	fmt.Fprintf(&b, "    rrset-cache-size: %dm\n", cache*2)
	if !dns.Rebind.Off {
		// The recursive path refuses private answers on its own account,
		// so the protection holds even for a query dnsmasq passes
		// straight through. 100.64.0.0/10 is deliberately absent: tailnet
		// names resolve into it through public DNS.
		for _, p := range unboundPrivate {
			fmt.Fprintf(&b, "    private-address: %s\n", p)
		}
		for _, d := range cfg.RebindAllowed() {
			fmt.Fprintf(&b, "    private-domain: %q\n", d)
		}
	}
	fmt.Fprintf(&b, "    auto-trust-anchor-file: %q\n", u.anchor())
	if dns.Resolver == model.ResolverTLS {
		fmt.Fprintf(&b, "    tls-cert-bundle: %q\n", u.certBundle())
	}
	if dns.Resolver == model.ResolverRecursive {
		// RFC 8806: the root zone is transferred once and answered from
		// here, so no client query ever reaches a root server. The only
		// thing that leaves is the transfer itself, on the SOA refresh.
		b.WriteString("\nauth-zone:\n")
		b.WriteString("    name: \".\"\n")
		for _, p := range rootPrimaries {
			fmt.Fprintf(&b, "    primary: %s\n", p)
		}
		b.WriteString("    fallback-enabled: yes\n")
		b.WriteString("    for-downstream: no\n")
		b.WriteString("    for-upstream: yes\n")
		// Beside the trust anchor, which is the one directory unbound may
		// already write and which carries the label RFC 5011 needs.
		fmt.Fprintf(&b, "    zonefile: %q\n", filepath.Join(filepath.Dir(u.anchor()), "root.zone"))
	}
	if dns.Resolver == model.ResolverTLS {
		b.WriteString("\nforward-zone:\n")
		b.WriteString("    name: \".\"\n")
		b.WriteString("    forward-tls-upstream: yes\n")
		for _, up := range dns.TLSUpstreams {
			fmt.Fprintf(&b, "    forward-addr: %s@853#%s\n", up.Address, up.Hostname)
		}
	}
	return b.String()
}

// unboundVerbosity maps the router's log level onto unbound's scale.
// Above 2 unbound writes a line per query, which is not a level, it is a
// feature this router does not have.
func unboundVerbosity(l model.LogLevel) int {
	switch l {
	case model.LogInfo:
		return 1
	case model.LogDebug:
		return 2
	default:
		return 0
	}
}

// unboundPrivate are the addresses an upstream answer has no business
// carrying. Link-local is in the list: a name that resolves to fe80::
// reaches the browser's own segment just as well as an RFC 1918 one.
var unboundPrivate = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16",
	"fd00::/8", "fe80::/10", "::ffff:0:0/96",
}

// rootPrimaries are the root servers that answer a zone transfer of the
// root zone, both families, in letter order.
var rootPrimaries = []string{
	"199.9.14.201", "2001:500:200::b", // b.root-servers.net
	"192.33.4.12", "2001:500:2::c", // c.root-servers.net
	"199.7.91.13", "2001:500:2d::d", // d.root-servers.net
	"192.5.5.241", "2001:500:2f::f", // f.root-servers.net
	"192.112.36.4", "2001:500:12::d0d", // g.root-servers.net
	"193.0.14.129", "2001:7fd::1", // k.root-servers.net
	"192.0.47.132", "2620:0:2830:202::132", // xfr.cjr.dns.icann.org
	"192.0.32.132", "2620:0:2d0:202::132", // xfr.lax.dns.icann.org
}

// Snapshot implements network.Backend.
func (u *Unbound) Snapshot() (network.Files, error) {
	files := network.Files{}
	raw, err := os.ReadFile(u.ConfPath())
	switch {
	case err == nil:
		files[unboundConfName] = string(raw)
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	return files, nil
}

// Apply implements network.Backend: write the configuration and start,
// reload, or stop the unit to match.
func (u *Unbound) Apply(ctx context.Context, files network.Files) error {
	conf, wanted := files[unboundConfName]
	current, err := u.Snapshot()
	if err != nil {
		return err
	}
	if !wanted {
		if len(current) == 0 {
			return nil
		}
		if _, err := u.cmd().Run(ctx, "systemctl", "disable", "--now", UnboundUnit); err != nil {
			return fmt.Errorf("stop %s: %w", UnboundUnit, err)
		}
		return os.Remove(u.ConfPath())
	}
	if !u.Installed(ctx) {
		return errors.New("the validating resolver is not set up on this router: run `ostiole repair` once as root")
	}
	if current[unboundConfName] == conf && u.Active(ctx) {
		return nil
	}
	if err := os.MkdirAll(u.dir(), 0o755); err != nil { //nolint:gosec // unbound reads this unprivileged
		return err
	}
	if err := writeFile(u.ConfPath(), conf); err != nil {
		return err
	}
	if out, err := u.cmd().Run(ctx, "systemctl", "enable", "--now", UnboundUnit); err != nil {
		return fmt.Errorf("enable %s: %w: %s", UnboundUnit, err, strings.TrimSpace(string(out)))
	}
	if out, err := u.cmd().Run(ctx, "systemctl", "restart", UnboundUnit); err != nil {
		return fmt.Errorf("restart %s: %w: %s", UnboundUnit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Installed reports whether the ostiole-unbound unit exists.
func (u *Unbound) Installed(ctx context.Context) bool {
	_, err := u.cmd().Run(ctx, "systemctl", "cat", UnboundUnit)
	return err == nil
}

// Active reports whether the unit is running.
func (u *Unbound) Active(ctx context.Context) bool {
	out, err := u.cmd().Run(ctx, "systemctl", "is-active", UnboundUnit)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}

// Reload HUPs unbound, which reloads it: the cache is flushed and the
// configuration re-read. The process stays up, so the root zone and the
// trust anchor are kept.
func (u *Unbound) Reload(ctx context.Context) error {
	if out, err := u.cmd().Run(ctx, "systemctl", "reload", UnboundUnit); err != nil {
		return fmt.Errorf("reload %s: %w: %s", UnboundUnit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// UnboundUnitContent renders the ostiole-unbound unit. dnsmasq waits for
// it, so the resolver is up before anything forwards to it.
func UnboundUnitContent(binary, checkconf, anchorTool, conf, anchor string) string {
	var pre strings.Builder
	if anchorTool != "" {
		// A failure here is not fatal: unbound ships a built-in anchor and
		// the router may be offline at boot.
		fmt.Fprintf(&pre, "ExecStartPre=-%s -a %s\n", anchorTool, anchor)
	}
	if checkconf != "" {
		fmt.Fprintf(&pre, "ExecStartPre=%s %s\n", checkconf, conf)
	}
	return fmt.Sprintf(`[Unit]
Description=Ostiole validating DNS resolver (unbound)
Documentation=https://github.com/rforced/ostiole
After=network.target ostiole-firewall.service
Before=%[4]s

[Service]
Type=simple
%[1]sExecStart=%[2]s -d -c %[3]s
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=2
ProtectSystem=full
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`, pre.String(), binary, conf, Unit)
}

// Dialer is how a reachability probe opens a connection; swapped in tests.
type Dialer func(ctx context.Context, network, address string) (net.Conn, error)

// UnreachableTLS dials each DNS over TLS upstream on its port and lists
// the ones that do not answer. A network that blocks port 853 makes every
// name fail with nothing in unbound's own state to say why; this is the
// line on the DNS page that says it.
func UnreachableTLS(ctx context.Context, ups []model.TLSUpstream, dial Dialer) []string {
	if dial == nil {
		dial = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	}
	bad := make([]string, len(ups))
	var wg sync.WaitGroup
	for i, up := range ups {
		wg.Go(func() {
			addr := up.Address
			if _, _, err := net.SplitHostPort(addr); err != nil {
				addr = net.JoinHostPort(addr, "853")
			}
			c, err := dial(ctx, "tcp", addr)
			if err != nil {
				bad[i] = up.Address
				return
			}
			_ = c.Close()
		})
	}
	wg.Wait()
	out := []string{}
	for _, b := range bad {
		if b != "" {
			out = append(out, b)
		}
	}
	return out
}
