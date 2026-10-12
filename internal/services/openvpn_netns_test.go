package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
	"ostiole/internal/openvpn"
)

// OpenVPN connects with what is rendered for it to a server in the next
// namespace that pushes a route, a redirect and DNS: the typed route
// carries a datagram through the tunnel and back, and the router's default
// route is left as it was. With the server's routes taken, the pushed one
// lands and the redirect still does not.
func TestOpenVPNConnectsAsRendered(t *testing.T) {
	if !netnstest.Enter(t, "openvpn") {
		return
	}
	dir := t.TempDir()
	lab := ovpnServer(t, dir)

	// A default route of the router's own, for the redirect to replace.
	netnstest.Dummy(t, "up0", "192.0.2.2/24")
	if err := netlink.AddRoute(netlink.Route{
		Dst: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Gw:  net.ParseIP("192.0.2.1"),
	}); err != nil {
		t.Fatal(err)
	}
	before := ovpnRoutes(t, "up0", "ovc0")

	tunnel := model.OpenVPN{
		Remotes:        []model.OpenVPNRemote{{Host: "10.202.0.2", Port: 1194, Proto: "udp"}},
		CAPEM:          lab.ca,
		CertPEM:        lab.cert,
		KeyPEM:         lab.key,
		TLSCryptKeyPEM: lab.tlsCrypt,
		Routes:         []string{"10.201.0.0/24"},
	}
	stop := ovpnClient(t, dir, "ovpn0", tunnel)
	ovpnEcho(t)
	ovpnCheckRoutes(t, "ovpn0", before)
	stop()

	tunnel.Routes, tunnel.PushedRoutes = nil, true
	ovpnClient(t, dir, "ovpn1", tunnel)
	ovpnEcho(t)
	ovpnCheckRoutes(t, "ovpn1", before)
}

// ovpnLab is what the client needs of the server: its CA, a certificate
// and key the CA signed, and the tls-crypt key, all PEM.
type ovpnLab struct {
	ca, cert, key, tlsCrypt string
}

