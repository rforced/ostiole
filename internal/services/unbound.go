package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// UnboundPort is where unbound listens on the loopback. It is not 53:
	// dnsmasq owns that, and not 5353, which is mDNS.
	UnboundPort = 5335
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
	"/etc/ssl/certs/ca-certificates.crt",     // Debian, Arch, Alpine
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
	return dns.Enabled && (dns.Resolver == model.ResolverValidate || dns.Resolver == model.ResolverTLS)
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
	b.WriteString("    verbosity: 0\n")
	fmt.Fprintf(&b, "    interface: 127.0.0.1@%d\n", UnboundPort)
	fmt.Fprintf(&b, "    interface: ::1@%d\n", UnboundPort)
	b.WriteString("    do-ip4: yes\n    do-ip6: yes\n    do-udp: yes\n    do-tcp: yes\n")
	// Only dnsmasq talks to it; everyone else is refused.
	b.WriteString("    access-control: 0.0.0.0/0 refuse\n")
	b.WriteString("    access-control: 127.0.0.0/8 allow\n")
	b.WriteString("    access-control: ::0/0 refuse\n")
	b.WriteString("    access-control: ::1 allow\n")
	b.WriteString("    username: \"unbound\"\n")
	fmt.Fprintf(&b, "    directory: %q\n", u.dir())
	b.WriteString("    chroot: \"\"\n")
	b.WriteString("    use-syslog: no\n")
	b.WriteString("    logfile: \"\"\n")
	b.WriteString("    hide-identity: yes\n    hide-version: yes\n")
	b.WriteString("    harden-glue: yes\n    harden-dnssec-stripped: yes\n    harden-below-nxdomain: yes\n")
	b.WriteString("    qname-minimisation: yes\n    aggressive-nsec: yes\n    prefetch: yes\n")
	b.WriteString("    rrset-roundrobin: yes\n")
	b.WriteString("    cache-min-ttl: 60\n")
	fmt.Fprintf(&b, "    auto-trust-anchor-file: %q\n", u.anchor())
	if dns.Resolver == model.ResolverTLS {
		fmt.Fprintf(&b, "    tls-cert-bundle: %q\n", u.certBundle())
		b.WriteString("\nforward-zone:\n")
		b.WriteString("    name: \".\"\n")
		b.WriteString("    forward-tls-upstream: yes\n")
		for _, up := range dns.TLSUpstreams {
			fmt.Fprintf(&b, "    forward-addr: %s@853#%s\n", up.Address, up.Hostname)
		}
	}
	return b.String()
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
		return fmt.Errorf("the validating resolver is not set up on this box: run `ostiole services setup --with-resolver` once as root")
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

// UnboundUnitContent renders the ostiole-unbound unit. dnsmasq waits for
// it, so the resolver is up before anything forwards to it.
func UnboundUnitContent(binary, checkconf, anchorTool, conf, anchor string) string {
	var pre strings.Builder
	if anchorTool != "" {
		// A failure here is not fatal: unbound ships a built-in anchor and
		// the box may be offline at boot.
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
