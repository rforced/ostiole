package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ostiole/internal/model"
	"ostiole/internal/network"
	"ostiole/internal/openvpn"
	"ostiole/internal/store"
)

// OpenVPN paths and names.
const (
	// OpenVPNUnit is a templated unit: one instance per tunnel, named
	// after the interface.
	OpenVPNUnit = "ostiole-openvpn@.service"

	openvpnConfExt = ".conf"
	openvpnAuthExt = ".auth"
	// openvpnCiphers is the data channel's default: the AEAD ciphers the
	// kernel's data channel takes.
	openvpnCiphers = "AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305"
)

// OpenVPN dials the configured OpenVPN servers, one instance of the unit
// per tunnel. The daemon creates the device and the server addresses it;
// the routes are the typed ones, and the router's default route and DNS
// are never taken from the server.
type OpenVPN struct {
	// Dir holds the generated files, under the configuration directory.
	Dir string
	// Cmd runs systemctl; default execs it.
	Cmd network.Commander
}

var (
	_ network.Backend = (*OpenVPN)(nil)
	_ Preflighter     = (*OpenVPN)(nil)
)

// NewOpenVPN returns a backend writing under configDir.
func NewOpenVPN(configDir string) *OpenVPN {
	return &OpenVPN{Dir: OpenVPNDir(configDir), Cmd: execCommander{}}
}

// OpenVPNDir is where the generated files go for a given configuration
// directory. The unit reads a tunnel's configuration from here.
func OpenVPNDir(configDir string) string {
	if configDir == "" {
		configDir = store.DefaultDir
	}
	return filepath.Join(configDir, "openvpn")
}

// OpenVPNUnitFor is the instance of the templated unit that runs one
// tunnel.
func OpenVPNUnitFor(name string) string {
	return strings.Replace(OpenVPNUnit, "@.", "@"+name+".", 1)
}

func (o *OpenVPN) dir() string {
	if o.Dir == "" {
		return OpenVPNDir("")
	}
	return o.Dir
}

func (o *OpenVPN) cmd() network.Commander {
	if o.Cmd == nil {
		return execCommander{}
	}
	return o.Cmd
}

func (o *OpenVPN) path(name string) string { return filepath.Join(o.dir(), name) }

// Name implements network.Backend.
func (o *OpenVPN) Name() string { return "openvpn" }

// Render implements network.Backend: a configuration per enabled tunnel,
// and the credentials beside it when it has a username.
func (o *OpenVPN) Render(cfg *model.Config) (network.Files, error) {
	files := network.Files{}
	for _, in := range cfg.OpenVPNInterfaces() {
		files[in.Name+openvpnConfExt] = o.conf(in)
		if in.OpenVPN.Username != "" {
			files[in.Name+openvpnAuthExt] = in.OpenVPN.Username + "\n" + in.OpenVPN.Password + "\n"
		}
	}
	return files, nil
}

func (o *OpenVPN) conf(in model.Interface) string {
	v := in.OpenVPN
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("client")
	line("dev %s", in.Name)
	line("dev-type tun")
	udp := true
	for _, r := range v.Remotes {
		line("remote %s %d %s", r.Host, r.Port, r.ProtoOrDefault())
		udp = udp && r.ProtoOrDefault() == "udp"
	}
	if v.RemoteRandom {
		line("remote-random")
	}
	line("resolv-retry infinite")
	line("nobind")
	line("persist-tun")
	line("remote-cert-tls server")
	if v.VerifyName != "" {
		line("verify-x509-name %q name", v.VerifyName)
	}
	line("tls-version-min 1.2")
	ciphers := openvpnCiphers
	if len(v.DataCiphers) > 0 {
		ciphers = strings.Join(v.DataCiphers, ":")
	}
	line("data-ciphers %s", ciphers)
	if v.Username != "" {
		line("auth-user-pass %s", o.path(in.Name+openvpnAuthExt))
	}
	line("auth-nocache")
	k := v.KeepaliveSeconds()
	line("keepalive %d %d", k, 6*k)
	// Over TCP the server sees the connection close, and the daemon says
	// so in a NOTICE for every start.
	if udp {
		line("explicit-exit-notify 1")
	}
	pulled := []string{"redirect-gateway", "dhcp-option", "dns ", "block-outside-dns"}
	if !v.PushedRoutes {
		pulled = append(pulled, "route ", "route-ipv6 ")
	}
	for _, p := range pulled {
		line("pull-filter ignore %q", p)
	}
	// The server's pushed tun-mtu otherwise wins over the typed one.
	if in.MTU > 0 {
		line("tun-mtu %d", in.MTU)
		line("pull-filter ignore %q", "tun-mtu")
	}
	for _, r := range in.TunnelRoutes() {
		p := r.Prefix
		if p.Addr().Is4() {
			line("route %s %s", p.Addr(), net.IP(net.CIDRMask(p.Bits(), 32)))
		} else {
			line("route-ipv6 %s", p)
		}
	}
	line("management %s unix", openvpn.SocketPath(openvpn.SocketDir, in.Name))
	line("management-client-user root")
	line("verb 3")
	if v.TLSAuthKeyPEM != "" && v.KeyDirection != nil {
		line("key-direction %d", *v.KeyDirection)
	}
	for _, block := range []struct{ tag, text string }{
		{"ca", v.CAPEM}, {"cert", v.CertPEM}, {"key", v.KeyPEM},
		{"tls-crypt", v.TLSCryptKeyPEM}, {"tls-auth", v.TLSAuthKeyPEM},
	} {
		if block.text != "" {
			line("<%s>\n%s\n</%s>", block.tag, strings.TrimSpace(block.text), block.tag)
		}
	}
	return b.String()
}

