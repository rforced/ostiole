package services

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netnstest"
)

// dnsmasq itself runs what is rendered for it, with devices asking for
// names over two networks: one server registers them and one does not, a
// device asking for wpad or for a proxy site's name gets nothing, and a
// name registered before the switch went off answers until the cleanup
// takes it off the lease.
func TestDnsmasqAnswersNamesAsRendered(t *testing.T) {
	if !netnstest.Enter(t, "dnsmasq") {
		return
	}
	bin, _ := exec.LookPath("dnsmasq")
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		t.Fatal(err)
	}
	veth(t, "v0", "v1", "10.99.0.1/24")
	veth(t, "v2", "v3", "10.98.0.1/24")
	for _, name := range []string{"all", "default", "v0", "v1", "v2", "v3"} {
		// Both ends of each pair are in this namespace, and the kernel drops
		// a packet from one of its own addresses unless told otherwise.
		sysctl(t, "net/ipv4/conf/"+name+"/accept_local", "1")
		sysctl(t, "net/ipv4/conf/"+name+"/rp_filter", "0")
	}

	dir := t.TempDir()
	d := &Dnsmasq{Dir: dir, Leases: filepath.Join(dir, "leases")}
	cfg := namesRouter()
	run := startDnsmasq(t, bin, d, cfg)

	for _, c := range []struct{ link, mac, ip, name string }{
		{"v1", "02:00:00:00:00:01", "10.99.0.150", "laptop"},
		{"v3", "02:00:00:00:00:02", "10.98.0.150", "guest"},
		{"v1", "02:00:00:00:00:03", "10.99.0.151", "wpad"},
		{"v1", "02:00:00:00:00:04", "10.99.0.152", "site"},
	} {
		if err := dhcpRequest(c.link, c.mac, c.ip, c.name); err != nil {
			t.Fatalf("%s asking for %s: %v", c.mac, c.name, err)
		}
	}
	for name, want := range map[string]string{
		"laptop.lan": "10.99.0.150", // registered
		"laptop":     "10.99.0.150",
		"guest.lan":  "", // its server does not register
		"wpad.lan":   "", // refused
		// The router's own on both networks: this query comes over
		// loopback, so localise-queries has no network to pick.
		"site.lan": "10.98.0.1,10.99.0.1",
		"site":     "10.98.0.1,10.99.0.1", // the proxy's, not the device's
	} {
		if got := lookup(t, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// Switched off, dnsmasq loads laptop back from the lease file and keeps
	// answering it; the cleanup is what takes it away.
	cfg.Services.DHCP.Servers[0].DNSRegistration = false
	run.stop()
	run = startDnsmasq(t, bin, d, cfg)
	if got := lookup(t, "laptop.lan"); got != "10.99.0.150" {
		t.Fatalf("laptop.lan after the switch = %q; dnsmasq no longer keeps names, so the cleanup may be moot", got)
	}
	run.stop()
	files, err := d.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	stripped, err := d.strippedLeases(files[confName])
	if err != nil || stripped == nil {
		t.Fatalf("stripped = %q, %v", stripped, err)
	}
	if err := os.WriteFile(d.Leases, stripped, 0o644); err != nil {
		t.Fatal(err)
	}
	startDnsmasq(t, bin, d, cfg)
	if got := lookup(t, "laptop.lan"); got != "" {
		t.Errorf("laptop.lan after the cleanup = %q, want no answer", got)
	}
	if err := dhcpRequest("v1", "02:00:00:00:00:01", "10.99.0.150", "laptop"); err != nil {
		t.Fatal(err)
	}
	if got := lookup(t, "laptop.lan"); got != "" {
		t.Errorf("laptop.lan after it renewed = %q, want no answer", got)
	}
}

// namesRouter serves DHCP on v0, which registers names, and v2, which does
// not, with a proxy site on both.
func namesRouter() *model.Config {
	cfg := model.Starter(model.StarterOptions{LAN: "v0", LANAddress: "10.99.0.1/24", WAN: "wan0", Services: true})
	lan, _ := cfg.Interface("v0")
	guest := *lan
	guest.Name = "v2"
	guest.IPv4.Address = "10.98.0.1/24"
	cfg.Interfaces = append(cfg.Interfaces, guest)
	cfg.Services.DHCP.Servers[0].DNSRegistration = true
	cfg.Services.DHCP.Servers = append(cfg.Services.DHCP.Servers, model.DHCPServer{
		Interface: "v2", Enabled: true, RangeStart: "10.98.0.100", RangeEnd: "10.98.0.199",
	})
	cfg.Services.Proxy = model.Proxy{
		Enabled: true,
		Zones:   []string{"lan"},
		Pools:   []model.ProxyPool{{ID: "media", Upstreams: []model.ProxyUpstream{{Address: "10.99.0.11:8096"}}}},
		Sites:   []model.ProxySite{{ID: "site", Enabled: true, Pool: "media", Hosts: []string{"site.lan"}}},
	}
	return cfg
}

type dnsmasqRun struct {
	cmd  *exec.Cmd
	done chan error
}

func (r dnsmasqRun) stop() {
	_ = r.cmd.Process.Kill()
	<-r.done
}

// startDnsmasq renders cfg into d's directory and runs dnsmasq on it until
// the test ends, waiting for it to answer.
func startDnsmasq(t *testing.T, bin string, d *Dnsmasq, cfg *model.Config) dnsmasqRun {
	t.Helper()
	files, err := d.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(d.Dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d.Dir, BlockConfName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--keep-in-foreground", "--user=", "--group=", "--pid-file=",
		"--conf-file="+filepath.Join(d.Dir, confName), "--log-facility="+filepath.Join(d.Dir, "log"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	run := dnsmasqRun{cmd: cmd, done: make(chan error, 1)}
	go func() { run.done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
	})
	for range 50 {
		if lookup(t, "site.lan") != "" {
			return run
		}
		select {
		case err := <-run.done:
			log, _ := os.ReadFile(filepath.Join(d.Dir, "log"))
			t.Fatalf("dnsmasq exited: %v\n%s", err, log)
		case <-time.After(100 * time.Millisecond):
		}
	}
	log, _ := os.ReadFile(filepath.Join(d.Dir, "log"))
	t.Fatalf("dnsmasq never answered\n%s", log)
	return run
}

// lookup asks the test's dnsmasq for name's IPv4 address, "" when it has
// none.
func lookup(t *testing.T, name string) string {
	t.Helper()
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", "127.0.0.1:53")
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := r.LookupIP(ctx, "ip4", name+".")
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return ""
	}
	if err != nil {
		return ""
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

func veth(t *testing.T, name, peer, addr string) {
	t.Helper()
	link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: peer}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	a, err := netlink.ParseAddr(addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, a); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{name, peer} {
		l, err := netlink.LinkByName(n)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(l); err != nil {
			t.Fatal(err)
		}
	}
}

func sysctl(t *testing.T, key, value string) {
	t.Helper()
	if err := os.WriteFile("/proc/sys/"+key, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dhcpRequest asks for ip in INIT-REBOOT form, which an authoritative
// server answers without an offer first, and waits for the ACK. The
// broadcast flag brings the answer back without an address to send it to.
func dhcpRequest(link, mac, ip, hostname string) error {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return err
	}
	xid := uint32(time.Now().UnixNano())
	pkt := make([]byte, 240, 300)
	pkt[0], pkt[1], pkt[2] = 1, 1, 6 // BOOTREQUEST over Ethernet
	binary.BigEndian.PutUint32(pkt[4:], xid)
	binary.BigEndian.PutUint16(pkt[10:], 0x8000)
	copy(pkt[28:], hw)
	copy(pkt[236:], []byte{99, 130, 83, 99})
	pkt = append(pkt, 53, 1, 3)                                           // DHCPREQUEST
	pkt = append(pkt, append([]byte{50, 4}, net.ParseIP(ip).To4()...)...) // requested address
	pkt = append(pkt, append([]byte{12, byte(len(hostname))}, hostname...)...)
	pkt = append(pkt, append([]byte{61, 7, 1}, hw...)...)
	pkt = append(pkt, 255)

	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); serr != nil {
				return
			}
			if serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); serr != nil {
				return
			}
			serr = syscall.BindToDevice(int(fd), link)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	conn, err := lc.ListenPacket(context.Background(), "udp4", "0.0.0.0:68")
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.WriteTo(pkt, &net.UDPAddr{IP: net.IPv4bcast, Port: 67}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		if n < 240 || binary.BigEndian.Uint32(buf[4:]) != xid {
			continue
		}
		for i := 240; i+1 < n && buf[i] != 255; {
			if buf[i] == 0 {
				i++
				continue
			}
			if buf[i] == 53 && i+2 < n {
				if buf[i+2] != 5 {
					return fmt.Errorf("answered with DHCP message type %d, not an ACK", buf[i+2])
				}
				return nil
			}
			i += 2 + int(buf[i+1])
		}
	}
}