// ovpnServer runs an OpenVPN server in a namespace of its own, joined to
// this one by a veth (ovc0 10.202.0.1 here, ovs0 10.202.0.2 there), with
// 10.201.0.1 on a dummy behind it echoing UDP on port 7007.
func ovpnServer(t *testing.T, dir string) ovpnLab {
	t.Helper()
	ca, caKey := ovpnCert(t, "lab-ca", nil, nil, 0)
	serverCert, serverKey := ovpnCert(t, "lab-server", ca, caKey, x509.ExtKeyUsageServerAuth)
	clientCert, clientKey := ovpnCert(t, "lab-client", ca, caKey, x509.ExtKeyUsageClientAuth)
	tlsCrypt := filepath.Join(dir, "tls-crypt.key")
	if out, err := exec.CommandContext(t.Context(), "openvpn", "--genkey", "tls-crypt", tlsCrypt).CombinedOutput(); err != nil {
		t.Fatalf("openvpn --genkey: %v\n%s", err, out)
	}
	key, err := os.ReadFile(tlsCrypt)
	if err != nil {
		t.Fatal(err)
	}
	lab := ovpnLab{ca: ovpnPEM(t, ca), cert: ovpnPEM(t, clientCert), key: ovpnPEM(t, clientKey), tlsCrypt: string(key)}

	for name, text := range map[string]string{
		"ca.pem": lab.ca, "server.pem": ovpnPEM(t, serverCert), "server.key": ovpnPEM(t, serverKey),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conf := filepath.Join(dir, "server.conf")
	if err := os.WriteFile(conf, []byte(strings.Join([]string{
		"dev tun",
		"proto udp",
		"local 10.202.0.2",
		"port 1194",
		"server 10.200.0.0 255.255.255.0",
		"topology subnet",
		`push "route 10.201.0.0 255.255.255.0"`,
		`push "redirect-gateway def1"`,
		`push "dhcp-option DNS 10.201.0.1"`,
		"data-ciphers AES-256-GCM:CHACHA20-POLY1305",
		"dh none",
		"keepalive 10 60",
		"verb 3",
		"disable-dco",
		"ca " + filepath.Join(dir, "ca.pem"),
		"cert " + filepath.Join(dir, "server.pem"),
		"key " + filepath.Join(dir, "server.key"),
		"tls-crypt " + tlsCrypt,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	ns := netnstest.NewNS(t)
	if err := netlink.AddVeth("ovc0", "ovs0"); err != nil {
		t.Fatal(err)
	}
	if err := netlink.SetLinkNamespace(netnstest.Link(t, "ovs0").Index, int(ns.Fd())); err != nil {
		t.Fatal(err)
	}
	ovpnUp(t, "ovc0", "10.202.0.1/24")
	var echo net.PacketConn
	var server *ovpnProcess
	netnstest.Do(t, ns, func() {
		ovpnUp(t, "ovs0", "10.202.0.2/24")
		netnstest.Dummy(t, "fake0", "10.201.0.1/24")
		var err error
		if echo, err = net.ListenPacket("udp4", "10.201.0.1:7007"); err != nil {
			t.Fatal(err)
		}
		// Started here, the server and the tun device it makes are in ns.
		server = ovpnStart(t, dir, "server", conf)
	})
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], from)
		}
	}()
	// A client that sends before the server listens waits out a retry.
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(server.log(), "Initialization Sequence Completed"); {
		if server.exited() || time.Now().After(deadline) {
			t.Fatal("the server never started")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return lab
}

// ovpnClient renders a configuration holding tunnel as the interface
// name, with the management socket in dir, runs OpenVPN on it in this
// namespace as the unit does and waits until the tunnel is up. It returns
// a function that stops it and waits for the device to go.
func ovpnClient(t *testing.T, dir, name string, tunnel model.OpenVPN) func() {
	t.Helper()
	cfg := &model.Config{Interfaces: []model.Interface{{
		Name: name, Enabled: true,
		IPv4: model.IPv4{Mode: model.AddrNone}, IPv6: model.IPv6{Mode: model.AddrNone},
		OpenVPN: &tunnel,
	}}}
	files, err := (&OpenVPN{Dir: dir}).Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	from := "management " + openvpn.SocketPath(openvpn.SocketDir, name) + " unix\n"
	conf := files[name+openvpnConfExt]
	if !strings.Contains(conf, from) {
		t.Fatalf("no %q in the rendered configuration:\n%s", from, conf)
	}
	conf = strings.Replace(conf, from, "management "+openvpn.SocketPath(dir, name)+" unix\n", 1)
	// The kernel's data channel adds a peer only for root on the host, not
	// root in a user namespace, so the tunnel here runs in userspace.
	conf = "disable-dco\n" + conf
	path := filepath.Join(dir, name+openvpnConfExt)
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	p := ovpnStart(t, dir, name, path, "--suppress-timestamps", "--nobind")

	status := &openvpn.Client{Dir: dir}
	var st openvpn.State
	for deadline := time.Now().Add(20 * time.Second); !st.Connected; {
		if p.exited() || time.Now().After(deadline) {
			t.Fatalf("%s never connected; last state %q", name, st.State)
		}
		time.Sleep(200 * time.Millisecond)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		st, _ = status.Status(ctx, name)
		cancel()
	}
	if channel := status.DataChannel(name); channel != "userspace" {
		t.Fatalf("%s is connected with a data channel of %q, want userspace", name, channel)
	}
	t.Logf("%s connected as %s", name, st.TunnelAddress)

	return func() {
		t.Helper()
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			if _, err := netlink.LinkByName(name); err != nil && p.exited() {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s did not stop", name)
			}
		}
	}
}

// ovpnEcho sends a datagram to the server's far network and waits for it
// to come back.
func ovpnEcho(t *testing.T) {
	t.Helper()
	conn, err := net.Dial("udp4", "10.201.0.1:7007")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 64)
	for range 5 {
		if _, err := conn.Write([]byte("lab")); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		if n, err := conn.Read(buf); err == nil && string(buf[:n]) == "lab" {
			return
		}
	}
	t.Fatal("nothing came back from 10.201.0.1 through the tunnel")
}

// ovpnCheckRoutes wants the routes through the tunnel to be its own
// network and the server's far one, through the server's address there,
// and every other route as before.
func ovpnCheckRoutes(t *testing.T, name string, before []string) {
	t.Helper()
	if got := ovpnRoutes(t, "up0", "ovc0"); !slices.Equal(got, before) {
		t.Errorf("the routes off the tunnel are %q, were %q", got, before)
	}
	want := []string{"10.200.0.0/24 dev " + name, "10.201.0.0/24 via 10.200.0.1 dev " + name}
	if got := ovpnRoutes(t, name); !slices.Equal(got, want) {
		t.Errorf("the routes through %s are %q, want %q", name, got, want)
	}
}

// ovpnRoutes lists the IPv4 routes of the main table leaving through the
// named links, sorted, in the shape ip(8) shows.
func ovpnRoutes(t *testing.T, links ...string) []string {
	t.Helper()
	all, err := netlink.Links()
	if err != nil {
		t.Fatal(err)
	}
	names := map[int]string{}
	for _, l := range all {
		names[l.Index] = l.Name
	}
	routes, err := netlink.Routes(unix.AF_INET, netlink.RouteFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range routes {
		if !slices.Contains(links, names[r.LinkIndex]) {
			continue
		}
		route := r.Dst.String()
		if r.Gw != nil {
			route += " via " + r.Gw.String()
		}
		out = append(out, route+" dev "+names[r.LinkIndex])
	}
	slices.Sort(out)
	return out
}

func ovpnUp(t *testing.T, name, addr string) {
	t.Helper()
	link := netnstest.Link(t, name)
	if err := netlink.AddAddr(link.Index, netip.MustParsePrefix(addr)); err != nil {
		t.Fatal(err)
	}
	if err := netlink.SetLinkUp(link.Index); err != nil {
		t.Fatal(err)
	}
}

// ovpnProcess is a running openvpn writing to a log file.
type ovpnProcess struct {
	cmd     *exec.Cmd
	logPath string
	done    chan struct{}
}

// ovpnStart runs openvpn on conf in the namespace the call is made in until
// the test ends, logging to <dir>/<name>.log, which a failed test prints.
func ovpnStart(t *testing.T, dir, name, conf string, args ...string) *ovpnProcess {
	t.Helper()
	logPath := filepath.Join(dir, name+".log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	cmd := exec.Command("openvpn", append(args, "--config", conf)...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &ovpnProcess{cmd: cmd, logPath: logPath, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.done
		if t.Failed() {
			t.Logf("openvpn %s:\n%s", name, p.log())
		}
	})
	return p
}

func (p *ovpnProcess) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *ovpnProcess) log() string {
	raw, _ := os.ReadFile(p.logPath)
	return string(raw)
}

// ovpnCert makes an ECDSA P-256 key and a certificate for it: a CA's when
// parent is nil, else one parent signs for the use given.
func ovpnCert(t *testing.T, cn string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, use x509.ExtKeyUsage) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{use},
		BasicConstraintsValid: true,
	}
	if parent == nil {
		tmpl.IsCA = true
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		tmpl.ExtKeyUsage = nil
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// ovpnPEM encodes a certificate or a private key.
func ovpnPEM(t *testing.T, v any) string {
	t.Helper()
	switch v := v.(type) {
	case *x509.Certificate:
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: v.Raw}))
	case *ecdsa.PrivateKey:
		der, err := x509.MarshalPKCS8PrivateKey(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	}
	panic(fmt.Sprintf("ovpnPEM: %T", v))
}