// Snapshot implements network.Backend.
func (o *OpenVPN) Snapshot() (network.Files, error) {
	files := network.Files{}
	entries, err := os.ReadDir(o.dir())
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, openvpnConfExt) && !strings.HasSuffix(name, openvpnAuthExt)) {
			continue
		}
		raw, err := os.ReadFile(o.path(name))
		if err != nil {
			return nil, err
		}
		files[name] = string(raw)
	}
	return files, nil
}

// errOpenVPNMissing is what an apply says when OpenVPN is not here. The
// script installs it when asked (ADR-0013).
var errOpenVPNMissing = errors.New(
	"OpenVPN is not on this router: run `ostiole repair --openvpn` once as root")

// Preflight implements Preflighter, so a tunnel that cannot start is
// refused before the ruleset has been touched.
func (o *OpenVPN) Preflight(ctx context.Context, files network.Files) error {
	if len(files) > 0 && !o.Installed(ctx) {
		return errOpenVPNMissing
	}
	return nil
}

// Apply implements network.Backend: stop the tunnels that have gone,
// write what changed, and keep an instance of the unit per tunnel in step.
func (o *OpenVPN) Apply(ctx context.Context, files network.Files) error {
	current, err := o.Snapshot()
	if err != nil {
		return err
	}
	// A router that never dialled a tunnel gets no directory and no
	// systemctl.
	if len(files) == 0 && len(current) == 0 {
		return nil
	}
	if len(files) > 0 && !o.Installed(ctx) {
		return errOpenVPNMissing
	}

	for _, name := range tunnelsIn(current) {
		if _, keep := files[name+openvpnConfExt]; keep {
			continue
		}
		if err := systemctl(ctx, o.cmd(), "stop the tunnel "+name, "disable", "--now", OpenVPNUnitFor(name)); err != nil {
			return err
		}
		for _, ext := range []string{openvpnConfExt, openvpnAuthExt} {
			if err := os.Remove(o.path(name + ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(o.dir(), 0o700); err != nil {
		return err
	}
	for _, name := range tunnelsIn(files) {
		changed := false
		for _, ext := range []string{openvpnConfExt, openvpnAuthExt} {
			file := name + ext
			want, wanted := files[file]
			have, had := current[file]
			switch {
			case wanted && (!had || have != want):
				// 0600: the keys and the password are in here.
				if err := writeSecretFile(o.path(file), want); err != nil {
					return err
				}
				changed = true
			case !wanted && had:
				if err := os.Remove(o.path(file)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				changed = true
			}
		}
		unit := OpenVPNUnitFor(name)
		active := o.active(ctx, unit)
		if !changed && active {
			continue
		}
		// The apply does not wait on a server that may never answer; a
		// tunnel that does not come up says why on its page.
		if err := systemctl(ctx, o.cmd(), "start the tunnel "+name, "enable", "--now", "--no-block", unit); err != nil {
			return err
		}
		// A fresh start has just read the files; an instance that was
		// already running has not.
		if changed && active {
			if err := systemctl(ctx, o.cmd(), "restart the tunnel "+name, "restart", "--no-block", unit); err != nil {
				return err
			}
		}
	}
	return nil
}

// tunnelsIn names the tunnels a set of files covers, sorted.
func tunnelsIn(files network.Files) []string {
	var out []string
	for name := range files {
		if tunnel, ok := strings.CutSuffix(name, openvpnConfExt); ok {
			out = append(out, tunnel)
		}
	}
	slices.Sort(out)
	return out
}

// Installed reports whether the templated unit exists.
func (o *OpenVPN) Installed(ctx context.Context) bool {
	_, err := o.cmd().Run(ctx, "systemctl", "cat", OpenVPNUnit)
	return err == nil
}

func (o *OpenVPN) active(ctx context.Context, unit string) bool {
	out, err := o.cmd().Run(ctx, "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}
